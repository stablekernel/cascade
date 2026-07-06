---
title: Manifest reference
description: Every field in the cascade manifest, grouped by lifecycle, with each field's emission status stated.
---

The manifest is the single input cascade compiles into GitHub Actions workflows. This page documents every field, grouped by the order you meet them, from the fields nearly everyone sets down to the reserved and validated-only tails almost nobody touches.

Each field carries an emission status:

| Status | Meaning |
|--------|---------|
| **emitted** | Changes generated workflow output. |
| **validated-only** | Parsed and schema-checked, but never appears in generated YAML. |
| **reserved** | Parsed, but has no generator consumption today. |

## File shape and schema support

A manifest holds both pipeline configuration and managed deployment state under a top-level wrapper key (`ci:` by default):

```yaml
ci:
  config:           # Pipeline definition (you write this)
    schema_version: 1
    trunk_branch: main
    environments: [dev, test, prod]
    # builds, deploys, and the rest below

  state:            # Deployment tracking (managed by cascade; do not edit)
    dev:
      sha: "abc123"
      version: "v1.2.0-rc.3"

  latest_release:   # Most recent published release (managed)
    version: "v1.1.0"
    sha: "abc000"
```

The wrapper key is set by `manifest_key` and the file path by `manifest_file`.

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `schema_version` | emitted | int | `1` when omitted | Manifest schema generation. Omitting it emits a CLI warning; set `schema_version: 1` in every manifest. |
| `manifest_file` | emitted | string | `.github/manifest.yaml` | Path to the manifest file. |
| `manifest_key` | emitted | string | `ci` | Top-level wrapper key inside the file. |
| `action_folder` | emitted | string | `manage-release` | Folder name for the generated manage-release action. |

Every field below lives under `ci.config` unless stated otherwise. The `ci.state` block is covered in [State section](#state-section-managed).

### Editor support

cascade ships a hand-authored JSON Schema. Registering it gives autocomplete, type checking, enum hints, and hover docs while you author the manifest. The schema covers structure, types, and enums; `cascade parse-config` remains the authority for semantic and cross-field rules.

The schema is published at:

```
https://stablekernel.github.io/cascade/manifest.schema.json
```

Print the embedded copy with `cascade schema` (write it to a file with `cascade schema --output manifest.schema.json`).

Add this directive to the top of the manifest so the YAML language server (VS Code, Neovim, and others) picks it up automatically:

```yaml
# yaml-language-server: $schema=https://stablekernel.github.io/cascade/manifest.schema.json
ci:
  config:
    schema_version: 1
    trunk_branch: main
```

Or map the schema to your manifest path in VS Code `settings.json`:

```json
{
  "yaml.schemas": {
    "https://stablekernel.github.io/cascade/manifest.schema.json": ".github/manifest.yaml"
  }
}
```

## Top-level identity

The two fields that define the pipeline shape.

| Field | Status | Type | Required | Default | Description |
|-------|--------|------|----------|---------|-------------|
| `trunk_branch` | emitted | string | Yes | `main` | The trunk branch releases flow from. |
| `environments` | emitted | list | No | - | The promotion chain. Omit for a no-environment library or CLI project. |

```yaml
ci:
  config:
    schema_version: 1
    trunk_branch: main
    environments: [dev, test, prod]
    cli_version: v0.9.1
```

:::note[Environment names are yours; roles are positional]
The `environments` list is fully configurable. cascade attaches no meaning to specific labels: `dev`, `test`, `staging`, and `prod` are illustrative, not reserved. Roles are decided by position, not by name. The last environment is the release stage, the second-to-last is the prerelease environment, and the publish boundary is the final crossing into the last environment. The count is structural too: zero environments is release-only, one environment generates a single-environment Release workflow, and two or more enable the full promote cascade.

**Naming.** Environment, build, and deploy names become GitHub Actions job IDs and output keys, so keep them identifier-safe: letters, digits, and underscores (hyphens read as subtraction in GitHub Actions expressions). The generator-owned names `environment` and `dry_run` cannot be used as `dispatch_inputs`.
:::

### Trigger configuration

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `triggers` | emitted | list | - | Global path patterns that activate orchestration. See [Trigger patterns](#trigger-patterns). |
| `release_trigger` | emitted | string | `push` | How orchestrate fires. `push` keeps push-on-trunk plus `workflow_dispatch`; `dispatch` drops `push:` so releases run only on manual `workflow_dispatch`. |
| `tag_prefix` | emitted | string | `v` | Version tag prefix. |

Workflow-level trigger types beyond `push` are set under [`extra_triggers`](#extra_triggers).

## CLI pinning

These fields pin the cascade CLI and third-party actions the generated workflows install.

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `cli_version` | emitted | string | latest | cascade CLI version the generated workflows install via setup-cli. Use `latest`, a prerelease channel, or a specific `vX.Y.Z`. |
| `cli_version_sha` | emitted | string | - | 40-hex commit SHA that `cli_version` resolves to. Under `pin_mode: sha`, the generated setup-cli ref pins to this commit. |
| `pin_mode` | emitted | string | `tag` | Third-party action pin policy: `tag` emits `<action>@<major-tag>`; `sha` emits `<action>@<commit-sha>` with the version as a trailing comment. |
| `action_pins` | emitted | map | - | Per-action ref overrides keyed by action path (for example `actions/checkout`), applied regardless of `pin_mode`. |

### cli_version values

| Value | Behavior |
|-------|----------|
| `latest` | Most recent stable release (default). |
| `beta` | Newest prerelease build. |
| `vX.Y.Z` | A specific version (for example `v0.9.1`). Pin for reproducibility. |

### cli_version_sha

Under `pin_mode: sha`, pair `cli_version` with `cli_version_sha`, the 40-character lowercase-hex commit the `cli_version` tag resolves to. The generated setup-cli ref then pins to that immutable commit, with `cli_version` carried as a trailing comment:

```yaml
uses: stablekernel/cascade/.github/actions/setup-cli@9dc69a1f66753a3865c38c34eca5a931f677c803 # v0.9.1
```

The `with: version:` input the action reads to select the release asset stays the human-readable tag, so only the action source is pinned to a commit. The field is optional and takes effect only under `pin_mode: sha`. Because cascade release tags are annotated, resolve the underlying commit (not the tag object) with `git ls-remote https://github.com/stablekernel/cascade 'refs/tags/<tag>^{}'`.

### Action pinning

Generated workflows are build output. cascade owns the third-party action pins inside them (for example `actions/checkout` and `actions/github-script`) and reconciles that ownership back to the manifest. The supported way to change a pinned action is `pin_mode` and `action_pins`, not a hand-edit of the generated YAML. A hand-edited pin is reported as drift by the next `cascade verify` and overwritten by the next regenerate.

`pin_mode` sets the reference style for every third-party action cascade emits:

| Value | Behavior |
|-------|----------|
| `tag` | Default. Emits `<action>@<major-tag>` (for example `actions/checkout@v4`). Never `@latest`. |
| `sha` | Emits `<action>@<commit-sha> # <version>`, pinning each action to an immutable commit with the human-readable version as a trailing comment. Under `sha`, pair `cli_version` with [`cli_version_sha`](#cli_version_sha) so the cascade self-action ref is pinned too. |

The `sha` values come from a single committed pin table (`internal/generate/action_pins.yaml`); no per-repo configuration is needed beyond setting `pin_mode: sha`.

`action_pins` overrides the built-in ref for individual actions, keyed by action path. The value is the bare ref emitted after `@` for that same action (a tag or a commit SHA); it cannot repoint an action to a different owner or repository. An override applies regardless of `pin_mode`:

```yaml
ci:
  config:
    pin_mode: sha
    action_pins:
      actions/checkout: 0123456789abcdef0123456789abcdef01234567
```

That emits `uses: actions/checkout@0123456789abcdef0123456789abcdef01234567`. An action neither in the built-in table nor overridden is emitted unchanged. `action_pins` is also the write target for [`cascade reconcile`](/cascade/reference/cli/#reconcile), which adopts an external governed-pin change (for example a Dependabot bump) into the manifest.

## Tokens and authentication

Two seams call GitHub on cascade's behalf: `release_token` for release API calls and the rc tag, and `state_token` for writing manifest state to trunk. Both default to `${{ secrets.GITHUB_TOKEN }}`, which is enough for a single-repo project whose trunk is unprotected.

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `release_token` | emitted | string | `state_token` if set, else `${{ secrets.GITHUB_TOKEN }}` | Token expression for release API calls and the rc tag. Inherits `state_token` when unset so the rc-to-release chain has a trigger-capable token. |
| `state_token` | emitted | string | `${{ secrets.GITHUB_TOKEN }}` | Token expression for writing manifest state to the trunk branch. |
| `release_token_app` | emitted | object | - | GitHub App identity that mints a release token at run time (`app_id`, `private_key`). |
| `state_token_app` | emitted | object | - | GitHub App identity that mints a state-write token at run time (`app_id`, `private_key`). |

Reference secrets by bare name; cascade wraps a bare name in a `${{ secrets.* }}` expression for you:

```yaml
ci:
  config:
    release_token: RELEASE_PAT
    state_token: STATE_PAT
```

:::caution[`release_token` must be trigger-capable]
The release token creates the rc tag, and that tag triggers the Release run, fleet validation, and promotion. GitHub suppresses workflow triggers for ref creations made with the default `GITHUB_TOKEN`, so an rc tag created with it fires nothing. An unset `release_token` inherits your `state_token` when one is set. Whatever resolves as the release token must be trigger-capable (a PAT or a GitHub App token). If your state token is supplied solely through `state_token_app`, set a static `release_token` explicitly, since a minted App token is a run-time step output the default cannot reach.
:::

A GitHub App avoids storing a long-lived PAT. Point `release_token_app` and `state_token_app` at App secrets; cascade mints a fresh, short-lived installation token per run via `actions/create-github-app-token`, guarded to real GitHub with `if: ${{ github.server_url == 'https://github.com' }}`:

```yaml
ci:
  config:
    release_token_app:
      app_id: CASCADE_APP_ID
      private_key: CASCADE_APP_PRIVATE_KEY
    state_token_app:
      app_id: CASCADE_APP_ID
      private_key: CASCADE_APP_PRIVATE_KEY
```

Add the App to the repository ruleset bypass list so it can write a protected trunk. Store only the private key as a secret. On act or gitea the minting step is skipped (the `github.server_url` guard does not match) and consumers fall back to the static `release_token` / `state_token`, so set both an App source and a static token if you run the same manifest locally and against real GitHub.

## git

Optional git identity and signing configuration for state commits.

```yaml
ci:
  config:
    git:
      mode: custom
      user_name: deploy-bot
      user_email: bot@example.com
      gpg_key_id: GPG_KEY_ID
      gpg_key_secret: GPG_PRIVATE_KEY
```

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `mode` | emitted | string | `default` | `default`, `custom`, or `external`. |
| `user_name` | emitted | string | github-actions[bot] | Git `user.name` (when `mode: custom`). |
| `user_email` | emitted | string | github-actions[bot]@users.noreply.github.com | Git `user.email`. |
| `gpg_key_id` | emitted | string | - | Secret name holding the GPG key ID. |
| `gpg_key_secret` | emitted | string | - | Secret name holding the GPG private key. |

`default` uses the `github-actions[bot]` identity, `custom` uses your supplied name and email, and `external` skips git config entirely (the runner is assumed pre-configured). When both `gpg_key_id` and `gpg_key_secret` are set, cascade imports the key, enables `commit.gpgsign`, and signs state commits.

## validate

Optional pre-build validation callback.

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

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `workflow` | emitted | string | - | Path to the validation workflow. |
| `supports_dry_run` | emitted | bool | false | Whether the callback handles the `dry_run` input. |
| `triggers` | emitted | list | - | File patterns that trigger validation. |
| `inputs` | emitted | map | {} | Static inputs passed to the workflow. |
| `env_inputs` | emitted | map | {} | Per-environment input overrides. |
| `run_policy` | emitted | string | `default` | Execution policy. See [Policy fields](#policy-fields). |
| `on_failure` | emitted | string | `abort` | Failure handling. See [Policy fields](#policy-fields). |
| `retries` | emitted | int | 0 | Retry attempts (0-3). |

## builds

Builds produce artifacts (container images, binaries, and the like). `builds` is a list.

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
        permissions:
          contents: read
          id-token: write
        matrix:
          dimensions:
            os: [linux, darwin]
            arch: [amd64, arm64]
          max_parallel: 4
          fail_fast: false
        state_tags: [image_tag]
        auto_commits: false
        run_policy: default
        on_failure: abort
        retries: 0
```

| Field | Status | Type | Required | Description |
|-------|--------|------|----------|-------------|
| `name` | emitted | string | Yes | Unique build identifier. |
| `workflow` | emitted | string | Yes | Path to the build workflow. |
| `triggers` | emitted | list | No | Glob patterns that trigger this build. |
| `depends_on` | emitted | list | No | Other callbacks to wait for (hard dependency). |
| `optional_depends_on` | emitted (ordering) | list | No | Soft dependency: orders this job after the named jobs when they exist, without failing generation if a name is absent. The counterpart to `depends_on`. |
| `inputs` | emitted | map | No | Static inputs to the workflow. |
| `env_inputs` | emitted | map | No | Per-environment input overrides. |
| `permissions` | emitted | map | No | Job-level `permissions:` for this callback's caller job. See [Permissions](#permissions). |
| `matrix` | emitted | object | No | Build fan-out. See [matrix](#matrix). |
| `state_tags` | emitted (behavior) | list | No | State-capture tags recorded for this build (the field behind the callback contract's State Capture). |
| `auto_commits` | emitted (behavior) | bool | No | When true, cascade captures the HEAD sha the callback advanced to (runtime env `AUTO_COMMITS_HEAD_SHA`), for callbacks that commit during their run. |
| `run_policy` | emitted | string | No | Execution policy. See [Policy fields](#policy-fields). |
| `on_failure` | emitted | string | No | Failure handling. See [Policy fields](#policy-fields). |
| `retries` | emitted | int | No | Retry attempts (0-3). |
| `runs_on` | validated-only | object | No | Parsed and validated, never emitted. See [Validated-only fields](#validated-only-fields). |
| `concurrency` | validated-only | object | No | Parsed and validated, never emitted. See [Validated-only fields](#validated-only-fields). |
| `timeout_minutes` | validated-only | int | No | Parsed and validated, never emitted. See [Validated-only fields](#validated-only-fields). |

The build's `artifact_id` output (if declared) is captured into state automatically. Other declared outputs are forwarded to dependent deploys as inputs.

### matrix

`matrix` (builds only) fans a build across a cross-product of dimensions.

| Sub-field | Status | Type | Description |
|-----------|--------|------|-------------|
| `dimensions` | emitted | map | The cross-product axes (for example `os: [linux, darwin]`, `arch: [amd64, arm64]`). |
| `max_parallel` | emitted | int | Caps concurrent matrix legs (0 uses the GitHub Actions default). |
| `fail_fast` | emitted | bool | Whether a failing leg cancels the rest. Unset applies the GitHub Actions default (true for matrix builds). |

### Permissions

A `permissions` map renders as a job-level `permissions:` block on the caller job that invokes the callback, scoping the `GITHUB_TOKEN` to least privilege for that one job. GitHub Actions treats a job-level block as the **complete** permission set: it replaces the workflow default rather than merging. Declare the full set the callback needs, including `contents: read` if it checks out code and `id-token: write` for OIDC. cascade emits exactly the scopes you declare and never injects an implicit one.

```yaml
permissions:
  contents: read
  id-token: write
```

This is the shipped OIDC answer: a per-callback `permissions:` block carrying `id-token: write` grants that one job an OIDC token without widening any other job.

## deploys

Deploys target environments. `deploys` is a list and shares most fields with `builds`.

```yaml
ci:
  config:
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
        triggers: [cdk/**]
        supports_dry_run: true
        depends_on: []
        permissions:
          contents: read
          id-token: write
        rollout:
          max_parallel: 2
          fail_fast: false
        run_policy: default
        on_failure: abort
        retries: 0
```

| Field | Status | Type | Required | Description |
|-------|--------|------|----------|-------------|
| `name` | emitted | string | Yes | Unique deploy identifier. |
| `workflow` | emitted | string | Yes | Path to the deploy workflow. |
| `triggers` | emitted | list | No | Glob patterns that trigger this deploy. |
| `depends_on` | emitted | list | No | Other callbacks to wait for. |
| `optional_depends_on` | emitted (ordering) | list | No | Soft dependency, as for builds. |
| `supports_dry_run` | emitted | bool | No | Whether the callback handles `dry_run`. |
| `inputs` | emitted | map | No | Static inputs. |
| `env_inputs` | emitted | map | No | Per-environment overrides. |
| `permissions` | emitted | map | No | Job-level `permissions:` for the caller job. See [Permissions](#permissions). |
| `rollout` | partial | object | No | Deploy rollout strategy. See [rollout](#rollout). |
| `state_tags` | emitted (behavior) | list | No | State-capture tags recorded for this deploy. |
| `auto_commits` | emitted (behavior) | bool | No | Captures the advanced HEAD sha, as for builds. |
| `run_policy` | emitted | string | No | Execution policy. |
| `on_failure` | emitted | string | No | Failure handling. |
| `retries` | emitted | int | No | Retry attempts (0-3). |
| `runs_on` | validated-only | object | No | Parsed and validated, never emitted. |
| `concurrency` | validated-only | object | No | Parsed and validated, never emitted. |
| `timeout_minutes` | validated-only | int | No | Parsed and validated, never emitted. |

### Deploy types

Deploys are classified by their configuration:

| Type | Configuration | When it runs |
|------|--------------|--------------|
| Trigger-based | Has `triggers` | When matching files change. |
| Build-linked | Has `depends_on` referencing a build | When the referenced build runs. |
| Unconstrained | No `triggers` or `depends_on` | Always runs. |

Build-linked deploys inherit the build's triggers for change detection during promotions.

### rollout

`rollout` (deploys only) tunes the deploy job's `strategy:` block. Its status is **partial**: two sub-fields are emitted, the rest are reserved.

| Sub-field | Status | Type | Description |
|-----------|--------|------|-------------|
| `max_parallel` | emitted | int | Caps concurrent rollout waves. Emitted into the deploy job's `strategy:` block. |
| `fail_fast` | emitted | bool | Whether a failing wave cancels the rest. Emitted into the `strategy:` block. Unset differs from an explicit false. |
| `type` | reserved | string | `default`, `rolling`, `canary`, or `blue_green`. Parsed, not yet wired to generation. |
| `canary` | reserved | object | Canary sub-block (`steps`, `analysis`, `percent`, `bake_time`, `promote_callback`, `rollback_callback`). Inert today. |
| `blue_green` | reserved | object | Blue/green sub-block. Inert today. |

See [Versioning and schema](/cascade/reference/versioning/) for the full reserved rollout shape.

## publish

The publish callback runs once per build when a release is published, at the point where an rc version becomes a final semver. Use it to retag artifacts that still carry their rc version.

```yaml
ci:
  config:
    publish:
      workflow: .github/workflows/publish.yaml
```

| Field | Status | Type | Required | Description |
|-------|--------|------|----------|-------------|
| `workflow` | emitted | string | Yes | Path to the publish workflow (reusable, `workflow_call` trigger). |

`publish` also accepts `permissions`, `rollout`, `state_tags`, and `auto_commits` with the same semantics as a deploy, and `runs_on` / `concurrency` / `timeout_minutes` as validated-only. The callback is invoked once per configured build and receives `build_name`, `old_version` (the rc version in the registry), `new_version` (the final semver), `sha`, and `artifact_id` (the immutable digest from the build's `artifact_id` output). cascade carries metadata only; the publish workflow performs the registry operation.

## external and notify

`external` (primary repos) and `notify` (satellite repos) coordinate deployments across repositories. A repository cannot set both.

### external

`external` is designed for satellite-repo artifact coordination: a satellite owns its own build, deploys to its first environment, then notifies the primary, which records the satellite's SHA and version in the shared manifest and includes its deploys in every subsequent promotion. It is not a GitOps mirror.

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
            on_update:
              deploy:
                workflow: org/cdk-infra/.github/workflows/deploy.yaml
```

| Field | Status | Type | Required | Description |
|-------|--------|------|----------|-------------|
| `repo` | emitted | string | Yes | External repository (for example `org/cdk-infra`). |
| `ref` | emitted | string | No | Branch or tag reference (default: `trunk_branch`). |
| `deploys` | emitted | list | Yes | Deployables from this repo. |
| `deploys[].name` | emitted | string | Yes | Unique deploy identifier. |
| `deploys[].workflow` | emitted | string | Yes | Workflow path (local, or `org/repo/.github/workflows/x.yaml@ref` for external). |
| `deploys[].triggers` | emitted | list | No | File patterns for change detection. |
| `deploys[].on_update.deploy.workflow` | emitted | string | No | Reusable workflow to run as a scoped deploy when this slot is recorded. |

By default the receiver is record-only. Setting `on_update.deploy.workflow` opts a component into a scoped deploy that runs synchronously in the same receiver run, right after the slot is recorded and only if the record step succeeds. Inline `run:` and `shell:` are not supported. See [Coordinate multiple repos](/cascade/guides/multi-repo/) for the operator recipe.

### notify

For satellite repos that report deployments back to a primary.

```yaml
ci:
  config:
    notify:
      repo: org/my-backend
      workflow: external-update.yaml
      token: PRIMARY_REPO_TOKEN
      deploy_name: artifact-a
      environment: staging
```

| Field | Status | Type | Required | Default | Description |
|-------|--------|------|----------|---------|-------------|
| `repo` | emitted | string | Yes | - | Primary repository to notify. |
| `workflow` | emitted | string | No | `external-update.yaml` | Workflow name. |
| `token` | emitted | string | No | `PRIMARY_REPO_TOKEN` | Secret name for cross-repo dispatch. |
| `deploy_name` | emitted | string | No | first local deploy, then first build | Deploy name to dispatch, when the primary recognizes this satellite under a different name. |
| `environment` | emitted | string | No | first local environment, then `dev` | Environment to dispatch, when the primary expects a different one. |

The primary validates the dispatched `deploy_name` and `environment` against its own config, so a satellite whose local names differ from what the primary expects must send the parent-recognized values.

## release and changelog

```yaml
ci:
  config:
    release:
      disabled: false
      workflow: .github/workflows/release-assets.yaml
    changelog:
      disabled: false
      contributors: true
```

### release

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `disabled` | emitted | bool | false | Disable cascade release management. |
| `tag` | emitted | string | - | `callback.output` reference for an external release tool. |
| `workflow` | emitted | string | - | Release workflow dispatched against a release tag to build and attach binaries. |
| `tag_only` | emitted | bool | false | Cut the version tag only, without pre-creating a draft release, and create the tag with the non-triggering `GITHUB_TOKEN`. For a self-publishing release workflow (for example GoReleaser) that is the sole creator of the release object. Requires `workflow` and `release_trigger: dispatch`. |
| `version_overrides` | reserved | object | - | Reserved pointer (`dir:`) to maintainer-committed version-intent override files. See [Versioning](/cascade/reference/versioning/). |

When `workflow` is set, cascade dispatches it (via `gh workflow run --ref <tag>`) rather than relying only on the tag-push trigger, which GitHub does not reliably start when the tagged commit carries a CI-skip marker. Omit the section to use cascade defaults.

Set `tag_only: true` when the release `workflow` publishes the release object itself, as GoReleaser does. Orchestrate then cuts the candidate tag with the default `GITHUB_TOKEN`, whose actions do not start a workflow run, and dispatches the release workflow explicitly. The tag push therefore does not also fire the release workflow, so it runs exactly once, and nothing pre-creates a draft the release workflow would duplicate into an orphan. It requires `release_trigger: dispatch` (the mode that emits the explicit dispatch) and a `workflow` (the dispatch target); validation rejects `tag_only` without both.

### changelog

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `disabled` | emitted | bool | false | Disable changelog generation entirely. |
| `workflow` | emitted | string | - | Path to a custom changelog workflow. |
| `contributors` | emitted | bool | false | Include contributor attribution via the GitHub API. |

Omit the section to use the built-in conventional commit parser.

## environment_config

Per-environment settings keyed by environment name. Consumed for native GitHub Environment support and deployment URLs.

```yaml
ci:
  config:
    environments: [production]
    environment_config:
      production:
        gha_environment: production
        environment_url: "https://app.example.com"
        required_reviewers: [octocat]
        wait_timer: 10
        branch_policy: protected
```

| Sub-field | Status | Description |
|-----------|--------|-------------|
| `gha_environment` | emitted | Maps the cascade environment to a real GitHub Environment (native deployments, `environment_url`). |
| `environment_url` | emitted | URL reported on the Deployment status for that environment. |
| `required_reviewers` | emitted (via `environments` command) | Reviewers the `environments` command applies to the GitHub Environment. |
| `wait_timer` | emitted (via `environments` command) | Wait timer the `environments` command applies. |
| `branch_policy` | emitted (via `environments` command) | Branch policy the `environments` command applies. |

GitHub Environment support is shipped: `gha_environment` is consumed for native deployments, and the `cascade environments` command emits `required_reviewers`, `wait_timer`, and `branch_policy` for an operator to apply. See [Add or change environments](/cascade/guides/environments/).

## Workflow-level fields

Fields that shape the cascade-owned workflows as a whole rather than a single callback.

### concurrency

Top-level concurrency block emitted onto the orchestrate, promote, hotfix, rollback, release, and external-update workflows.

```yaml
ci:
  config:
    concurrency:
      group: cascade-${{ github.ref }}
      cancel_in_progress: false
```

| Sub-field | Status | Type | Description |
|-----------|--------|------|-------------|
| `group` | emitted | string | The concurrency group expression. |
| `cancel_in_progress` | emitted | bool | Whether a new run cancels an in-progress run in the same group. |

### job_timeout_minutes

```yaml
ci:
  config:
    job_timeout_minutes: 30
```

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `job_timeout_minutes` | emitted | int | 30 | Sets `timeout-minutes` on cascade-owned jobs. |

### extra_triggers

Non-push trigger types wired onto the generated workflows.

```yaml
ci:
  config:
    extra_triggers:
      schedule:
        - cron: "0 7 * * *"
      repository_dispatch:
        types: [deploy-request]
      merge_group: {}
```

| Sub-field | Status | Description |
|-----------|--------|-------------|
| `schedule` | emitted | List of cron schedule entries. Each entry has one required key, `cron`. |
| `repository_dispatch` | emitted | Wires the `repository_dispatch` trigger; `types` lists the event types. |
| `workflow_run` | emitted | Wires the `workflow_run` trigger. |
| `merge_group` | emitted | Present (even empty) wires the merge-queue trigger. The validation lane behavior lives in the separate `merge_queue` block. |

### rollback

Opts the rollback workflow into a `repository_dispatch` trigger, driving the rollback-dispatch path.

```yaml
ci:
  config:
    rollback:
      repository_dispatch:
        types: [rollback-request]
```

| Sub-field | Status | Description |
|-----------|--------|-------------|
| `repository_dispatch` | emitted | Reuses the shared `RepositoryDispatchTrigger` shape (`types`), configured the same way as `extra_triggers.repository_dispatch`. |

See [Roll back an environment](/cascade/guides/rollback/) for the operator recipe.

## Companion workflows (opt-in)

Each of these emits an additional workflow only when its block is present. Omit the block and output is unchanged.

### pr_preview

```yaml
ci:
  config:
    pr_preview:
      enabled: true
      comment: true
```

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `enabled` | emitted | bool | false | Emit the PR-preview companion. |
| `comment` | emitted | bool | false | Also post or update a sticky preview comment. |

### drift_check

Emits a pull-request workflow that runs [`cascade verify`](/cascade/reference/cli/#verify) and fails the check when committed workflows fall out of sync with the manifest.

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `enabled` | emitted | bool | false | Emit `.github/workflows/cascade-drift-check.yaml` (read-only, `contents: read`). |
| `comment` | emitted | bool | false | Also emit the fork-safe comment companion (`cascade-drift-comment.yaml`). |

The check job triggers on `pull_request` and is read-only; the comment companion triggers on `workflow_run` in the base-repo context with a scoped `pull-requests: write` token and derives the target PR only from trusted run metadata. When you set `comment: true`, consider `pin_mode: sha` to remove the floating-tag exposure on the one write-scoped job.

### reconcile

Emits the fork-safe [`cascade reconcile`](/cascade/reference/cli/#reconcile) lane that adopts an external governed-pin change into `action_pins` and regenerates.

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `enabled` | emitted | bool | false | Emit the reconcile detector and companion workflows. |
| `source` | emitted | string | `dependabot` | The change-source adapter the companion recognizes. |
| `commit` | emitted | string | `append` | Adoption commit routing. `append` pushes onto the triggering PR branch; `followup` opens a separate PR (prefer this if you automerge on green). |

### deployments

Reports deployment status through the GitHub Deployments API from the finalize job.

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `enabled` | emitted | bool | false | Create a Deployment and report status. Adds `deployments: write` to top-level permissions only when enabled. |
| `keep_prior_active` | emitted | bool | false | Set `auto_inactive: false` so GitHub leaves prior deployments Active. |

Every Deployments API step carries an `if: ${{ github.server_url == 'https://github.com' }}` guard, so on act or gitea the steps are skipped. Pair with `environment_config.<env>.environment_url` so the status links to the running environment.

### validate_check

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `enabled` | emitted | bool | false | Emit `.github/workflows/cascade-validate.yaml`, a `pull_request` check that runs `cascade parse-config` and fails on an invalid manifest. |

The check validates cascade's own configuration only, requests `contents: read` alone, and has no dry-run or comment side effects.

### merge_queue

| Field | Status | Type | Default | Description |
|-------|--------|------|---------|-------------|
| `enabled` | emitted | bool | false | Emit `.github/workflows/cascade-merge-queue.yaml`, a `merge_group`-triggered lane that runs `cascade parse-config` and a dry-run `cascade orchestrate setup` against the merge-group candidate. |

The lane is read-only. This block owns the lane behavior; the raw `merge_group` trigger is expressible separately under `extra_triggers.merge_group`, and the two are intentionally distinct.

## Shared policy and pattern reference

### Policy fields

`run_policy`, `on_failure`, and `retries` apply to `validate`, each `builds` entry, and each `deploys` entry.

| `run_policy` | Behavior |
|--------------|----------|
| `default` | Skip if any dependency was skipped. |
| `always` | Run if triggered, even if dependencies skipped. |
| `force` | Always run, ignore triggers and dependencies. |

| `on_failure` | Behavior |
|--------------|----------|
| `abort` | Fail the entire workflow. |
| `continue` | Let other callbacks proceed. |

`retries` is the number of retry attempts on failure (0-3).

### Trigger patterns

Triggers use glob patterns:

| Pattern | Matches |
|---------|---------|
| `src/**` | All files under `src/` recursively. |
| `*.go` | Go files in the root directory. |
| `**/*.yaml` | YAML files anywhere in the repo. |
| `Dockerfile` | Exact file match. |
| `cdk/*.ts` | TypeScript files directly in `cdk/` (not recursive). |

`*` matches any characters except `/`, `**` matches any path segments, and `?` matches a single character.

### Input inheritance

Inputs flow from static `inputs` to per-environment `env_inputs`, with the environment-specific value winning:

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

For `dev`: `{ cluster: "dev-cluster", region: "us-east-1" }`. For `prod`: `{ cluster: "default-cluster", region: "us-west-2" }`.

## Validated-only fields

These fields parse and pass schema validation but **never appear in generated YAML**. They are kept in the schema so a manifest that sets them stays valid and forward-compatible, but they change nothing today.

| Field | Where | Why it is not emitted |
|-------|-------|-----------------------|
| `runs_on` | top-level and per-callback | GitHub Actions forbids `runs-on` on a reusable-workflow `uses:` caller job, so cascade-owned jobs are hardcoded `ubuntu-latest`. Per-environment runner overrides are blocked by the same structural limit. |
| `concurrency` (per-callback) | `builds[]`, `deploys[]`, `publish` | GitHub Actions forbids a `concurrency:` block on a reusable-workflow caller job. Top-level `concurrency` is emitted; the per-callback form is not. |
| `timeout_minutes` (per-callback) | `builds[]`, `deploys[]`, `publish` | The timeout belongs inside the called workflow, not the caller job. Use top-level `job_timeout_minutes` to bound the cascade-owned jobs. |

## Reserved fields

Reserved fields parse but have zero generator consumption today. They reserve a stable shape so a future capability can land without a schema break.

| Field | Where | Note |
|-------|-------|------|
| `telemetry` | top-level | `enabled`, `adapter`, plus reserved `webhook` and `job_summary`. No generator consumption. |
| `rollout.type` / `rollout.canary` / `rollout.blue_green` | `deploys[]` | Reserved rollout sub-blocks (`rollout.fail_fast` and `rollout.max_parallel` are emitted; the rest are inert). |
| `release.version_overrides` | `release` | Reserved pointer to version-intent override files. |
| `deploy_target` | `deploys[]` | Reserved shape for the GitOps mirror pattern. |

The full reserved-shapes catalog, including per-component versioning and the canary sub-fields (`steps`, `analysis`, `percent`, `bake_time`, `promote_callback`, `rollback_callback`), lives in [Versioning and schema](/cascade/reference/versioning/).

## State section (managed)

The `ci.state` block tracks deployment state per environment plus a synthetic `release` slot. cascade manages it automatically; do not hand-edit.

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
          artifact_id: "sha256:def456..."
          tags:
            image_tag: "abc123-1736923500"
      deploys:
        infra:
          sha: "abc123def456"
          deployed_at: "2026-01-15T10:30:00Z"
    release:
      sha: "def789abc012"
      version: "v1.1.0"
  latest_release:
    version: "v1.1.0"
    sha: "def789abc012"
```

| Environment-level field | Description |
|-------------------------|-------------|
| `sha` | Commit SHA promoted into this environment. |
| `version` | Semantic version tag (for example `v1.2.3-rc.0`). |
| `committed_at` / `committed_by` | ISO 8601 timestamp and actor for the promotion. |
| `builds` / `deploys` / `external` | Per-callback tracking (auto-populated). |

The implicit `release` slot tracks the most recently published (non-draft) GitHub release. Promotions to the last environment cross the `release` boundary first, where the breaking-change gate runs and the publish callback fires. Per-build state carries `artifact_id` (the canonical identifier passed to publish) and `tags` (the build's other declared outputs). Per-deploy state enables diff-based change detection, so only deployables with actual file changes are redeployed.

## Validation rules

`cascade parse-config` enforces the semantic rules the schema alone cannot:

- `schema_version` should be `1`. Omitting it emits a warning.
- Environment, build, and deploy names must be identifier-safe (letters, digits, underscores). The generator-owned names `environment` and `dry_run` are reserved and cannot be used as `dispatch_inputs`.
- `pin_mode` must be `tag` or `sha`; `run_policy` must be `default`, `always`, or `force`; `on_failure` must be `abort` or `continue`; `retries` must be 0-3.
- A repository cannot set both `external` (primary) and `notify` (satellite).
- A per-callback `permissions` block is the complete permission set for that caller job and replaces the workflow default rather than merging.
- `cli_version_sha` takes effect only under `pin_mode: sha`.

## What to read next

- **Prerequisite**: [Getting started](/cascade/start/getting-started/) walks you from install to a first pipeline.
- **Next**: [Callback contract](/cascade/reference/callbacks/) documents the inputs and outputs the workflows referenced by `validate`, `builds`, `deploys`, and `publish` must honor.
