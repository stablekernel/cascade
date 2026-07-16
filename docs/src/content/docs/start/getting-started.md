---
title: Getting started
description: Install the Cascade CLI, write a manifest and callbacks, generate your workflows, and run your first pipeline.
---

This tutorial takes a fresh repository to a running Cascade pipeline. It is the canonical install page: every other page links here instead of repeating install steps.

## Prerequisites

- Go 1.25 or newer, to install the CLI.
- A GitHub repository with Actions enabled.
- Trunk-based development: one primary branch (this guide assumes `main`).

## Install

```bash
go install github.com/stablekernel/cascade/cmd/cascade@latest
```

`@latest` tracks the newest tagged release. To pin an exact version instead
(check the [releases page](https://github.com/stablekernel/cascade/releases/latest)
for the newest tag):

```bash
go install github.com/stablekernel/cascade/cmd/cascade@v0.16.2
```

Verify the install:

```bash
cascade version
```

You rarely need to invoke Cascade manually in CI: generated workflows install it for you via the `setup-cli` composite action, pinned to whatever `cli_version` you set in your manifest. If you need to call it directly in a workflow of your own:

```yaml
- uses: stablekernel/cascade/.github/actions/setup-cli@v0.16.2
  with:
    token: ${{ secrets.GITHUB_TOKEN }}
```

The action downloads the release archive, extracts it, and puts `cascade` on `PATH`.

## Scaffold with `cascade init`

The fastest path to a working manifest is `cascade init`. It renders the manifest and callback stubs, verifies them through the real generator, and writes them into your repository:

```bash
# Two-environment pipeline (the default topology)
cascade init --topology two-env

# Or choose your own ordered environments; the last is the release stage
cascade init --envs staging,production --name my-service

# Preview without writing anything
cascade init --topology two-env --dry-run
```

Topologies: `no-env` (library or CLI project, no deployments), `two-env` (the default), `three-env`, and `four-env`. Each produces `.github/manifest.yaml` plus build and deploy stubs under `.github/workflows/`. If a target file already exists, `init` aborts and lists the conflicts unless you pass `--force`.

Once scaffolded, skip to [write your callbacks](#write-your-callbacks) to fill in the stubs. The manual walkthrough below builds the same files by hand if you would rather start from scratch.

## Or write the manifest by hand

Create `.github/manifest.yaml`:

```yaml
ci:
  config:
    schema_version: 1
    trunk_branch: main
    environments: [dev, test, prod]
    cli_version: v0.16.2

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
        depends_on: [app]

    changelog:
      contributors: true

  state:
    dev: {}
    test: {}
    prod: {}
```

Cascade owns `state:` and `latest_release:`. The empty `state: { dev: {}, ... }` skeleton is enough to start; every run fills in the details. See the [manifest reference](/cascade/reference/manifest/) for every field.

### No-environment mode

Library and CLI projects that publish releases without deployments can omit `environments` entirely:

```yaml
ci:
  config:
    schema_version: 1
    trunk_branch: main
    cli_version: v0.16.2
    builds:
      - name: cli
        workflow: .github/workflows/build-cli.yaml
        triggers: ["cmd/**", "internal/**", "go.mod"]
    changelog:
      contributors: true
```

Commits create RC pre-releases automatically; a `promote` dispatch (default mode) publishes the final release.

## Write your callbacks

Write your callback workflows **before** you run `generate-workflow`: the generator reads each callback's `on: workflow_call` block to discover its declared outputs, so a callback that does not exist yet cannot be wired in. Follow the [callback contract](/cascade/reference/callbacks/) for the exact input and output shape.

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

jobs:
  build:
    runs-on: ubuntu-latest
    outputs:
      artifact_id: ${{ steps.push.outputs.digest }}
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ inputs.sha }}
      - name: Build and push
        id: push
        if: ${{ !inputs.dry_run }}
        run: |
          docker build -t myrepo/app:${{ inputs.sha }} .
          docker push myrepo/app:${{ inputs.sha }}
          DIGEST=$(docker inspect --format='{{index .RepoDigests 0}}' myrepo/app:${{ inputs.sha }} | cut -d@ -f2)
          echo "digest=$DIGEST" >> "$GITHUB_OUTPUT"
```

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
        run: echo "Deploying ${{ inputs.sha }} to ${{ inputs.environment }}"
```

The hand-written manifest above declares two more callbacks: create `.github/workflows/validate.yaml` and `.github/workflows/deploy-infra.yaml` the same way. Copy the `on: workflow_call` block from `deploy-services.yaml` (both take the same `environment`, `sha`, and `dry_run` inputs) and give each a job that runs your checks or your infrastructure deploy. Every workflow the manifest names must exist on disk before the next step, or the generator stops with a "no such file" error.

If you configured `publish:` in the manifest, add a matching callback that retags an RC's artifact once it is published as a final release; see the [callback contract](/cascade/reference/callbacks/) for its inputs.

## Generate

```bash
# Preview
cascade generate-workflow --dry-run

# Write the files
cascade generate-workflow --force
```

For the manifest above (two or more environments, no opt-in lanes configured), this writes:

- `.github/workflows/orchestrate.yaml`, runs on merge to trunk.
- `.github/workflows/promote.yaml`, handles manual promotion between environments.
- `.github/workflows/cascade-hotfix.yaml`, the hotfix off-ramp (emitted whenever you have two or more environments).
- `.github/workflows/cascade-rollback.yaml`, the rollback off-ramp (same condition).
- `.github/actions/manage-release/action.yaml`, the composite action that creates and updates GitHub Releases.

Opt-in companions such as `validate_check`, `merge_queue`, `pr_preview`, `drift_check`, and `external` add their own files only when you configure them; see [generated workflows reference](/cascade/reference/generated-workflows/) for the full set and each file's anatomy.

## Commit and run

```bash
git add .github/manifest.yaml .github/workflows/
git commit -m "feat: add trunk-based CI/CD"
git push origin main
```

The orchestrate workflow runs automatically on the next merge to `main`: it detects which builds and deploys your changed files trigger, runs them for the first environment, and opens or updates a draft pre-release. From there, dispatch the promote workflow from the Actions tab to advance a change through the rest of your environments.

---

**Prerequisite:** [How Cascade works](/cascade/start/how-it-works/).
**Next:** the [callback contract](/cascade/reference/callbacks/) for the full input/output shape, then [promote a release](/cascade/guides/promote/) to run your first promotion.
