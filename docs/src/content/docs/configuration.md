---
title: Configuration Reference
description: Complete reference for every field in the cascade manifest file, including config, state, and policy sections.
---

Complete reference for the manifest file (default `.github/manifest.yaml`).

## File Structure

The manifest holds both pipeline configuration and deployment state under a top-level `ci:` key:

```yaml
ci:
  config:           # Pipeline definition (you write this)
    trunk_branch: master
    environments: [dev, test, prod]
    # builds, deploys, etc.

  state:            # Deployment tracking (managed by the framework, do not edit)
    dev:
      sha: "abc123"
      version: "v1.2.0-rc.3"
      committed_at: "2026-01-15T10:30:00Z"

  latest_release:   # Most recent published release (managed)
    version: "v1.1.0"
    sha: "abc000"
```

The wrapper key (`ci:` by default) is configurable via `config.manifest_key`. The file path is configurable via `config.manifest_file`.

## Editor support

cascade ships a hand-authored JSON Schema for the manifest. Registering it with your editor gives you autocomplete, type checking, enum hints, and hover documentation while you author `.github/manifest.yaml`. The schema covers structure, types, and enums; `cascade parse-config` remains the authority for semantic and cross-field rules.

The schema is published at:

```
https://stablekernel.github.io/cascade/manifest.schema.json
```

You can also print the embedded copy with `cascade schema` (write it to a file with `cascade schema --output manifest.schema.json`).

### YAML language server directive

Add this comment to the top of `.github/manifest.yaml`. The YAML language server (used by VS Code, Neovim, and others) reads it automatically:

```yaml
# yaml-language-server: $schema=https://stablekernel.github.io/cascade/manifest.schema.json
ci:
  config:
    trunk_branch: main
```

### VS Code settings

Alternatively, map the schema to your manifest path in `settings.json`:

```json
{
  "yaml.schemas": {
    "https://stablekernel.github.io/cascade/manifest.schema.json": ".github/manifest.yaml"
  }
}
```

If your manifest uses a different path or wrapper key, point the mapping at your file. Either registration path works; the directive travels with the file, while the settings mapping is per-workspace.

## Config Section

### Top-Level Fields

```yaml
ci:
  config:
    trunk_branch: master
    environments: [dev, test, prod]
    cli_version: v2.0.4
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `trunk_branch` | string | Yes | - | Main branch (e.g., `master`, `main`) |
| `environments` | list | No | - | Promotion chain. Omit for no-env library/CLI projects. |
| `cli_version` | string | No | latest | CLI version: `latest`, `beta`, or specific version (e.g., `v2.0.4`) |
| `triggers` | list | No | - | Global path patterns that activate orchestration |
| `tag_prefix` | string | No | `v` | Version tag prefix |
| `release_token` | string | No | `${{ secrets.GITHUB_TOKEN }}` | Token expression for release API calls |
| `manifest_file` | string | No | `.github/manifest.yaml` | Path to manifest file |
| `manifest_key` | string | No | `ci` | Top-level key inside the manifest file |
| `action_folder` | string | No | `manage-release` | Folder name for the manage-release action |

:::note[Environment names are yours; roles are positional]
The `environments` list is fully configurable. cascade attaches no meaning to specific labels: `dev`, `test`, `uat`, `staging`, and `prod` are illustrative examples used throughout these docs, not reserved names. Roles are decided by position in the list, not by name. The last environment is the release stage (prod), the second-to-last is the prerelease environment, and the publish boundary is the final crossing into the last environment. The count is structural too: zero environments is release-only, one environment generates a single-environment Release workflow, and two or more enable the full promote cascade.

**Naming.** Environment, build, and deploy names become GitHub Actions job IDs and output-variable keys, so keep them identifier-safe: use letters, digits, and underscores (hyphens are read as subtraction in GitHub Actions expressions). The reserved generator-owned names `environment` and `dry_run` cannot be used as `dispatch_inputs`. Any `gha_environment` value maps to a real GitHub Environment, so GitHub's own naming rules apply there.
:::

### cli_version

Controls which CLI version the generated workflows install via setup-cli:

| Value | Behavior |
|-------|----------|
| `latest` | Most recent stable release (default) |
| `beta` | Latest build from the `master` branch |
| `vX.Y.Z` | Specific version (e.g., `v2.0.4`) |

Pin to a specific version for reproducibility. Use `beta` for early access.

### git Section

Optional git identity and signing configuration for state commits:

```yaml
ci:
  config:
    git:
      mode: custom
      user_name: deploy-bot
      user_email: deploy@example.com
      gpg_key_id: GPG_KEY_ID
      gpg_key_secret: GPG_PRIVATE_KEY
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `mode` | string | default | `default`, `custom`, or `external` |
| `user_name` | string | github-actions[bot] | Git user.name (when `mode: custom`) |
| `user_email` | string | github-actions[bot]@users.noreply.github.com | Git user.email |
| `gpg_key_id` | string | - | Secret name containing GPG key ID |
| `gpg_key_secret` | string | - | Secret name containing GPG private key |

**Modes:**
- `default`: Use `github-actions[bot]` identity
- `custom`: Use the supplied `user_name` and `user_email`
- `external`: Skip git config entirely (assume pre-configured by the runner)

**GPG signing:** When both `gpg_key_id` and `gpg_key_secret` are set, the framework imports the key, enables `commit.gpgsign`, and signs state commits.

### validate Section

Optional pre-build validation:

```yaml
ci:
  config:
    validate:
      workflow: .github/workflows/validate.yaml
      supports_dry_run: false
      triggers: [src/**]
      inputs:
        check_lint: true
      env_inputs:
        prod:
          check_security: true
      run_policy: default
      on_failure: abort
      retries: 0
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `workflow` | string | - | Path to validation workflow |
| `supports_dry_run` | bool | false | Whether the callback handles `dry_run` input |
| `triggers` | list | - | File patterns that should trigger validation |
| `inputs` | map | {} | Static inputs passed to the workflow |
| `env_inputs` | map | {} | Per-environment input overrides |
| `run_policy` | string | default | Execution policy |
| `on_failure` | string | abort | Failure handling |
| `retries` | int | 0 | Retry attempts (0-3) |

### builds Section

Builds produce artifacts (Docker images, binaries, etc.):

```yaml
ci:
  config:
    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers: [src/**, Dockerfile]
        depends_on: []
        inputs:
          dockerfile: ./Dockerfile
        env_inputs:
          prod:
            sign_image: true
        run_policy: default
        on_failure: abort
        retries: 0
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique build identifier |
| `workflow` | string | Yes | Path to build workflow |
| `triggers` | list | No | Glob patterns that trigger this build |
| `depends_on` | list | No | Other callbacks to wait for |
| `inputs` | map | No | Static inputs to workflow |
| `env_inputs` | map | No | Per-environment input overrides |
| `run_policy` | string | No | Execution policy |
| `on_failure` | string | No | Failure handling |
| `retries` | int | No | Retry attempts (0-3) |
| `permissions` | map | No | `GITHUB_TOKEN` scopes for this callback's caller job |

The build's `artifact_id` output (if declared) is captured automatically into state. Any other declared outputs are forwarded to dependent deploys as inputs.

A `permissions` map is rendered as a job-level `permissions:` block on the caller job that invokes this callback, scoping the `GITHUB_TOKEN` to least privilege for that one job. GitHub Actions treats a job-level block as the **complete** permission set: it replaces the workflow default rather than merging with it. Declare the full set the callback needs, including `contents: read` if the callback checks out code and `id-token: write` for OIDC. cascade emits exactly the scopes you declare and never injects an implicit scope.

```yaml
permissions:
  contents: read
  id-token: write
```

### deploys Section

Deploys target environments:

```yaml
ci:
  config:
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
        triggers: [cdk/**]
        depends_on: []
        supports_dry_run: true
        inputs:
          stack_name: my-stack
        env_inputs:
          prod:
            approval_required: true
        run_policy: default
        on_failure: abort
        retries: 0
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique deploy identifier |
| `workflow` | string | Yes | Path to deploy workflow |
| `triggers` | list | No | Glob patterns that trigger this deploy |
| `depends_on` | list | No | Other callbacks to wait for |
| `supports_dry_run` | bool | No | Whether the callback handles `dry_run` |
| `inputs` | map | No | Static inputs |
| `env_inputs` | map | No | Per-environment overrides |
| `run_policy` | string | No | Execution policy |
| `on_failure` | string | No | Failure handling |
| `retries` | int | No | Retry attempts (0-3) |
| `permissions` | map | No | `GITHUB_TOKEN` scopes for this callback's caller job |

As with builds, a deploy's `permissions` map is the complete permission set for its caller job (it replaces the workflow default, not merges). Include every scope the deploy needs, such as `contents: read` for checkout and `id-token: write` for OIDC.

### Deploy Types

Deploys are classified by their configuration:

| Type | Configuration | When It Runs |
|------|--------------|--------------|
| **Trigger-based** | Has `triggers` | When matching files change |
| **Build-linked** | Has `depends_on` referencing a build | When the referenced build runs |
| **Unconstrained** | No `triggers` or `depends_on` | Always runs |

Build-linked deploys inherit the build's triggers for change detection during promotions.

### publish Section

The publish callback runs once per build when a release is published, at the point where an RC version becomes a final semver. Use it to retag artifacts that still carry their RC version.

```yaml
ci:
  config:
    publish:
      workflow: .github/workflows/publish.yaml
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `workflow` | string | Yes | Path to publish workflow (reusable, `workflow_call` trigger) |

The callback is invoked once per configured build and receives:

| Input | Type | Description |
|-------|------|-------------|
| `build_name` | string | Which build's artifacts to retag (e.g., `app`) |
| `old_version` | string | RC version currently in the registry (e.g., `v1.0.0-rc.2`) |
| `new_version` | string | Final semver to apply (e.g., `v1.0.0`) |
| `sha` | string | Git commit SHA |
| `artifact_id` | string | Immutable digest from the build's `artifact_id` output (if declared) |

The framework only carries metadata. The publish workflow performs the registry operation.

### external Section (Multi-Repo Orchestration)

For repositories that coordinate deployments owned by satellite repos.

`external:` is designed for the **satellite/sibling-repo artifact coordination** pattern: a separate repo (the satellite) owns its own build and deploys to its first environment, then notifies the primary via `workflow_dispatch`. The primary records the satellite's SHA and version in the shared manifest and includes the satellite's deploys in every subsequent promotion. `external:` is **not** a GitOps mirror mechanism - it does not push rendered manifests to a target repo or track a pushed commit in a foreign repo. For GitOps-style deploys where the primary generates and pushes manifests to a dedicated config repo, use a callback workflow instead (see [Deploy Workflow Contract](./callback-contract#deploy-workflow-contract)).

```yaml
ci:
  config:
    external:
      - repo: org/cdk-infra
        ref: main
        deploys:
          - name: cdk
            workflow: .github/workflows/deploy-cdk.yaml
            triggers: [cdk/**]
      - repo: org/k8s-manifests
        deploys:
          - name: k8s
            workflow: org/k8s-manifests/.github/workflows/deploy.yaml@v1
            on_update:
              deploy:
                workflow: org/k8s-manifests/.github/workflows/deploy.yaml@v1
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repo` | string | Yes | External repository (e.g., `org/cdk-infra`) |
| `ref` | string | No | Branch/tag reference (default: trunk_branch) |
| `deploys` | list | Yes | Deployables from this repo |
| `deploys[].name` | string | Yes | Unique deploy identifier |
| `deploys[].workflow` | string | Yes | Workflow path (local or external) |
| `deploys[].triggers` | list | No | File patterns for change detection |
| `deploys[].on_update.deploy.workflow` | string | No | Reusable workflow to run as a scoped deploy when this slot is recorded |

**Workflow paths:**
- Local (`.github/workflows/deploy.yaml`) calls a workflow in the primary repo
- External (`org/repo/.github/workflows/deploy.yaml@ref`) calls a workflow in the external repo

When external deploys are configured, the generated promote workflow includes deploy jobs for each external deploy and the finalize job tracks their state.

#### Deploy on update (opt-in)

By default the receiver is record-only: when a satellite reports a new version, the primary records the new external state and stops. Setting `on_update.deploy.workflow` on an external deploy opts that component in to a scoped deploy that runs synchronously in the same receiver run, right after the slot is recorded.

```yaml
ci:
  config:
    external:
      - repo: org/cdk-infra
        ref: main
        deploys:
          - name: cdk
            workflow: org/cdk-infra/.github/workflows/deploy.yaml
            on_update:
              deploy:
                workflow: org/cdk-infra/.github/workflows/deploy.yaml
```

Behavior:

- **Opt-in and additive.** Omit `on_update` and the receiver stays record-only, byte-for-byte identical to before. No deploy job is generated.
- **Scoped to the updated component.** The generated receiver emits one `deploy_<name>` job per opted-in component, each gated on `inputs.deploy_name` so a single receiver run deploys only the component that was just recorded. Other components are untouched.
- **Synchronous and gated on the record.** The deploy job runs in the same receiver run and only after the record step succeeds. A failed record never triggers a deploy.
- **Reusable-workflow only.** Like `deploys[].workflow`, `on_update.deploy` accepts a workflow path (local `.github/workflows/x.yaml` or `org/repo/.github/...@ref`); inline `run:` and `shell:` are not supported. The scoped deploy receives the recorded `environment`, `sha`, `version`, and `deploy_name` as inputs and inherits secrets.

### notify Section (Satellite Repos)

For satellite repositories that report deployments back to a primary repo:

```yaml
ci:
  config:
    notify:
      repo: org/my-backend
      workflow: external-update.yaml
      token: PRIMARY_REPO_TOKEN
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repo` | string | Yes | Primary repository to notify |
| `workflow` | string | No | Workflow name (default: `external-update.yaml`) |
| `token` | string | No | Secret name for cross-repo dispatch (default: `PRIMARY_REPO_TOKEN`) |

When configured, the orchestrate workflow's finalize job dispatches to the primary repo after deploying to the first environment.

**Important:** A repository cannot be both primary (has `external`) and satellite (has `notify`).

### release Section

```yaml
ci:
  config:
    release:
      disabled: false
      tag: goreleaser.tag
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `disabled` | bool | false | Disable framework release management |
| `tag` | string | - | callback.output reference for an external release tool |

Omit this section to use framework defaults (creates releases with conventional commit changelogs).

### changelog Section

```yaml
ci:
  config:
    changelog:
      disabled: false
      workflow: .github/workflows/custom-changelog.yaml
      contributors: true
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `disabled` | bool | false | Disable changelog generation entirely |
| `workflow` | string | - | Path to a custom changelog workflow |
| `contributors` | bool | false | Include contributor attribution via the GitHub API |

Omit this section to use the built-in conventional commit parser.

## State Section

The `state` section tracks deployment state per environment plus a synthetic `release` slot. The framework manages it automatically. Do not hand-edit.

### Structure

```yaml
ci:
  state:
    dev:
      sha: "abc123def456"
      version: "v1.2.0-rc.3"
      committed_at: "2026-01-15T10:30:00Z"
      committed_by: "github-actions[bot]"
      builds:
        app:
          sha: "abc123def456"
          built_at: "2026-01-15T10:25:00Z"
          built_by: "github-actions[bot]"
          artifact_id: "sha256:def456..."
          tags:
            image_tag: "abc123-1736923500"
      deploys:
        infra:
          sha: "abc123def456"
          deployed_at: "2026-01-15T10:30:00Z"
          deployed_by: "github-actions[bot]"

    test:
      sha: "abc123def456"
      version: "v1.2.0-rc.3"
      committed_at: "2026-01-15T14:00:00Z"

    prod:
      sha: "def789abc012"
      version: "v1.1.0"

    release:
      sha: "def789abc012"
      version: "v1.1.0"
      committed_at: "2026-01-14T09:00:00Z"

  latest_release:
    version: "v1.1.0"
    sha: "def789abc012"
    released_on: "2026-01-14T09:00:00Z"
    released_by: "octocat"
```

### Environment-Level Fields

| Field | Description |
|-------|-------------|
| `sha` | Commit SHA promoted into this environment |
| `version` | Semantic version tag (e.g., `v1.2.3-rc.0`) |
| `committed_at` | ISO 8601 timestamp when code was committed/promoted |
| `committed_by` | GitHub actor who triggered the commit/promotion |
| `builds` | Per-build tracking (auto-populated) |
| `deploys` | Per-deployable tracking (auto-populated) |
| `external` | Per-external-deploy tracking (primary repos only) |

### The `release` Slot

The implicit `release` env tracks the most recently published (non-draft) GitHub release. Promotions to prod first cross the `release` boundary, where the breaking-change gate runs and the publish callback fires.

### Per-Build Tracking

```yaml
builds:
  app:
    sha: "abc123"
    built_at: "2026-01-15T10:25:00Z"
    built_by: "github-actions[bot]"
    artifact_id: "sha256:def456..."
    tags:
      image_tag: "abc123-1736923500"
      version: "1.2.3"
```

| Field | Description |
|-------|-------------|
| `sha` | Commit SHA that was built |
| `built_at` | ISO 8601 timestamp |
| `built_by` | GitHub actor who triggered the build |
| `artifact_id` | Immutable artifact identifier captured from the build's `artifact_id` output |
| `tags` | Additional declared workflow outputs |

`artifact_id` is the canonical identifier passed to the publish callback. Tags are populated from the build's other declared outputs.

### Per-Deployable Tracking

```yaml
deploys:
  infra:
    sha: "abc123"
    deployed_at: "2026-01-15T10:30:00Z"
    deployed_by: "github-actions[bot]"
    tags:
      stack_version: "v2.1.0"
```

This enables diff-based change detection during promotions. Only deployables with actual file changes are redeployed.

### External Deploy Tracking

For primary repos coordinating satellites:

```yaml
ci:
  state:
    dev:
      sha: "abc123def456"
      external:
        cdk:
          repo: "org/cdk-infra"
          sha: "cdk789xyz"
          version: "v1.2.0"
          deployed_at: "2026-01-15T10:30:00Z"
          deployed_by: "github-actions[bot]"
          artifacts:
            image_tag: "cdk-abc123"
```

External state is updated when:
1. A satellite repo dispatches to the primary's `external-update` workflow
2. The promote workflow promotes external deploys to higher environments

## Policy Fields

### run_policy

Controls when a callback executes:

| Value | Behavior |
|-------|----------|
| `default` | Skip if any dependency was skipped |
| `always` | Run if triggered, even if dependencies skipped |
| `force` | Always run, ignore triggers and dependencies |

### on_failure

| Value | Behavior |
|-------|----------|
| `abort` | Fail the entire workflow |
| `continue` | Let other callbacks proceed |

### retries

Number of retry attempts if the callback fails (0-3).

## Trigger Patterns

Triggers use glob patterns:

| Pattern | Matches |
|---------|---------|
| `src/**` | All files under src/ recursively |
| `*.go` | Go files in the root directory |
| `**/*.yaml` | YAML files anywhere in the repo |
| `Dockerfile` | Exact file match |
| `cdk/*.ts` | TypeScript files directly in cdk/ (not recursive) |
| `deploy/k8s/**` | All files under deploy/k8s/ |

Special characters:
- `*` matches any characters except `/`
- `**` matches any path segments
- `?` matches a single character

## Input Inheritance

Inputs flow from static to environment-specific:

```yaml
deploys:
  - name: services
    inputs:
      cluster: default-cluster
      region: us-east-1
    env_inputs:
      dev:
        cluster: dev-cluster
      prod:
        region: us-west-2
```

For `dev`: `{ cluster: "dev-cluster", region: "us-east-1" }`
For `prod`: `{ cluster: "default-cluster", region: "us-west-2" }`

## Complete Example

```yaml
# .github/manifest.yaml
ci:
  config:
    trunk_branch: master
    environments: [dev, test, prod]
    cli_version: v2.0.4

    validate:
      workflow: .github/workflows/validate.yaml
      inputs:
        run_tests: true
      run_policy: default
      on_failure: abort

    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers: [src/**, Dockerfile, go.mod, go.sum]
        inputs:
          dockerfile: ./Dockerfile
        env_inputs:
          prod:
            sign_image: true
        retries: 1

      - name: wiremock
        workflow: .github/workflows/build-wiremock.yaml
        triggers: [wiremock/**, wiremock.Dockerfile]

    deploys:
      - name: cdk
        workflow: .github/workflows/deploy-cdk.yaml
        triggers: [cdk/**, cdk.json]
        supports_dry_run: true
        on_failure: abort

      - name: services
        workflow: .github/workflows/deploy-services.yaml
        triggers: [src/**, deploy/**]
        depends_on: [cdk]
        inputs:
          cluster: default
        env_inputs:
          dev:
            cluster: dev-cluster
          test:
            cluster: test-cluster
          prod:
            cluster: prod-cluster
        retries: 2

    publish:
      workflow: .github/workflows/publish.yaml

    changelog:
      contributors: true
```
