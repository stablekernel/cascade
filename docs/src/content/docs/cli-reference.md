---
title: CLI Reference
description: Complete reference for every cascade command, flag, subcommand, environment variable, and exit code.
---

Complete reference for the `cascade` command-line tool.

## Installation

```bash
# Latest stable
go install github.com/stablekernel/cascade/cmd/cascade@latest

# Latest build from master
go install github.com/stablekernel/cascade/cmd/cascade@master

# Specific version
go install github.com/stablekernel/cascade/cmd/cascade@v2.0.4

# Run without installing
go run github.com/stablekernel/cascade/cmd/cascade@latest version
```

In GitHub Actions, the generated workflows install the CLI via `setup-cli`. To invoke it manually:

```yaml
- uses: stablekernel/cascade/.github/actions/setup-cli@master
  with:
    token: ${{ secrets.GITHUB_TOKEN }}
    # version: latest  # 'beta', or a specific version
```

The action downloads the GoReleaser archive (`tar.gz`), extracts it, and installs the `cascade` binary on `PATH`.

## Global Flags

These flags are available on all commands:

| Flag | Type | Description |
|------|------|-------------|
| `--dry-run` | bool | Preview mode: show what would happen without making changes |
| `--trace` | bool | Enable TRACE-level logging for detailed internals |
| `--json` | bool | Output structured JSON for workflow consumption |

## Commands

### version

Display version information.

```bash
cascade version
```

Output (with `--json`):
```json
{
  "version": "v2.0.4",
  "commit": "abc123d",
  "date": "2026-01-15T10:30:00Z"
}
```

### parse-config

Parse and validate the manifest file.

```bash
cascade parse-config
```

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config` | string | auto-detect | Path to manifest file (auto-detects `.github/manifest.yaml`) |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--environment` | string | - | Filter deploys by environment |

### detect-changes

Determine which builds/deploys are triggered by file changes.

```bash
cascade detect-changes \
  --base-sha abc123 \
  --head-sha def456
```

#### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config` | string | No | Path to manifest file (default: auto-detect) |
| `--base-sha` | string | Yes | Base commit SHA |
| `--head-sha` | string | Yes | Head commit SHA |

#### Output

```json
{
  "triggered_builds": ["app"],
  "triggered_deploys": ["cdk", "services"],
  "has_changes": true,
  "changed_files": [
    "src/main.go",
    "cdk/stack.ts"
  ]
}
```

#### Logic

1. Get the changed file list between base and head
2. For each build/deploy, check whether any changed file matches its triggers
3. Build-linked deploys inherit triggers from referenced builds

### generate-changelog

Generate a markdown changelog from conventional commits.

```bash
cascade generate-changelog \
  --base-sha abc123 \
  --head-sha def456 \
  --repo owner/repo
```

#### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--base-sha` | string | Yes | Base commit SHA |
| `--head-sha` | string | Yes | Head commit SHA |
| `--repo` | string | Yes | Repository (`owner/repo`) |
| `--exclude-paths` | string | No | Comma-separated paths to exclude |
| `--contributors` | bool | No | Include contributors section |

#### Output

```json
{
  "changelog": "### Features\n\n- Add user authentication ...",
  "has_breaking": false,
  "has_features": true,
  "has_fixes": true
}
```

#### Conventional Commits

| Type | Category | Included |
|------|----------|----------|
| feat | Features | Yes |
| fix | Bug Fixes | Yes |
| perf | Performance | Yes |
| docs / chore / ci / test / style / refactor | Routine | No |

Breaking changes detected via:
- `!` suffix: `feat!: breaking change`
- Footer: `BREAKING CHANGE: description` (case-sensitive, line start)

### generate-workflow

Generate the orchestrate and promote workflows from the manifest.

```bash
cascade generate-workflow
```

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--action-folder` | string | `manage-release` | Folder for the manage-release composite action |
| `--output`, `-o` | string | `.github/workflows/orchestrate.yaml` | Output path for orchestrate workflow |
| `--promote-output` | string | `.github/workflows/promote.yaml` | Output path for promote workflow |
| `--validate-only` | bool | false | Only validate, don't generate |
| `--dry-run` | bool | false | Print generated workflow to stdout |
| `--force`, `-f` | bool | false | Overwrite output file without prompting |
| `--commit` | bool | false | Commit generated files |
| `--push`, `-p` | bool | false | Push (implies `--commit`) |
| `--orchestrate-only` | bool | false | Only generate `orchestrate.yaml` |
| `--promote-only` | bool | false | Only generate `promote.yaml` |

#### Generated Workflow Features

- **Output discovery**: parses callback workflows to discover declared outputs
- **Dependency ordering**: topological sort honours `depends_on`
- **Output chaining**: passes outputs from one callback to dependents
- **Per-callback policies**: respects `run_policy`, `on_failure`, `retries`
- **Environment overrides**: applies `env_inputs` per environment
- **Publish step**: when `publish:` is configured, the promote workflow dispatches the callback once per build at the boundary where a prerelease becomes a release

### manage-release

Manage GitHub releases.

```bash
cascade manage-release \
  --action create \
  --repo owner/repo \
  --tag v1.0.0 \
  --changelog "Release notes here"
```

#### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--action` | string | Yes | `create`, `update`, `lock`, `prerelease`, `publish`, `delete` |
| `--repo` | string | Yes | Repository (`owner/repo`) |
| `--tag` | string | Yes | Release tag |
| `--environment` | string | No | Target environment |
| `--sha` | string | No | Release commit SHA |
| `--changelog` | string | No | Release notes (markdown) |
| `--changelog-file` | string | No | Path to file containing notes (overrides `--changelog`) |
| `--token` | string | No | GitHub token (or use `GITHUB_TOKEN`) |
| `--previous-tag` | string | No | Previous tag for changelog comparison |
| `--new-tag` | string | No | New semver tag (for `prerelease` action) |
| `--delete-tag` | string | No | Tag to delete after publish (cleanup) |
| `--create-tag` | bool | No | Create git tag on `create` |

#### Actions

| Action | Description |
|--------|-------------|
| `create` | Create a new release |
| `update` | Update an existing release |
| `lock` | Mark as pre-release |
| `prerelease` | Re-tag a draft RC as a non-draft pre-release |
| `publish` | Finalize a release (drops the RC suffix) |
| `delete` | Delete a release |

### orchestrate

Main CI/CD orchestration command with subcommands.

#### orchestrate setup

Prepare the execution plan.

```bash
cascade orchestrate setup \
  --environment dev \
  --sha def456
```

##### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config` | string | No | Path to manifest file (default: auto-detect) |
| `--manifest-key` | string | No | Top-level key (default: `ci`) |
| `--environment` | string | Yes | Target environment (empty for no-env setup) |
| `--sha` | string | No | Head SHA (default: current HEAD) |
| `--gha-output` | bool | No | Write outputs to `$GITHUB_OUTPUT` |

##### Output

```json
{
  "triggered_builds": ["app"],
  "triggered_deploys": ["cdk", "services"],
  "version": "v1.2.0-rc.0",
  "execution_plan": {
    "waves": [
      {"callbacks": ["app", "cdk"]},
      {"callbacks": ["services"]}
    ]
  }
}
```

#### orchestrate finalize

Update state and the release after deployments.

```bash
cascade orchestrate finalize \
  --environment dev \
  --sha abc123 \
  --version v1.2.0-rc.0 \
  --build-results "app:success" \
  --deploy-results "cdk:success,services:success"
```

##### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--environment` | string | Yes | Target environment |
| `--sha` | string | Yes | Head SHA |
| `--version` | string | Yes | Calculated version |
| `--build-results` | string | No | Comma-separated `name:status` pairs |
| `--deploy-results` | string | No | Comma-separated `name:status` pairs |

### promote

Promotion command with subcommands.

#### Persistent Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config` | string | auto-detect | Path to manifest file |
| `--dry-run` | bool | false | Preview without modifying state |
| `--json` | bool | false | Output result as JSON |
| `--gha-output` | bool | false | Write to `$GITHUB_OUTPUT` |
| `--actor` | string | `$GITHUB_ACTOR` | Actor performing the action |

#### promote preflight

Validate and plan a promotion.

```bash
cascade promote preflight \
  --mode dev-to-test \
  --deploys "app,infra" \
  --rollback-on-failure
```

##### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--mode` | string | `default` | `default` or cascade target (e.g., `dev-to-prod`) |
| `--force` | bool | false | Continue on failure (default mode only) |
| `--allow-breaking` | bool | false | Allow breaking changes past the prerelease boundary |
| `--deploys` | string | `all` | Deploys to promote (comma-separated names or `all`) |
| `--rollback-on-failure` | bool | true | Revert successful deploys if any fails |

##### Output

```json
{
  "mode": "cascade",
  "target": "dev-to-test",
  "source_env": "dev",
  "target_env": "test",
  "source_sha": "abc123",
  "rollback_sha": "def456",
  "rollback_on_failure": true,
  "deploys_to_run": ["cdk", "services"],
  "external_deploys_to_run": ["satellite-cdk"],
  "can_proceed": true,
  "version": "v1.2.0-rc.0"
}
```

#### promote finalize

Update state after promotion deploys complete.

```bash
cascade promote finalize \
  --promotion-result "$RESULT_JSON" \
  --repo owner/repo \
  --run-id "$GITHUB_RUN_ID" \
  --commit-push
```

##### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--promotion-result` | string | Yes | JSON from `preflight` output |
| `--repo` | string | No | Repository (`owner/name`) for job query |
| `--run-id` | string | No | Workflow run ID for job query |
| `--commit-push` | bool | No | Commit and push state changes |

### hotfix

Apply a trunk commit onto an environment pinned to an older base. A hotfix targets one environment on its `env/<env>` integration branch. The fix must already be on trunk; cascade refuses to apply a commit that is not an ancestor of trunk tip. The subcommands compute and validate the hotfix and write its final state; the cherry-pick, build, and deploy run in the generated `cascade-hotfix.yaml` workflow. See the Hotfix section of [Workflows](/cascade/workflows/) for the full flow.

#### hotfix plan

Validate a hotfix request and compute the integration-branch plan. It enforces, in order: trunk ancestry of the fix, target-environment eligibility (a configured environment that is not the first; prod is allowed), no-op detection when the fix is already in the target, the single-flight open-pull-request gate, and `env/<env>` branch reconciliation. With `--dry-run` nothing is mutated (the env branch is planned but not created).

```bash
cascade hotfix plan \
  --commit abc1234 \
  --target-env test \
  --gha-output
```

##### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config`, `-c` | string | No | Path to manifest file (default: `.github/manifest.yaml`) |
| `--key` | string | No | Top-level manifest key (default: `ci`) |
| `--commit` | string | Yes | Trunk commit (SHA or ref) carrying the fix |
| `--target-env` | string | Yes | Environment to hotfix |
| `--actor` | string | No | Actor recorded on the plan (default: `$GITHUB_ACTOR`) |
| `--remote` | string | No | Git remote env branches live on (default: `origin`) |
| `--repo` | string | No | `owner/repo` for single-flight pull-request lookup via `gh` (default: skip the check) |
| `--dry-run` | bool | No | Compute the plan without mutating anything |
| `--json` | bool | No | Output the plan as JSON |
| `--gha-output` | bool | No | Write outputs to `$GITHUB_OUTPUT` for workflow consumption |

##### Output

With `--json`:

```json
{
  "target_env": "test",
  "fix_sha": "abc1234...",
  "branch": "env/test",
  "base_sha": "def5678...",
  "no_op": false,
  "branch_created": true,
  "hotfix_version_candidate": "v1.4.0-rc.2.hotfix.1",
  "conflict_expected": false,
  "protection_suggestions": ["gh api -X PUT repos/{owner}/{repo}/branches/env/test/protection ..."],
  "dry_run": false
}
```

The GHA output writes `target_env`, `fix_sha`, `branch`, `base_sha`, `no_op`, `branch_created`, `hotfix_version_candidate`, `conflict_expected`, `dry_run`, and the `protection_suggestions` commands (as JSON and as multiline text).

#### hotfix finalize

Write the diverged state, tag, and release for a merged hotfix. Run after the resolution pull request merges and the build and deploy succeed. It cross-checks the merge SHA against the `env/<target>` branch tip, allocates the next free hotfix version, snapshots the prior state into the rollback ring, writes the divergence fields and substates, commits the manifest to trunk with the rebase-retry push, and creates the hotfix tag and release object. The verb is idempotent on identical inputs: a rerun after the state already records the merge SHA is a no-op.

```bash
cascade hotfix finalize \
  --target-env test \
  --merge-sha 1234abc \
  --fix-sha abc1234 \
  --base-sha def5678 \
  --build-result app=success \
  --deploy-result app=success
```

##### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config`, `-c` | string | No | Path to manifest file (default: `.github/manifest.yaml`) |
| `--key` | string | No | Top-level manifest key (default: `ci`) |
| `--target-env` | string | Yes | Environment to finalize |
| `--merge-sha` | string | Yes | Tip of `env/<target>` after the resolution pull request merged |
| `--fix-sha` | string | Yes | Trunk commit the hotfix carries |
| `--base-sha` | string | Yes | Trunk anchor the integration branch diverged from |
| `--actor` | string | No | Actor recorded on the state (default: `$GITHUB_ACTOR`) |
| `--dry-run` | bool | No | Validate and compute without writing state, tags, or releases |
| `--build-result` | string | No | Build result as `name=result` (repeatable) |
| `--deploy-result` | string | No | Deploy result as `name=result` (repeatable) |

Only successful build and deploy results update the per-build and per-deploy substates. For a prerelease-environment target the hotfix release is promoted to a GitHub prerelease, superseding that environment's current prerelease object; for other environments it stays a draft.

### next-version

Calculate the next semantic version.

```bash
cascade next-version \
  --environment prod \
  --base-sha abc123 \
  --head-sha def456
```

#### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config`, `-c` | string | No | Path to manifest file |
| `--environment`, `-e` | string | Yes | Target environment |
| `--base-sha` | string | No | Base SHA (defaults to next env's SHA) |
| `--head-sha` | string | No | Head SHA (defaults to HEAD) |
| `--json` | bool | No | Output as JSON |

Bump rules:
- Breaking change (`feat!`, `BREAKING CHANGE:`) triggers a major bump
- Feature (`feat`) triggers a minor bump
- Fix (`fix`) triggers a patch bump
- Pre-release environments append an RC suffix (e.g., `v1.3.0-rc.0`)

### external

Commands for multi-repo orchestration.

#### external update

Update primary repo state when a satellite repo deploys.

```bash
cascade external update \
  --source-repo org/cdk-infra \
  --deploy-name cdk \
  --environment dev \
  --sha abc123 \
  --version v1.2.0 \
  --artifacts '{"image_tag": "cdk-abc123"}'
```

##### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config` | string | No | Path to manifest file |
| `--source-repo` | string | Yes | Source repository (e.g., `org/cdk-infra`) |
| `--deploy-name` | string | Yes | Deploy name |
| `--environment` | string | Yes | Target environment |
| `--sha` | string | Yes | Commit SHA from source repo |
| `--version` | string | No | Version from source repo |
| `--artifacts` | string | No | Artifacts JSON |
| `--dry-run` | bool | No | Preview mode |

This is typically called by the satellite's `external-update.yaml` workflow after deploying to dev.

### reset

Reset releases and state for testing.

```bash
cascade reset --state --push
```

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--state` | bool | false | Reset the state section in the manifest |
| `--dry-run` | bool | false | Preview without executing |
| `--push` | bool | false | Push state changes (requires `--state`) |
| `--repo` | string | cwd | Path to repository |
| `--config` | string | auto-detect | Path to manifest file |
| `--manifest-key` | string | `ci` | Top-level key |

Deletes all GitHub releases and tags. With `--state`, also clears the state section.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | GitHub API token for releases |
| `GITHUB_ACTOR` | Default actor for commits/promotions |
| `GITHUB_RUN_ID` | Workflow run ID (used by promote finalize) |
| `LOG_LEVEL` | Logging verbosity (info, debug, trace) |
| `NO_COLOR` | Disable colored output |

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |

## JSON Output

Pass `--json` (or use `--gha-output` inside Actions) to get machine-readable output:

```yaml
- name: Detect changes
  id: detect
  run: |
    cascade detect-changes \
      --base-sha "${{ github.event.before }}" \
      --head-sha "${{ github.sha }}" \
      --gha-output
```

## Debugging

```bash
cascade --trace parse-config
```

Trace logs include:
- File matching details
- Dependency resolution steps
- API request/response info
