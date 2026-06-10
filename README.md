# cascade

Declarative trunk-based CI/CD for GitHub Actions. Generate your pipelines, environment cascade, and release lifecycle from a single manifest.

> **Status: active development.** cascade is functional and self-hosted (it uses itself to ship itself), but the manifest schema is not yet frozen. Config may change between minor versions before v1.0.0. See [Project status](#project-status).

You define *what* to build and *where* to deploy in one manifest file. cascade generates the GitHub Actions wiring, tracks deployment state, manages releases, and cascades promotions through your environments.

Licensed under Apache 2.0.

---

## How it works

```
Merge to trunk
      │
      ▼
┌─────────────────────────────────────────────────────────────┐
│  Orchestrate workflow (generated)                           │
│  Setup → Validate → Build(s) → Deploy(s) → Finalize         │
│                                                             │
│  • Change detection: only run what changed                  │
│  • Version computation: next semver RC from commits         │
│  • State written to manifest.yaml on every run              │
└─────────────────────────────────────────────────────────────┘
      │
      ▼  state[dev] updated, draft release created
      │
      │  workflow_dispatch (promote)
      ▼
┌─────────────────────────────────────────────────────────────┐
│  Promote workflow (generated)                               │
│  Preflight → Deploy(s) → Publish callback → Finalize        │
│                                                             │
│  • Same artifacts, never rebuilt on promote                 │
│  • Breaking-change gate at prerelease → release boundary    │
│  • Per-deploy change detection: skip unchanged              │
│  • Release published, RC tags cleaned up                    │
└─────────────────────────────────────────────────────────────┘
```

The **manifest** (`.github/manifest.yaml`) is the single source of truth. It holds both the pipeline configuration and the deployment state for every environment.

---

## Quick start

### 1. Install the CLI

```bash
go install github.com/stablekernel/cascade/cmd/cascade@latest
# or pin a specific version:
go install github.com/stablekernel/cascade/cmd/cascade@v0.1.0
```

### 2. Create the manifest

```yaml
# .github/manifest.yaml
ci:
  config:
    trunk_branch: main
    cli_version: v0.1.0

    environments: [dev, test, uat, prod]

    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers: [src/**, go.mod]

    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
        triggers: [cdk/**]
      - name: app
        workflow: .github/workflows/deploy-app.yaml
        depends_on: [app]   # only runs when build-app succeeds

    changelog:
      contributors: true
```

### 3. Generate the workflows

```bash
cascade generate-workflow --config .github/manifest.yaml
# Creates: .github/workflows/orchestrate.yaml
#          .github/workflows/promote.yaml
```

### 4. Write your callbacks

cascade calls your workflows via `workflow_call` and passes standard inputs. You own the build and deploy logic.

```yaml
# .github/workflows/build-app.yaml
on:
  workflow_call:
    inputs:
      environment:
        type: string
        required: true
      sha:
        type: string
        required: true
    outputs:
      artifact_id:
        description: 'Immutable artifact identifier (e.g., Docker image digest)'
        value: ${{ jobs.build.outputs.artifact_id }}
```

---

## Configuration reference

### Top-level structure

```yaml
ci:
  config:           # Pipeline definition (you write this)
    ...
  state:            # Deployment state (managed by cascade, do not edit)
    ...
  latest_release:   # Last published release (managed by cascade)
    ...
```

### `config` fields

| Field | Type | Description |
|---|---|---|
| `trunk_branch` | string | Branch that triggers orchestration |
| `environments` | list | Promotion chain. Omit for no-env library/CLI projects. |
| `cli_version` | string | CLI version workflows download (`latest` or `vX.Y.Z`) |
| `triggers` | list | Global path patterns that activate orchestration |
| `validate` | object | Optional validation callback run before builds |
| `builds` | list | Build callbacks |
| `deploys` | list | Deploy callbacks |
| `publish` | object | Optional publish callback invoked when a release is published |
| `changelog` | object | Changelog settings |
| `release_token` | string | Secret expression for release API calls. Default: `${{ secrets.GITHUB_TOKEN }}` |
| `git` | object | Git identity for state commits |

### Builds

```yaml
builds:
  - name: app
    workflow: .github/workflows/build-app.yaml
    triggers: [src/**, go.mod]
    depends_on: []          # Other build names this depends on
    run_policy: default     # default | always | force
    on_failure: abort       # abort | continue
    retries: 0              # 0–3
    inputs:
      dockerfile: ./Dockerfile
    env_inputs:
      prod:
        sign_image: true
```

### Deploys

```yaml
deploys:
  - name: infra
    workflow: .github/workflows/deploy-infra.yaml
    triggers: [cdk/**]
    depends_on: [app]       # Waits for build-app to succeed
    supports_dry_run: true
    on_failure: abort
    retries: 2
```

### Publish callback

When a release is published (RC to final semver), artifact registries still hold RC-tagged versions. The publish callback lets you retag them.

```yaml
publish:
  workflow: .github/workflows/publish.yaml
```

The publish workflow receives these inputs per configured build:

| Input | Example | Description |
|---|---|---|
| `build_name` | `app` | Which build's artifacts to retag |
| `old_version` | `v1.0.0-rc.2` | RC version currently in the registry |
| `new_version` | `v1.0.0` | Final semver to apply |
| `sha` | `abc123` | Git commit SHA |
| `artifact_id` | `sha256:def456` | Immutable digest (from build job output, if declared) |

cascade is a metadata courier. You construct the registry operations yourself.

### No-environment mode (library/CLI projects)

Omit `environments` for projects that publish releases without environment deployments:

```yaml
ci:
  config:
    trunk_branch: main
    cli_version: v0.1.0
    builds:
      - name: cli
        workflow: .github/workflows/build-cli.yaml
        triggers: [cmd/**, internal/**, go.mod]
    changelog:
      contributors: true
```

Commits create RC pre-releases. A `promote` dispatch (default mode) publishes the final release.

---

## Promotion

Promotions are triggered via `workflow_dispatch` on the generated `promote.yaml`.

| Mode | Behavior |
|---|---|
| `default` | Advance the chain by one logical step |
| `dev-to-test` | Promote dev to test |
| `dev-to-uat` | Cascade: dev → test → uat (all intermediates updated atomically) |
| `dev-to-prod` | Full cascade through all environments |
| `uat-to-prod` | Partial cascade from uat onward |

A **breaking-change gate** blocks the prerelease-to-release boundary when `feat!:` or `BREAKING CHANGE:` commits are found in the range. Pass `allow_breaking_changes: true` to proceed.

---

## State

The manifest tracks deployment state automatically. Don't edit the `state:` section. The workflows own it.

```yaml
ci:
  state:
    dev:
      sha: abc123
      version: v1.2.0-rc.3
      committed_at: "2025-01-15T10:30:00Z"
      committed_by: github-actions[bot]
      builds:
        app:
          sha: abc123
          artifact_id: sha256:def456
          built_at: "2025-01-15T10:30:00Z"
      deploys:
        infra:
          sha: abc123
          deployed_at: "2025-01-15T10:31:00Z"
    release:
      sha: abc000
      version: v1.1.0
  latest_release:
    version: v1.1.0
    sha: abc000
```

---

## CLI commands

| Command | Description |
|---|---|
| `generate-workflow` | Generate `orchestrate.yaml` and `promote.yaml` |
| `orchestrate setup` | Detect changes, compute version, plan execution |
| `orchestrate finalize` | Update state, manage release, commit manifest |
| `promote preflight` | Validate, compute promotions, check breaking changes |
| `promote finalize` | Update state after promotion deploys complete |
| `generate-changelog` | Create changelog from conventional commits |
| `manage-release` | Create / update / publish GitHub releases |
| `next-version` | Calculate next semantic version |
| `detect-changes` | Determine which builds/deploys a file change triggers |
| `parse-config` | Validate and print parsed manifest |
| `reset` | Wipe releases and state (for testing / fresh start) |

---

## Documentation

| Document | Description |
|---|---|
| [Getting Started](docs/getting-started.md) | Step-by-step setup guide |
| [Configuration](docs/configuration.md) | Full manifest reference |
| [Workflows](docs/workflows.md) | Orchestrate and Promote explained |
| [CLI Reference](docs/cli-reference.md) | All commands and flags |
| [Callback Contract](docs/callback-contract.md) | How to write build/deploy/publish workflows |
| [Architecture](docs/architecture.md) | System design and internals |

---

## Development

```bash
# Build
go build -o cascade ./cmd/cascade

# Test (all packages)
go test ./...

# E2E tests (requires Docker)
cd e2e && go test -v -timeout 20m ./...

# Lint
golangci-lint run ./...

# Regenerate cascade's own workflows
go run ./cmd/cascade generate-workflow --config .github/manifest.yaml -f
```

---

## Contributing

Contributions are welcome. cascade uses the [Developer Certificate of Origin](https://developercertificate.org/): sign off your commits with `git commit -s`.

---

## Project status

cascade is in active development. It is functional and self-hosted (the releases page shows the pipeline working end to end), but a few things are still settling before v1.0.0:

- **Schema stability:** the manifest schema is not yet frozen. Config may change between minor versions until the v1.0.0 contract.
- **GHA feature coverage:** some native GitHub Actions capabilities (runner selection, concurrency, environment gates, OIDC, matrix builds) are not yet modeled in the manifest. They are on the path to v1.0.0.
- **Documentation:** some docs lag the current implementation.

Open work is tracked in [GitHub Issues](https://github.com/stablekernel/cascade/issues).
