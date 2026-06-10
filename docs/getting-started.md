# Getting Started

This guide walks through setting up `cascade` in your repository.

## Prerequisites

- Go 1.23+ (for the CLI)
- A GitHub repository with Actions enabled
- Trunk-based development (single primary branch)

## Step 1: Install the CLI

```bash
# Install latest stable release
go install github.com/stablekernel/cascade/cmd/cascade@latest

# Install bleeding edge from master
go install github.com/stablekernel/cascade/cmd/cascade@master

# Install a specific version
go install github.com/stablekernel/cascade/cmd/cascade@v2.0.4

# Verify
cascade version
```

In GitHub Actions, generated workflows install the CLI for you via the setup action, so you don't need to add it explicitly. To pin a version, set `cli_version` in your manifest.

If you need to invoke it manually:

```yaml
- uses: stablekernel/cascade/.github/actions/setup-cli@master
  with:
    token: ${{ secrets.GITHUB_TOKEN }}
    # version: latest  # or 'beta', or a specific version like 'v2.0.4'
```

The setup action downloads the release archive (`tar.gz`) from GoReleaser and installs the `cascade` binary on `PATH`.

## Step 2: Create the manifest

Create `.github/manifest.yaml` in your repository:

```yaml
ci:
  config:
    trunk_branch: master
    environments: [dev, test, prod]
    cli_version: v2.0.4

    # Optional pre-build validation
    validate:
      workflow: .github/workflows/validate.yaml

    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers:
          - "src/**"
          - "Dockerfile"
          - "go.mod"

    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
        triggers:
          - "infra/**"

      - name: services
        workflow: .github/workflows/deploy-services.yaml
        depends_on: [app]    # waits for build-app to succeed

    # Optional: retag artifacts when an RC is published as final
    publish:
      workflow: .github/workflows/publish.yaml

    changelog:
      contributors: true

  state:
    dev: {}
    test: {}
    prod: {}
```

The framework owns `state:` and `latest_release:`. The `state: { dev: {}, ... }` skeleton is enough. The workflows fill in the details on every run.

See [Configuration Reference](configuration.md) for every field.

### No-environment mode

For library/CLI projects that publish releases without environment deployments, omit `environments`:

```yaml
ci:
  config:
    trunk_branch: master
    cli_version: v2.0.4
    builds:
      - name: cli
        workflow: .github/workflows/build-cli.yaml
        triggers: [cmd/**, internal/**, go.mod]
    changelog:
      contributors: true
```

Commits create RC pre-releases automatically; a `promote` dispatch (default mode) publishes the final release.

## Step 3: Create Callback Workflows

The framework calls your workflows. Create them following the [Callback Contract](callback-contract.md).

### Build Workflow Example

`.github/workflows/build-app.yaml`:

```yaml
name: Build App

on:
  workflow_call:
    inputs:
      environment:
        type: string
        required: true
      sha:
        type: string
        required: true
      dry_run:
        type: boolean
        required: false
        default: false
    outputs:
      artifact_id:
        description: Immutable artifact identifier (e.g., image digest)
        value: ${{ jobs.build.outputs.artifact_id }}
      image_tag:
        description: Docker image tag
        value: ${{ jobs.build.outputs.image_tag }}

jobs:
  build:
    runs-on: ubuntu-latest
    outputs:
      artifact_id: ${{ steps.push.outputs.digest }}
      image_tag: ${{ steps.meta.outputs.tag }}
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ inputs.sha }}

      - name: Generate tag
        id: meta
        run: |
          TAG="${{ github.sha }}-$(date +%s)"
          echo "tag=$TAG" >> "$GITHUB_OUTPUT"

      - name: Build image
        run: docker build -t myrepo/app:${{ steps.meta.outputs.tag }} .

      - name: Push image
        id: push
        if: ${{ !inputs.dry_run }}
        run: |
          docker push myrepo/app:${{ steps.meta.outputs.tag }}
          DIGEST=$(docker inspect --format='{{index .RepoDigests 0}}' \
            myrepo/app:${{ steps.meta.outputs.tag }} | cut -d@ -f2)
          echo "digest=$DIGEST" >> "$GITHUB_OUTPUT"
```

### Deploy Workflow Example

`.github/workflows/deploy-services.yaml`:

```yaml
name: Deploy Services

on:
  workflow_call:
    inputs:
      environment:
        type: string
        required: true
      sha:
        type: string
        required: true
      image_tag:
        type: string
        required: true
      dry_run:
        type: boolean
        required: false
        default: false

jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: ${{ inputs.environment }}
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ inputs.sha }}

      - name: Deploy
        if: ${{ !inputs.dry_run }}
        run: |
          echo "Deploying ${{ inputs.image_tag }} to ${{ inputs.environment }}"
          # Your deployment logic
```

### Publish Workflow Example

If you configured `publish:` in the manifest, create the callback. It runs once per build when an RC is published as a final release:

```yaml
name: Publish

on:
  workflow_call:
    inputs:
      build_name:
        type: string
        required: true
      old_version:
        type: string
        required: true   # e.g., v1.0.0-rc.2
      new_version:
        type: string
        required: true   # e.g., v1.0.0
      sha:
        type: string
        required: true
      artifact_id:
        type: string
        required: false  # immutable digest if your build declares it

jobs:
  retag:
    runs-on: ubuntu-latest
    steps:
      - name: Retag image
        run: |
          docker pull myrepo/${{ inputs.build_name }}:${{ inputs.old_version }}
          docker tag \
            myrepo/${{ inputs.build_name }}:${{ inputs.old_version }} \
            myrepo/${{ inputs.build_name }}:${{ inputs.new_version }}
          docker push myrepo/${{ inputs.build_name }}:${{ inputs.new_version }}
```

## Step 4: Generate Orchestration Workflows

The CLI generates orchestration and promotion workflows from the manifest:

```bash
# Preview
cascade generate-workflow --dry-run

# Write the files
cascade generate-workflow --force
```

This creates:
- `.github/workflows/orchestrate.yaml` runs on merge to trunk
- `.github/workflows/promote.yaml` handles manual promotion between environments

## Step 5: Validate

```bash
cascade parse-config
```

Validates the manifest and prints any errors.

## Step 6: Commit and Push

```bash
git add .github/manifest.yaml .github/workflows/
git commit -m "feat: add trunk-based CI/CD"
git push origin master
```

The orchestrate workflow runs automatically on the next merge.

## What Happens Next

1. **On every merge to trunk:**
   - Framework detects which files changed
   - Runs validation (if configured)
   - Triggers relevant builds and deploys
   - Updates state in `.github/manifest.yaml`
   - Creates/updates a draft pre-release with the changelog

2. **To promote to test:**
   - Actions → Promote workflow
   - Select `dev-to-test`
   - Run

3. **To promote to prod:**
   - Actions → Promote workflow
   - Select `test-to-prod` (or `dev-to-prod` for full cascade)
   - Run

   The release is published, the publish callback fires, and a git tag is created.

## Common Issues

### Workflow not triggering

- Branch name matches `trunk_branch` in the manifest
- Trigger patterns match the changed files
- Workflow file is in `.github/workflows/`

### Callback not found

- Workflow path in the manifest matches the actual file path
- Workflow has `on: workflow_call`
- Required inputs/outputs are declared

### Permission errors

The generated workflows include the necessary permissions. If you wrap them in your own workflow, ensure:

```yaml
permissions:
  contents: write
  actions: write    # promote dispatches release builds
```

## Next Steps

- [Configuration Reference](configuration.md) for every field
- [Callback Contract](callback-contract.md) for callback inputs/outputs
- [Workflows](workflows.md) for generated workflow internals
