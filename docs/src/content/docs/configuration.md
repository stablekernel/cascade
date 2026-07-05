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
| `cli_version_sha` | string | No | - | 40-hex commit SHA that `cli_version` resolves to. With `pin_mode: sha`, the generated setup-cli ref is pinned to this commit. See [cli_version_sha](#cli_version_sha). |
| `triggers` | list | No | - | Global path patterns that activate orchestration |
| `release_trigger` | string | No | `push` | How the orchestrate workflow fires. `push` keeps the push-on-trunk plus `workflow_dispatch` triggers; `dispatch` drops the `push:` trigger so releases run only on manual `workflow_dispatch`. See [Release trigger](#release-trigger). |
| `pin_mode` | string | No | `tag` | Third-party action pin policy. `tag` emits `<action>@<major-tag>`; `sha` emits `<action>@<commit-sha>` with the version as a trailing comment. See [Action pinning](#action-pinning). |
| `action_pins` | map | No | - | Per-action ref overrides keyed by action path (e.g. `actions/checkout`), applied regardless of `pin_mode`. See [Action pinning](#action-pinning). |
| `tag_prefix` | string | No | `v` | Version tag prefix |
| `release_token` | string | No | `state_token` if set, else `${{ secrets.GITHUB_TOKEN }}` | Token expression for release API calls and the rc tag; inherits `state_token` when unset so the rc-to-release chain has a trigger-capable token |
| `state_token` | string | No | `${{ secrets.GITHUB_TOKEN }}` | Token expression for writing manifest state to the trunk branch |
| `release_token_app` | object | No | - | GitHub App identity that mints a release token at run time; see [Token authentication](#token-authentication) |
| `state_token_app` | object | No | - | GitHub App identity that mints a state-write token at run time; see [Token authentication](#token-authentication) |
| `manifest_file` | string | No | `.github/manifest.yaml` | Path to manifest file |
| `manifest_key` | string | No | `ci` | Top-level key inside the manifest file |
| `action_folder` | string | No | `manage-release` | Folder name for the manage-release action |

:::note[Environment names are yours; roles are positional]
The `environments` list is fully configurable. cascade attaches no meaning to specific labels: `dev`, `test`, `uat`, `staging`, and `prod` are illustrative examples used throughout these docs, not reserved names. Roles are decided by position in the list, not by name. The last environment is the release stage (prod), the second-to-last is the prerelease environment, and the publish boundary is the final crossing into the last environment. The count is structural too: zero environments is release-only, one environment generates a single-environment Release workflow, and two or more enable the full promote cascade.

**Naming.** Environment, build, and deploy names become GitHub Actions job IDs and output-variable keys, so keep them identifier-safe: use letters, digits, and underscores (hyphens are read as subtraction in GitHub Actions expressions). The reserved generator-owned names `environment` and `dry_run` cannot be used as `dispatch_inputs`. A `dispatch_inputs` name is emitted as a `workflow_dispatch` input key and referenced as `${{ inputs.<name> }}`, so it is held to the same identifier-safe charset (letters, digits, hyphens, underscores); a choice input's `options` are emitted verbatim and must contain only letters, digits, dots, hyphens, and underscores (so version-like values such as `v1.2.3` are allowed, but spaces, colons, and `${{ }}` fragments are rejected). Any `gha_environment` value maps to a real GitHub Environment, so GitHub's own naming rules apply there.
:::

### cli_version

Controls which CLI version the generated workflows install via setup-cli:

| Value | Behavior |
|-------|----------|
| `latest` | Most recent stable release (default) |
| `beta` | Latest build from the `master` branch |
| `vX.Y.Z` | Specific version (e.g., `v2.0.4`) |

Pin to a specific version for reproducibility. Use `beta` for early access.

### cli_version_sha

When `pin_mode: sha` is set, pair `cli_version` with `cli_version_sha`, the 40-character lowercase-hex commit SHA that the `cli_version` tag resolves to. The generated setup-cli ref is then pinned to that immutable commit, with `cli_version` carried as a trailing comment:

```yaml
uses: stablekernel/cascade/.github/actions/setup-cli@9dc69a1f66753a3865c38c34eca5a931f677c803 # v0.1.0
```

The `with: version:` input the action reads to select the release asset stays the human-readable tag, so only the action source is pinned to a commit.

This closes the supply-chain gap where the cascade self-action was referenced by a mutable tag while third-party actions were already SHA-pinned. The field is optional and only takes effect under `pin_mode: sha`; leave it unset (or use the default `pin_mode: tag`) to keep the tag-based ref. Set `cli_version_sha` alongside `cli_version` whenever you bump the pinned version. Because cascade release tags are annotated, resolve the underlying commit (not the tag object) with `git ls-remote https://github.com/stablekernel/cascade 'refs/tags/<tag>^{}'`.

### Release trigger

`release_trigger` selects how the generated orchestrate workflow fires. It is opt-in; repos that leave it unset keep the push triggers.

| Value | Behavior |
|-------|----------|
| `push` | Default. Orchestrate fires on trunk pushes (filtered by `triggers:`) plus `workflow_dispatch`. |
| `dispatch` | Drops the `push:` trigger so orchestrate runs only on `workflow_dispatch`, letting a maintainer-owned gate decide when a release candidate is cut. |

```yaml
ci:
  config:
    release_trigger: dispatch
```

### Action pinning

cascade governs how the third-party actions it emits into your workflows (for example `actions/checkout` and `actions/github-script`) are referenced. Two fields control the policy.

`pin_mode` sets the reference style for every third-party action cascade emits:

| Value | Behavior |
|-------|----------|
| `tag` | Default. Emits `<action>@<major-tag>` (for example `actions/checkout@v4`). Never `@latest`. |
| `sha` | Emits `<action>@<commit-sha> # <version>`, pinning each action to an immutable commit with the human-readable version as a trailing comment. Under `sha`, pair `cli_version` with [`cli_version_sha`](#cli_version_sha) so the cascade self-action ref is pinned too. |

The default `sha` values come from a single committed pin table (`internal/generate/action_pins.yaml`); no per-repo configuration is needed to adopt SHA pinning beyond setting `pin_mode: sha`.

`action_pins` overrides the built-in ref for individual actions, keyed by action path. The map value is the bare ref emitted after `@` for that same action path (a tag or a commit SHA); it cannot repoint an action to a different owner or repository. An override is applied regardless of `pin_mode`, so use it to hold an action at a known-good commit or tag:

```yaml
ci:
  config:
    pin_mode: sha
    action_pins:
      actions/checkout: 0123456789abcdef0123456789abcdef01234567
```

That emits `uses: actions/checkout@0123456789abcdef0123456789abcdef01234567`. An action that is neither in the built-in table nor overridden is emitted unchanged.

### Token authentication

Two seams call GitHub on cascade's behalf: `release_token` for release API calls and `state_token` for writing manifest state back to the trunk branch. Both default to `${{ secrets.GITHUB_TOKEN }}`, which is enough for a single-repo project whose trunk is unprotected. When the default token cannot do the job, supply your own token through one of two paths: a static secret (PAT) or a GitHub App.

#### Static secret (PAT)

Set `release_token` or `state_token` to a custom secret expression when the default `GITHUB_TOKEN` falls short:

- **Pulling a private-source CLI.** Installing the cascade CLI from a private repository or registry needs a token with read access to that source.
- **Cross-repo dispatch.** Coordinating builds or deploys in other repositories requires a token scoped beyond the current repository.
- **Writing to a protected trunk.** `GITHUB_TOKEN` cannot bypass branch protection, so it cannot push manifest state to a protected trunk branch. A PAT (or a GitHub App token) can bypass protection and produces a verified, signed commit.

Reference your secrets by bare name. cascade wraps a bare name in a `${{ secrets.* }}` expression for you:

```yaml
ci:
  config:
    release_token: RELEASE_PAT
    state_token: STATE_PAT
```

:::caution[`release_token` defaults to `state_token`, and must be trigger-capable]
The release token creates the rc tag, and that tag is what triggers the Release run, fleet validation, and promotion. GitHub deliberately suppresses workflow triggers for ref creations made with the default `GITHUB_TOKEN`, so an rc tag created with `GITHUB_TOKEN` fires nothing and the rc-to-release chain dies silently. To avoid that, an unset `release_token` inherits your `state_token` when one is set, reusing the trigger-capable token you already configured for protected-trunk writes. Whatever resolves as the release token must be trigger-capable (a PAT or a GitHub App token) for the automatic chain to run. If your state token is supplied solely through `state_token_app` (no static `state_token`), set a static `release_token` explicitly, since a minted App token is a run-time step output that this default cannot reach.
:::

#### GitHub App

A GitHub App avoids storing a long-lived PAT. cascade mints a fresh installation token per run, scoped to the App's least-privilege permissions and short-lived by construction. Only the App private key is ever stored as a secret; no PAT lives in your secret store.

One-time operator setup:

1. Create a GitHub App in your organization (for example, under `my-org`).
2. Generate a private key for the App and download the key file.
3. Install the App on the repository (or repositories) cascade runs in.
4. Add the App to the repository ruleset bypass list so it can write the protected trunk branch.
5. Store the App ID and the private key as GitHub secrets, for example `CASCADE_APP_ID` and `CASCADE_APP_PRIVATE_KEY`. Store only the private key as a secret, never the raw key material in the manifest.

Then point the manifest at those secrets with `release_token_app` and `state_token_app`. Each takes an `app_id` and a `private_key`, both secret references (a bare secret name or a `secrets`/`vars` expression):

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

When an App source is set, the generated workflow mints a short-lived installation token at run time via the `actions/create-github-app-token` action, guarded to real GitHub with `if: ${{ github.server_url == 'https://github.com' }}`. The token consumers prefer the minted token.

:::note[Local act/gitea is unaffected]
On act or gitea the minting step is skipped (the `github.server_url` guard does not match), and the consumers fall back to the static `release_token` / `state_token`. Set both the App source and a static token if you run the same manifest locally and against real GitHub.
:::

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

`external:` is designed for the **satellite/sibling-repo artifact coordination** pattern: a separate repo (the satellite) owns its own build and deploys to its first environment, then notifies the primary via `workflow_dispatch`. The primary records the satellite's SHA and version in the shared manifest and includes the satellite's deploys in every subsequent promotion. `external:` is **not** a GitOps mirror mechanism. It does not push rendered manifests to a target repo or track a pushed commit in a foreign repo. The first-class (reserved) home for the GitOps mirror pattern is a deploy's `deploy_target:` block with `mode: gitops`, which reserves the shape for pushing a rendered field into a dedicated config repo and recording the pushed commit (see [Reserved shape: GitOps deploy target](./versioning#reserved-shape-gitops-deploy-target)).

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
      deploy_name: artifact-a
      environment: staging
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repo` | string | Yes | Primary repository to notify |
| `workflow` | string | No | Workflow name (default: `external-update.yaml`) |
| `token` | string | No | Secret name for cross-repo dispatch (default: `PRIMARY_REPO_TOKEN`) |
| `deploy_name` | string | No | Deploy name to dispatch. Set this when the primary recognizes this satellite under a name that differs from its local deploy/build name. Defaults to the first local deploy name, then the first build name. |
| `environment` | string | No | Environment to dispatch. Set this when the primary expects an environment that differs from the satellite's first local environment (for example a build-only satellite with no environments). Defaults to the first local environment, then `dev`. |

When configured, the orchestrate workflow's finalize job dispatches to the primary repo after deploying to the first environment.

Use `deploy_name` and `environment` when the satellite's local names do not match the external deploy the primary defines. The primary validates the dispatched `deploy_name` and `environment` against its own config, so a satellite whose local build name or environment differs from what the primary expects must send the parent-recognized values here.

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
| `workflow` | string | - | Release workflow dispatched against a release tag to build and attach binaries. See the dispatch note below. |
| `version_overrides` | object | - | Reserved pointer (`dir:`) to maintainer-committed version-intent override files. Reserved shape only; see [Versioning](/versioning/#reserved-shape-version-intent-overrides). |

Omit this section to use framework defaults (creates releases with conventional commit changelogs).

When `workflow` is set, cascade dispatches it (via `gh workflow run --ref <tag>`) rather than relying only on the tag-push trigger. GitHub does not reliably start a tag-push workflow when the tagged commit carries a CI-skip marker, and release tags routinely point at a state commit that does. The explicit dispatch fires in two places: the promote flow dispatches it against the final tag when a release publishes, and, when `release_trigger: dispatch` is set, the orchestrate finalize job dispatches it against the release-candidate tag as soon as the candidate is cut. Restricting the candidate dispatch to dispatch-mode trunks keeps it from racing the native tag-push trigger a push-mode trunk relies on, so a candidate is never built twice.

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

### Drift-check workflow (opt-in)

Set `drift_check.enabled: true` and `generate-workflow` emits a pull-request workflow that runs [`cascade verify`](/cli-reference/#verify) and fails the check whenever the committed workflows fall out of sync with the manifest. This wires the same protection cascade uses on its own repository into yours, without hand-rolling the job.

```yaml
ci:
  config:
    drift_check:
      enabled: true
      comment: true
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | false | Emit the pull-request drift-check workflow (`.github/workflows/cascade-drift-check.yaml`) |
| `comment` | bool | false | Also emit the fork-safe comment companion (`.github/workflows/cascade-drift-comment.yaml`) |

Behavior:

- **Opt-in and additive.** Omit `drift_check` and nothing is emitted; existing output is byte-for-byte identical to before.
- **Read-only on the pull request.** The `cascade-drift-check.yaml` job triggers on `pull_request` with `contents: read` only. A pull request from a fork gets a read-only token and no secrets, so the job cannot comment or write. It captures the verify result as a `cascade-drift-result` artifact instead, and re-exits non-zero on drift to keep the check red.
- **Fork-safe comment companion.** When `comment: true`, `cascade-drift-comment.yaml` triggers on `workflow_run` in the base-repo context, where it has a scoped `pull-requests: write` token. It downloads the artifact (data only), then posts or updates a sticky comment with the verify output. It never checks out or executes pull-request head code.
- **Trusted PR resolution.** The companion derives the target pull-request number only from trusted `workflow_run` run metadata (the source run's `pull_requests` array, or a head-SHA lookup for fork pull requests), never from the artifact the pull-request job uploads. A fork therefore cannot redirect the comment at another pull request.
- **cascade-owned.** Both files carry the cascade-generated marker, so `cascade verify` itself tracks them: edit them by hand and they are reported as drift; remove the toggle and they are reported as orphans.

> **Pin recommendation.** When you enable `comment: true`, consider setting `pin_mode: sha`. The comment companion runs `actions/github-script` in a write-scoped `workflow_run` job, and the product default `pin_mode: tag` references that action by a floating major tag. Pinning to a full commit SHA removes the floating-tag exposure on the one job that holds a `pull-requests: write` token.

### Native deployments (opt-in)

Set `deployments.enabled: true` and the finalize job reports deployment status through the [GitHub Deployments API](https://docs.github.com/en/rest/deployments/deployments). It creates a Deployment for the environment selected at run time, marks it `in_progress`, then reports a terminal `success` or `failure` status once the deploy callbacks finish. Pair it with a per-environment `environment_url` so the Deployment status links straight to the running environment.

```yaml
ci:
  config:
    environments: [production]
    deployments:
      enabled: true
      keep_prior_active: false
    environment_config:
      production:
        environment_url: "https://app.example.com"
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `deployments.enabled` | bool | false | Create a Deployment and report status from the finalize job |
| `deployments.keep_prior_active` | bool | false | Set `auto_inactive: false` so GitHub leaves prior deployments for the same environment Active. Default relies on GitHub's native auto-inactivation |
| `environment_config.<env>.environment_url` | string | "" | URL reported on the Deployment status for that environment |

Behavior:

- **Status transition model.** The finalize job runs after every deploy callback, so it owns the full lifecycle: create the Deployment, set `in_progress`, then set `success` or `failure` based on whether every deploy callback succeeded. The terminal status step runs under `always()` so a failed deploy still reports `failure` instead of leaving the Deployment stuck at `in_progress`.
- **Per-environment URL.** `environment_url` is resolved at run time from `environment_config.<env>.environment_url` for the environment being deployed. Environments without a configured URL report an empty URL.
- **Guarded to real GitHub.** Every Deployments API step carries an `if: ${{ github.server_url == 'https://github.com' }}` guard, so on act or gitea (which have no Deployments API) the steps are skipped and the workflow stays runnable.
- **Least-privilege scope.** The toggle adds `deployments: write` to the workflow's top-level permissions only when enabled; the OFF-state output is unchanged.
- **Opt-in and additive.** Omit `deployments` and nothing is emitted. The field did not bump `schema_version`.

### Validate-check workflow (opt-in)

Set `validate_check.enabled: true` and `generate-workflow` emits a lightweight `pull_request` workflow (`.github/workflows/cascade-validate.yaml`) that runs [`cascade parse-config`](/cli-reference/#parse-config) against the manifest and fails the check when the configuration is invalid, so a malformed manifest cannot merge to trunk.

```yaml
ci:
  config:
    validate_check:
      enabled: true
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | false | Emit the manifest-validation PR check (`.github/workflows/cascade-validate.yaml`) |

The check validates cascade's own configuration only. It does not run the repository's build or test suites, requests `contents: read` alone, and has no dry-run or comment side effects.

### Merge-queue workflow (opt-in)

Set `merge_queue.enabled: true` and cascade emits a `merge_group`-triggered workflow (`.github/workflows/cascade-merge-queue.yaml`) that validates the prospective trunk commit: it runs `cascade parse-config` as a validity gate and a dry-run `cascade orchestrate setup` to preview the build and deploy decisions against the merge-group candidate ref.

```yaml
ci:
  config:
    merge_queue:
      enabled: true
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | false | Emit the merge-queue validation lane (`.github/workflows/cascade-merge-queue.yaml`) |

The lane is read-only: no state writes, no releases, no deploys. It reports a status the merge queue can require. This generator owns the lane behavior; the raw `merge_group` trigger itself is expressible separately under `extra_triggers.merge_group`, and the two are intentionally distinct.

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
