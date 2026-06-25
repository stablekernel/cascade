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

### init

Scaffold a starter manifest and matching callback workflow stubs, verified through the real generator before anything is written.

```bash
# Two-environment pipeline (dev, prod) in the current directory
cascade init --topology two-env

# Custom ordered environments; the last is the release stage
cascade init --envs staging,production --name my-service

# Preview without writing
cascade init --topology three-env --dry-run
```

`init` renders `.github/manifest.yaml` plus build (and, when environments are set, deploy) stubs under `.github/workflows`, runs the manifest through parse, validation, and generation, and only then writes the files. It also drops a starter `.github/CODEOWNERS` and an `.github/aws-oidc-role.example.json` IAM trust-policy example for GitHub Actions OIDC; both carry placeholder owners and account IDs to replace. The manifest carries a `$schema` directive for editor autocomplete and validation. After running it, fill in the stub callbacks, commit, and run `cascade generate-workflow`. The scaffold is a verifying starter: `cascade init` then `cascade generate-workflow` leaves `cascade verify` clean, so you can wire a drift gate from the first commit.

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--topology` | string | `two-env` | Preset shape: one of `no-env`, `two-env`, `three-env`, `four-env` |
| `--envs` | string | - | Comma-separated ordered environment names; the last is the release stage (overrides `--topology`) |
| `--name` | string | dir base name | Project name woven into the stubs |
| `--dir` | string | `.` | Target directory to scaffold into |
| `--cli-version` | string | pinned release | cascade CLI version pinned in the manifest |
| `--force`, `-f` | bool | false | Overwrite existing files |
| `--dry-run` | bool | false | Print what would be written without writing anything |

Environment names are positional, not semantic: the last name is the release stage. An empty list (`--topology no-env`) produces a release-only project with no deploy stub. If any target file already exists and `--force` is not set, `init` aborts and lists the conflicts, writing nothing.

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

### verify

Check that the committed workflow and action files match what the manifest would currently generate, without writing anything. `verify` is read-only: it never writes files, runs git, or modifies the repository.

```bash
cascade verify
```

`verify` reports drift when a file the manifest would generate is missing on disk, or when its committed bytes differ from the generated bytes. It covers the complete set of files `generate-workflow` emits (orchestrate, promote or release, external-update, validate-check, merge-queue, hotfix, rollback, pr-preview, drift-check, drift-comment, and the manage-release composite action), so adopters do not need to enumerate files by hand.

`verify` also reports orphans: cascade-owned workflow files left behind in the workflows output directory that the manifest no longer plans (for example, after removing an environment or build). Only files carrying the cascade-generated marker are considered, so hand-written workflows in the same directory are never flagged. An orphan is reported as drift and exits 1 like any other drift. Pass `--allow-orphans` to skip this check when stale generated files are expected. Orphan detection reads the workflows output directory only and never deletes anything.

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--action-folder` | string | `manage-release` | Folder for the manage-release composite action |
| `--output`, `-o` | string | `.github/workflows/orchestrate.yaml` | Path of the orchestrate workflow |
| `--promote-output` | string | `.github/workflows/promote.yaml` | Path of the promote workflow |
| `--quiet`, `-q` | bool | false | Suppress the per-file report body; only set the exit code |
| `--allow-orphans` | bool | false | Do not report cascade-owned workflow files that are no longer in the plan as drift |

#### Exit codes

| Exit | Meaning |
|------|---------|
| 0 | No drift: every generated file is present and byte-identical, and no orphaned generated files remain |
| 1 | Drift detected: a generated file is missing, its committed bytes differ, or a cascade-owned file is orphaned (unless `--allow-orphans` is set) |
| 2 | Error: the manifest is missing or invalid, or another operational failure prevented the check from running |

#### Use in CI

`verify` replaces a hand-rolled "regenerate and `git diff`" drift check with a single step. A CI job can run `cascade verify` to fail the build whenever committed workflows fall out of sync with the manifest:

```yaml
- run: cascade verify
```

Rather than wire this job by hand, set `drift_check.enabled: true` in the manifest and `generate-workflow` emits the drift-check workflow for you. See [Drift-check workflow](/configuration/#drift-check-workflow-opt-in).

### plan

Preview, as a per-file unified diff, what `generate-workflow` would change in the committed workflow and action files, without writing anything. `plan` is read-only: it never writes files, runs git, or modifies the repository.

```bash
cascade plan
```

`plan` is the human-facing preview counterpart to `verify`. For every file the manifest would generate, it prints the diff between the committed bytes and the generated bytes: a new file appears as a whole-file add, a changed file as a unified hunk, and a file already in sync produces no diff. When nothing is pending it prints a single `plan: N files, no pending changes` line; otherwise it prints the diffs followed by a summary of how many files would change.

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--action-folder` | string | `manage-release` | Folder for the manage-release composite action |
| `--output`, `-o` | string | `.github/workflows/orchestrate.yaml` | Path of the orchestrate workflow |
| `--promote-output` | string | `.github/workflows/promote.yaml` | Path of the promote workflow |

#### Exit codes

| Exit | Meaning |
|------|---------|
| 0 | Success, whether or not any diff was printed. `plan` is informational, so a pending change does not change the exit code |
| non-zero | Error: the manifest is missing or invalid, or another operational failure prevented the preview from running |

#### plan versus verify

`plan` and `verify` are separate commands with separate contracts. `plan` is the human preview you read before regenerating: it shows the actual diff and always exits 0 on success, so it never fails a build on its own. `verify` is the pass/fail gate you wire into CI: it prints a terse drift report and exits 1 on drift, 2 on an operational failure, so it fails the build when committed workflows fall out of sync. Reach for `plan` at the terminal to see what would change, and for `verify` in a CI job to enforce that nothing has.

### branch-protection

Emit the JSON body an operator applies to GitHub's branch-protection API for a cascade-managed trunk. cascade emits the file; the operator applies it. cascade never calls the GitHub API.

```bash
cascade branch-protection
```

The output is a wrapper with two top-level keys:

- `protection` is the exact body to PUT to the branches protection API.
- `operator_todo` is companion guidance and is NOT part of the PUT body.

Apply it by sending only the `.protection` object:

```bash
cascade branch-protection | jq .protection | \
  gh api -X PUT repos/my-org/my-app/branches/main/protection --input -
```

#### What ends up required, and why it is safe

The required status checks contain only the cascade-controlled `Setup` and `Finalize` jobs. These are the orchestrate workflow's two steps jobs; cascade knows their exact check-run names and both run on every pipeline run. Because of that, `.protection` applied as-is never creates a required check that can never report, so it never blocks a pull request on its own.

The reusable-workflow caller jobs (validate, build, deploy) are deliberately left out of the required contexts. cascade knows each caller's display-name prefix (for example `Build (my-app)`) but not the inner job name that GitHub appends to form the real check-run context, which is `<DisplayName> / <inner-job>`. That inner job lives in your reusable workflow, which cascade does not author. Requiring a bare prefix would never match and would block every pull request, so cascade lists those prefixes under `operator_todo.complete_these_contexts` as `<DisplayName> / <inner-job>` placeholders instead. Replace `<inner-job>` with the job name inside each reusable workflow, then add the completed strings to `required_status_checks.contexts` when you want them required.

The `--branch` flag only labels the guidance note. The required contexts are the same across branches and environments because they are the orchestrate-workflow steps jobs, so `--env` would not change them and is not offered.

This command complements the hotfix branch-protection advisory (see [Hotfix workflow](/workflows/#hotfix-workflow)): the advisory prints ready-to-run `gh` commands for env branches, while `branch-protection` emits the full PUT body for the trunk.

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--branch` | string | `main` | Branch the protection targets (labels the guidance note only; does not change the required contexts) |
| `--output`, `-o` | string | stdout | Write to this path instead of stdout (`-` also means stdout) |

### environments

Emit a per-environment configuration file an operator applies to GitHub's Environments REST API. cascade emits the file; the operator applies it. cascade never calls the GitHub API.

```bash
cascade environments
```

The output is a wrapper. The top-level `environments` is an array with one entry per manifest environment, in the manifest's `environments` order. Each entry has:

- `name` is the cascade environment name.
- `gha_environment` is the GitHub Environment to configure; it defaults to `name`.
- `environment` is the exact body to PUT to the environments API.
- `operator_todo` is companion guidance and is NOT part of the PUT body.

Apply it by sending only the `.environment` object per entry:

```bash
cascade environments | jq -c '.environments[] | {gha_environment, environment}' | while read -r row; do
  env=$(jq -r .gha_environment <<<"$row")
  jq .environment <<<"$row" | gh api -X PUT "repos/my-org/my-app/environments/$env" --input -
done
```

The per-environment settings come from the manifest under `config.environment_config.<env>`:

```yaml
config:
  environments: [dev, test, prod]
  environment_config:
    prod:
      gha_environment: production
      required_reviewers: [team/ops]
      wait_timer: 10
      branch_policy: protected
      secrets: [MY_SECRET]
      variables: [REGION]
```

#### What the body carries, and what is operator guidance

The `.environment` body holds only the fields cascade can fully form from the manifest:

- `wait_timer` in minutes (0..43200).
- `deployment_branch_policy`, mapped from the manifest `branch_policy`: `protected` becomes `{protected_branches: true, custom_branch_policies: false}`; `custom` becomes `{protected_branches: false, custom_branch_policies: true}`; `all` or unset becomes `null`, meaning all branches.

Everything else cascade cannot fully form from the manifest is surfaced under `operator_todo` so the operator can finish it:

- `operator_todo.required_reviewers` lists user and team slugs, NOT the body. The REST API requires a numeric reviewer id that the manifest does not carry, so the operator resolves each slug to an id and adds it to the body's `reviewers` array.
- `operator_todo.secrets` and `operator_todo.variables` list the expected env-scoped secret and variable names. cascade emits names only, never values; the operator creates them with values through the environment-secrets and environment-variables APIs.
- `branch_patterns` and `tag_patterns` (custom policy only) are created through the deployment-branch-policies API and are surfaced under `operator_todo`.

The output is deterministic: the same manifest yields byte-identical output, and environments follow the manifest order.

This is the sibling of the [branch-protection](#branch-protection) command, using the same emit-a-config-file pattern (operator applies; cascade never calls the API).

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--output`, `-o` | string | stdout | Write to this path instead of stdout (`-` also means stdout) |

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
| `--allow-downgrade` | bool | false | Permit promoting an older version onto an env (a downgrade). Blocked by default; prod always requires this flag |

A promotion that would place a strictly older semver version onto an env than the version it currently holds is a downgrade. Preflight blocks it by default, naming both versions and the env. Pass `--allow-downgrade` to permit it. The terminal (prod) env always requires the flag, even when a lower env in the same cascade already permitted the same downgrade. Equal versions are an idempotent re-promote and are never treated as a downgrade. When either version is not parseable as semver the gate fails open with a warning rather than blocking, so non-semver pipelines keep working.

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

Apply one or more trunk commits onto an environment pinned to an older base. A hotfix elevates the commit set bottom-up across the environment chain, up to and including the target environment, on each environment's `env/<env>` integration branch. The fixes must already be on trunk; cascade refuses to apply any commit that is not an ancestor of trunk tip. The subcommands compute and validate the hotfix and write its final state; the cherry-pick, build, and deploy run in the generated `cascade-hotfix.yaml` workflow. See the Hotfix section of [Workflows](/cascade/workflows/) for the full flow.

#### hotfix plan

Validate a hotfix request and compute the integration-branch plan. It enforces, in order: trunk ancestry of every fix, target-environment eligibility (a configured environment that is not the first; prod is allowed), no-op detection when a fix is already present, the single-flight open-pull-request gate, and `env/<env>` branch reconciliation. With `--dry-run` nothing is mutated (the env branches are planned but not created).

Supply the fixes with one of two mutually exclusive flags, exactly one of which is required:

- `--commit <sha>` applies a single commit to the target environment.
- `--commits <sha,sha,...>` takes a comma-delimited set and elevates it bottom-up across the chain, from the environment above the first up to and including `--target-env`. On this path each (commit, environment) pair is skipped when the commit is already an ancestor of that environment's state SHA or already in its recorded `patches`; an environment whose whole set is already present is a no-op and the chain moves on.

```bash
cascade hotfix plan \
  --commit abc1234 \
  --target-env test \
  --gha-output
```

To carry a set of commits and elevate them across the chain:

```bash
cascade hotfix plan \
  --commits abc1234,def5678 \
  --target-env staging \
  --gha-output
```

##### Flags

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config`, `-c` | string | No | Path to manifest file (default: `.github/manifest.yaml`) |
| `--key` | string | No | Top-level manifest key (default: `ci`) |
| `--commit` | string | One of `commit`/`commits` | Single trunk commit (SHA or ref) carrying the fix; single-env path |
| `--commits` | string | One of `commit`/`commits` | Comma-delimited trunk commits to elevate across the env chain up to `--target-env` |
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

The GHA output writes `target_env`, `fix_sha`, `branch`, `base_sha`, `no_op`, `branch_created`, `hotfix_version_candidate`, `conflict_expected`, `dry_run`, and the `protection_suggestions` commands (as JSON and as multiline text). On the `--commits` path the plan also writes `env_sequence` (the environments to walk bottom-up) and a `commits_<env>` list per environment that the apply job replays in order.

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
| `--fix-sha` | string | Yes | Trunk commit(s) the hotfix carries; comma-delimited for a multi-commit set. Every commit applied to the environment is appended to its recorded `patches` (commits already present in that environment are skipped) |
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

### schema

Print the manifest JSON Schema. Point your editor at it for autocomplete, type checking, and hover docs while authoring `.github/manifest.yaml`. See [Editor support](/cascade/configuration/#editor-support) for registration.

```bash
# Print the schema to stdout
cascade schema

# Write the schema to a file
cascade schema --output manifest.schema.json
```

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--output`, `-o` | string | stdout | Write the schema to a file instead of stdout |

The same schema is published at `https://stablekernel.github.io/cascade/manifest.schema.json`. `parse-config` remains the authority for semantic and cross-field rules; the schema covers structure, types, enums, and hover docs.

### simulate

Preview a hypothetical action against a clone of your manifest and print what would happen, without changing anything. The engine replays the real orchestration logic in record-only mode: it touches no GitHub, starts no container, runs no git command, and leaves the manifest untouched. It validates orchestration, the state transitions and run/skip/gate decisions, not your build and deploy scripts.

```bash
cascade simulate promote
cascade simulate release
cascade simulate rollback --env prod
cascade simulate hotfix --env uat --fix <sha>
```

Each run prints a before/after state diff and an ordered effect sequence. The four subcommands are `promote`, `release`, `rollback`, and `hotfix`. See [Local Simulation](/cascade/simulate/) for the full walkthrough, example output, and the deploy-stub model.

#### Flags

The following flags are shared by every subcommand.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config` | string | auto-detect | Path to manifest file |
| `--actor` | string | (none) | Actor performing the hypothetical action |
| `--deploy-result` | string | (none) | Simulated outcome for a build or deploy callback, `name=success\|failure\|skipped` (repeatable) |
| `--json` | bool | `false` | Output result as JSON |

Subcommand-specific flags: `promote` takes `--mode` (`default` or `cascade`) and `--target`; `rollback` takes `--env` (required), `--to`, and `--deployable`; `hotfix` takes `--env` (required), `--fix`, and `--merge-sha`; `release` takes only the shared flags.

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
