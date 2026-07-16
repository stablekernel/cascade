---
title: CLI reference
description: Every cascade command, flag, subcommand, environment variable, and exit code, verified against the shipped binary.
---

This page is the complete reference for the `cascade` command-line tool: its global
flags, every command and subcommand, the environment variables it reads, its exit codes,
and its JSON output. It serves operators wiring pipelines and contributors reading
generated workflows. Reach for a command by name; the how-to guides link here for flag
detail.

## Install

Install is documented in one place. See [Getting started](/cascade/start/getting-started/)
for the canonical install section, including the pinned `go install` form and the
`setup-cli` action used inside generated workflows. The short version:

```bash
go install github.com/stablekernel/cascade/cmd/cascade@latest
```

## Global flags

These flags are available on every command.

| Flag | Type | Description |
|------|------|-------------|
| `--dry-run` | bool | Preview mode: show what would happen without making changes |
| `--trace` | bool | Enable TRACE-level logging for detailed internals |
| `--json` | bool | Output structured JSON for workflow consumption |

### How the manifest is located

Most commands (`generate-workflow`, `verify`, `plan`, `graph`, `status`, and the
promotion lifecycle commands) auto-detect the manifest at `.github/manifest.yaml` when
`--config` is not given. Two commands are the exception: `lint` and
`detect-changes` default `--config` to the literal path `cicd-config.yaml` and do NOT
auto-detect. Pass `--config` explicitly to point either one at a different file.

## Commands

Commands are grouped by how often you reach for them:

- **Everyday**: `version`, `init`, `generate-workflow`, `verify`, `plan`, `status`, `graph`
- **Preview and inspect**: `simulate`, `lint`, `detect-changes`
- **Promotion lifecycle**: `orchestrate`, `promote`, `hotfix`, `rollback`
- **Releases and versioning**: `next-version`, `generate-changelog`, `manage-release`
- **Multi-repo**: `external`
- **Setup and governance**: `schema`, `environments`, `branch-protection`, `reconcile`
- **Maintenance**: `reset`

### version

Display version information.

```bash
cascade version
```

Output:

```text
cascade v0.16.2
  commit: abc123d
  built:  2026-01-15T10:30:00Z
```

### init

Scaffold a starter manifest and matching callback workflow stubs, verified through the
real generator before anything is written.

```bash
# Two-environment pipeline (dev, prod) in the current directory
cascade init --topology two-env

# Custom ordered environments; the last is the release stage
cascade init --envs staging,production --name my-service

# Preview without writing
cascade init --topology three-env --dry-run
```

`init` renders `.github/manifest.yaml` plus build (and, when environments are set, deploy)
stubs under `.github/workflows`, runs the manifest through parse, validation, and
generation, and only then writes the files. It also drops a starter `.github/CODEOWNERS`
and an `.github/aws-oidc-role.example.json` IAM trust-policy example for GitHub Actions
OIDC; both carry placeholder owners and account IDs to replace. The manifest carries a
`$schema` directive for editor autocomplete and validation. After running it, fill in the
stub callbacks, commit, and run `cascade generate-workflow`. The scaffold is a verifying
starter: `cascade init` then `cascade generate-workflow` leaves `cascade verify` clean, so
you can wire a drift gate from the first commit.

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

Environment names are positional, not semantic: the last name is the release stage. An
empty list (`--topology no-env`) produces a release-only project with no deploy stub. If
any target file already exists and `--force` is not set, `init` aborts and lists the
conflicts, writing nothing.

### generate-workflow

Generate the pipeline workflows from the manifest. A run emits the full set of
cascade-owned workflows and the `manage-release` composite action; see
[Generated workflows](/cascade/reference/generated-workflows/) for the exact file set and
each workflow's anatomy.

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
| `--orchestrate-only` | bool | false | Only generate the orchestrate workflow |
| `--promote-only` | bool | false | Only generate the promote workflow |

#### Generated workflow features

- **Output discovery**: parses callback workflows to discover declared outputs.
- **Dependency ordering**: topological sort honours `depends_on`.
- **Output chaining**: passes outputs from one callback to dependents.
- **Per-callback policies**: respects `run_policy`, `on_failure`, `retries`.
- **Environment overrides**: applies `env_inputs` per environment.
- **Publish step**: when `publish:` is configured, the promote workflow dispatches the
  callback once per build at the boundary where a prerelease becomes a release.

### verify

Check that the committed workflow and action files match what the manifest would currently
generate, without writing anything. `verify` is read-only: it never writes files, runs
git, or modifies the repository.

```bash
cascade verify
```

`verify` reports drift when a file the manifest would generate is missing on disk, or when
its committed bytes differ from the generated bytes. It covers the complete set of files
`generate-workflow` emits (orchestrate, promote or release, external-update,
validate-check, merge-queue, hotfix, rollback, pr-preview, drift-check, drift-comment, and
the manage-release composite action), so adopters do not need to enumerate files by hand.

`verify` also reports orphans: cascade-owned workflow files left behind in the workflows
output directory that the manifest no longer plans (for example, after removing an
environment or build). Only files carrying the cascade-generated marker are considered, so
hand-written workflows in the same directory are never flagged. An orphan is reported as
drift and exits `1` like any other drift. Pass `--allow-orphans` to skip this check when
stale generated files are expected. Orphan detection reads the workflows output directory
only and never deletes anything.

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
| `0` | No drift: every generated file is present and byte-identical, and no orphaned generated files remain |
| `1` | Drift detected: a generated file is missing, its committed bytes differ, or a cascade-owned file is orphaned (unless `--allow-orphans` is set) |
| `2` | Operational failure: the manifest is missing or invalid, or another error prevented the check from running |

These are the same three process exit codes described in [Exit codes](#exit-codes-1)
below; `verify` is the command that returns `2` to distinguish an operational failure from
drift.

#### Use in CI

`verify` replaces a hand-rolled "regenerate and `git diff`" drift check with a single
step. A CI job can run `cascade verify` to fail the build whenever committed workflows fall
out of sync with the manifest:

```yaml
- run: cascade verify
```

Rather than wire this job by hand, set `drift_check.enabled: true` in the manifest and
`generate-workflow` emits the drift-check workflow for you. See
[Generated workflows](/cascade/reference/generated-workflows/) for the opt-in companions.

### plan

Preview, as a per-file unified diff, what `generate-workflow` would change in the committed
workflow and action files, without writing anything. `plan` is read-only: it never writes
files, runs git, or modifies the repository.

```bash
cascade plan
```

`plan` is the human-facing preview counterpart to `verify`. For every file the manifest
would generate, it prints the diff between the committed bytes and the generated bytes: a
new file appears as a whole-file add, a changed file as a unified hunk, and a file already
in sync produces no diff. When nothing is pending it prints a single
`plan: N files, no pending changes` line; otherwise it prints the diffs followed by a
summary of how many files would change.

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
| `0` | Success, whether or not any diff was printed. `plan` is informational, so a pending change does not change the exit code |
| non-zero | Error: the manifest is missing or invalid, or another operational failure prevented the preview from running |

#### plan versus verify

`plan` and `verify` are separate commands with separate contracts. `plan` is the human
preview you read before regenerating: it shows the actual diff and always exits `0` on
success, so it never fails a build on its own. `verify` is the pass/fail gate you wire into
CI: it prints a terse drift report and exits `1` on drift, `2` on an operational failure,
so it fails the build when committed workflows fall out of sync. Reach for `plan` at the
terminal to see what would change, and for `verify` in a CI job to enforce that nothing
has.

### status

Query deployed state recorded in the manifest. Every `status` command is read-only: it
loads the manifest and prints recorded state, never writing files or calling GitHub. With
no subcommand, `status` summarizes every environment together with `latest_release`.

```bash
cascade status
```

#### Persistent flags

These flags apply to `status` and all its subcommands.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file (default `.github/manifest.yaml`) |
| `--key` | string | `ci` | Top-level manifest key |
| `--json` | bool | false | Output as JSON |

#### status env

Show the version, SHA, and commit metadata recorded for a single environment. Takes the
environment name as a positional argument.

```bash
cascade status env prod
```

#### status build

Show the build state (SHA, artifact id, built_at, tags) for a build within an environment.
Takes the build name as a positional argument.

```bash
cascade status build app --env prod
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--env` | string | - | Environment name (required) |

#### status deploy

Show the deploy state (SHA, deployed_at) for a deploy within an environment. Takes the
deploy name as a positional argument.

```bash
cascade status deploy services --env prod
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--env` | string | - | Environment name (required) |

#### status consistency

Flag `env/*` integration branches that have no matching divergence in the manifest. A
hotfix creates an `env/<name>` branch that exists only while the environment is diverged;
when the environment rejoins trunk the branch is deleted. A leftover `env/<name>` branch
with no diverged environment behind it is an orphan from an interrupted hotfix or manual
branch creation.

By default the command only reports. With `--fix` it deletes each flagged orphan branch on
the remote (default `origin`). A branch that backs a diverged environment is never touched,
and deleting an already-absent branch is a no-op, so `--fix` is safe to re-run.

```bash
cascade status consistency
cascade status consistency --fix
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--fix` | bool | false | Delete the flagged orphan `env/*` branches on the remote |
| `--remote` | string | `origin` | Remote to inspect and, with `--fix`, delete orphan branches on |

### graph

Render the manifest's generated pipeline as a Mermaid diagram on stdout. GitHub renders
Mermaid natively in Markdown, so the output pastes directly into a README or pull request.
`graph` is read-only: it never writes files, runs git, or modifies the repository. A
missing or invalid manifest is reported as an error.

```bash
cascade graph
cascade graph --granularity env
cascade graph --granularity stages > pipeline.mmd
```

The `--granularity` flag chooses the projection:

- `jobs` renders the full job dependency graph, with hard dependencies as solid arrows and
  optional ordering-only ones as dotted arrows.
- `stages` renders the coarse lifecycle flow from trunk through build, deploy, and promote
  to release.
- `env` renders the promotion state machine, including any hotfix divergence and rejoin.
- `cross-repo` renders the multi-repo flow, a lane per repository with the primary
  coordinating its dependent satellites and any satellite-to-primary notify edge.

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file (default `.github/manifest.yaml`) |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--granularity` | string | `jobs` | Pipeline projection to render: `jobs`, `stages`, `env`, or `cross-repo` |
| `--format` | string | `mermaid` | Diagram output format (supported value: `mermaid`) |
| `--theme` | string | `cascade` | Diagram theme: `cascade`, `bland`, or a path to a JSON theme file |

See [Visualize the pipeline](/cascade/guides/visualize/) for the task-oriented walkthrough.

### simulate

Preview a hypothetical action against a clone of your manifest and print what would happen,
without changing anything. The engine replays the real orchestration logic in record-only
mode: it touches no GitHub, starts no container, runs no git command, and leaves the
manifest untouched. It validates orchestration, the state transitions and run/skip/gate
decisions, not your build and deploy scripts.

```bash
cascade simulate promote
cascade simulate release
cascade simulate rollback --env prod
cascade simulate hotfix --env uat --fix <sha>
```

Each run prints a before/after state diff and an ordered effect sequence. The four
subcommands are `promote`, `release`, `rollback`, and `hotfix`. `hotfix` also prints a
`Cherry-pick chain` section: the ordered environments the fix elevates through, bottom-up
from the second environment up to and including the `--env` target, one cherry-pick per
environment. See [Simulate and verify](/cascade/guides/simulate-and-verify/) for the full
walkthrough, example output, and the deploy-stub model.

On a multi-component (monorepo) manifest, pass `--component <name>` to scope the
simulation to one declared component. The engine then reads and replays that
component's recorded state under `state.components.<name>.<env>`, the same path the
component-scoped `promote`, `rollback`, and `hotfix` commands use:

```bash
cascade simulate promote --component api
cascade simulate rollback --component web --env prod
```

`--component` is required when the manifest declares components: a manifest with no
flat `state.<env>` rows would otherwise replay against empty state. The flag is left
empty on a single-component manifest, which keeps the flat behavior unchanged.

#### Flags

The following flags are shared by every subcommand.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config` | string | auto-detect | Path to manifest file |
| `--actor` | string | (none) | Actor performing the hypothetical action |
| `--component` | string | (single-component) | Declared component to scope the simulation to (required for multi-component manifests) |
| `--deploy-result` | string | (none) | Simulated outcome for a build or deploy callback, `name=success\|failure\|skipped` (repeatable) |
| `--json` | bool | `false` | Output result as JSON |

Subcommand-specific flags: `promote` takes `--mode` (`default` or `cascade`) and
`--target`; `rollback` takes `--env` (required), `--to`, and `--deployable`; `hotfix` takes
`--env` (required), `--fix`, and `--merge-sha`; `release` takes only the shared flags.

### lint

Parse a manifest, validate it, and report any errors or warnings. Unknown or misspelled
keys are rejected with a "did you mean" suggestion, so a typo or a field pasted at the wrong
level is a hard error rather than a silently ignored line. By default the report is
human-readable and the command exits non-zero when the manifest is invalid; pass `--json` to
emit the parsed config and validation result (`valid`/`errors`/`warnings`) as JSON, the
surface the generated validation lanes consume. This command defaults `--config` to the
literal `cicd-config.yaml` path and does NOT auto-detect `.github/manifest.yaml`; pass
`--config` to point it at another file.

```bash
cascade lint --config .github/manifest.yaml
cascade lint --json --config .github/manifest.yaml
```

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | `cicd-config.yaml` | Path to the manifest file (no auto-detection) |
| `--json` | bool | `false` | Emit the parsed config and validation result as JSON |

### detect-changes

Determine which builds and deploys are triggered by file changes between two commits. Like
`lint`, this command defaults `--config` to the literal `cicd-config.yaml` path and
does NOT auto-detect `.github/manifest.yaml`.

```bash
cascade detect-changes \
  --config .github/manifest.yaml \
  --base-sha abc123 \
  --head-sha def456
```

#### Flags

| Flag | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `--config`, `-c` | string | No | `cicd-config.yaml` | Path to the manifest file (no auto-detection) |
| `--base-sha` | string | Yes | - | Base commit SHA |
| `--head-sha` | string | Yes | - | Head commit SHA |

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

1. Get the changed file list between base and head.
2. For each build or deploy, check whether any changed file matches its triggers.
3. Build-linked deploys inherit triggers from referenced builds.

### orchestrate

Main orchestration command with subcommands. It drives the generated orchestrate workflow.

#### orchestrate setup

Prepare the execution plan.

```bash
cascade orchestrate setup \
  --environment dev \
  --sha def456
```

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config` | string | No | Path to manifest file (auto-detect) |
| `--manifest-key` | string | No | Top-level key (default `ci`) |
| `--environment` | string | Yes | Target environment (empty for no-env setup) |
| `--sha` | string | No | Head SHA (default: current HEAD) |
| `--gha-output` | bool | No | Write outputs to `$GITHUB_OUTPUT` |
| `--component` | string | No | Declared component to scope this orchestration to (multi-component manifests) |

Output:

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

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--environment` | string | Yes | Target environment |
| `--sha` | string | Yes | Head SHA |
| `--version` | string | Yes | Calculated version |
| `--build-results` | string | No | Comma-separated `name:status` pairs |
| `--deploy-results` | string | No | Comma-separated `name:status` pairs |
| `--component` | string | No | Declared component to scope this orchestration to (multi-component manifests). Persistent across `orchestrate` subcommands. |

### promote

Promotion command with subcommands. For the operator recipe see
[Promote a release](/cascade/guides/promote/).

#### Persistent flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config` | string | auto-detect | Path to manifest file |
| `--dry-run` | bool | false | Preview without modifying state |
| `--json` | bool | false | Output result as JSON |
| `--gha-output` | bool | false | Write to `$GITHUB_OUTPUT` |
| `--actor` | string | `$GITHUB_ACTOR` | Actor performing the action |
| `--component` | string | (single-component) | Declared component to scope promotion state to (multi-component manifests) |

#### promote preflight

Validate and plan a promotion.

```bash
cascade promote preflight \
  --mode dev-to-test \
  --deploys "app,infra" \
  --rollback-on-failure
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--mode` | string | `default` | `default` or cascade target (e.g., `dev-to-prod`) |
| `--force` | bool | false | Continue on failure (default mode only) |
| `--allow-breaking` | bool | false | Allow breaking changes past the prerelease boundary |
| `--deploys` | string | `all` | Deploys to promote (comma-separated names or `all`) |
| `--rollback-on-failure` | bool | true | Revert successful deploys if any fails |
| `--allow-downgrade` | bool | false | Permit promoting an older version onto an env (a downgrade). Blocked by default; prod always requires this flag |

A promotion that would place a strictly older semver version onto an env than the version
it currently holds is a downgrade. Preflight blocks it by default, naming both versions and
the env. Pass `--allow-downgrade` to permit it. The terminal (prod) env always requires the
flag, even when a lower env in the same cascade already permitted the same downgrade. Equal
versions are an idempotent re-promote and are never treated as a downgrade. When either
version is not parseable as semver the gate fails open with a warning rather than blocking,
so non-semver pipelines keep working.

Output:

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
  --run-id "$RUN_ID" \
  --commit-push
```

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--promotion-result` | string | Yes | JSON from `preflight` output |
| `--repo` | string | No | Repository (`owner/name`) for job query |
| `--run-id` | string | No | Workflow run id for job query |
| `--commit-push` | bool | No | Commit and push state changes |

### hotfix

Apply one or more trunk commits onto an environment pinned to an older base. A hotfix
elevates the commit set bottom-up across the environment chain, up to and including the
target environment, on each environment's `env/<env>` integration branch. The fixes must
already be on trunk; cascade refuses to apply any commit that is not an ancestor of trunk
tip. The subcommands compute and validate the hotfix and write its final state; the
cherry-pick and build run in the generated `cascade-hotfix.yaml` workflow, whose deploy
and rollback jobs are placeholders that do not yet invoke the configured deploy workflow.
See [Run a hotfix](/cascade/guides/hotfix/) for the full flow and the limitation.

#### hotfix plan

Validate a hotfix request and compute the integration-branch plan. It enforces, in order:
trunk ancestry of every fix, target-environment eligibility (a configured environment that
is not the first; prod is allowed), no-op detection when a fix is already present, the
single-flight open-pull-request gate, and `env/<env>` branch reconciliation. With
`--dry-run` nothing is mutated (the env branches are planned but not created).

Supply the fixes with one of two mutually exclusive flags, exactly one of which is
required:

- `--commit <sha>` applies a single commit to the target environment.
- `--commits <sha,sha,...>` takes a comma-delimited set and elevates it bottom-up across
  the chain, from the environment above the first up to and including `--target-env`. On
  this path each (commit, environment) pair is skipped when the commit is already an
  ancestor of that environment's state SHA or already in its recorded `patches`; an
  environment whose whole set is already present is a no-op and the chain moves on.

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

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config`, `-c` | string | No | Path to manifest file (default `.github/manifest.yaml`) |
| `--key` | string | No | Top-level manifest key (default `ci`) |
| `--commit` | string | One of `commit`/`commits` | Single trunk commit (SHA or ref) carrying the fix; single-env path |
| `--commits` | string | One of `commit`/`commits` | Comma-delimited trunk commits to elevate across the env chain up to `--target-env` |
| `--target-env` | string | Yes | Environment to hotfix |
| `--actor` | string | No | Actor recorded on the plan (default `$GITHUB_ACTOR`) |
| `--remote` | string | No | Git remote env branches live on (default `origin`) |
| `--repo` | string | No | `owner/repo` for single-flight pull-request lookup via `gh` (default: skip the check) |
| `--dry-run` | bool | No | Compute the plan without mutating anything |
| `--json` | bool | No | Output the plan as JSON |
| `--gha-output` | bool | No | Write outputs to `$GITHUB_OUTPUT` for workflow consumption |
| `--component` | string | No | Declared component to scope the hotfix to (default: single-component manifest) |

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

The GHA output writes `target_env`, `fix_sha`, `branch`, `base_sha`, `no_op`,
`branch_created`, `hotfix_version_candidate`, `conflict_expected`, `dry_run`, and the
`protection_suggestions` commands (as JSON and as multiline text). On the `--commits` path
the plan also writes `env_sequence` (the environments to walk bottom-up) and a
`commits_<env>` list per environment that the apply job replays in order.

#### hotfix finalize

Write the diverged state, tag, and release for a merged hotfix. Run after the resolution
pull request merges and the build and deploy succeed. It cross-checks the merge SHA against
the `env/<target>` branch tip, allocates the next free hotfix version, snapshots the prior
state into the rollback ring, writes the divergence fields and substates, commits the
manifest to trunk with the rebase-retry push, and creates the hotfix tag and release
object. The verb is idempotent on identical inputs: a rerun after the state already records
the merge SHA is a no-op.

```bash
cascade hotfix finalize \
  --target-env test \
  --merge-sha 1234abc \
  --fix-sha abc1234 \
  --base-sha def5678 \
  --build-result app=success \
  --deploy-result app=success
```

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config`, `-c` | string | No | Path to manifest file (default `.github/manifest.yaml`) |
| `--key` | string | No | Top-level manifest key (default `ci`) |
| `--target-env` | string | Yes | Environment to finalize |
| `--merge-sha` | string | Yes | Tip of `env/<target>` after the resolution pull request merged |
| `--fix-sha` | string | Yes | Trunk commit(s) the hotfix carries; comma-delimited for a multi-commit set. Every commit applied to the environment is appended to its recorded `patches` (commits already present in that environment are skipped) |
| `--base-sha` | string | Yes | Trunk anchor the integration branch diverged from |
| `--actor` | string | No | Actor recorded on the state (default `$GITHUB_ACTOR`) |
| `--dry-run` | bool | No | Validate and compute without writing state, tags, or releases |
| `--build-result` | string | No | Build result as `name=result` (repeatable) |
| `--deploy-result` | string | No | Deploy result as `name=result` (repeatable) |
| `--component` | string | No | Declared component to scope the hotfix to (default: single-component manifest) |

Only successful build and deploy results update the per-build and per-deploy substates. For
a prerelease-environment target the hotfix release is promoted to a GitHub prerelease,
superseding that environment's current prerelease object; for other environments it stays a
draft.

### rollback

Re-promote a prior known-good version or SHA to an environment. Rollback resolves the
target from existing deployment state and, when needed, the git history of the manifest,
then re-applies that SHA and version using the same state-write path as a normal promotion.
There is no separate deploy code path. For the operator recipe see
[Roll back an environment](/cascade/guides/rollback/).

`rollback` has a flat operator form plus two subcommands, `preflight` and `finalize`, that
the generated rollback workflow drives: a read-only preflight that resolves the target, and
a finalize that applies the state write and pushes it back to trunk.

Resolution order for `--to <version|sha>`:

1. The environment's current recorded state (and per-deployable state).
2. The environment's deploy-history ring, newest first.
3. The manifest's git history, newest first (recovers a deployment the manifest has already
   moved past).

When `--to` is omitted, rollback resolves the previous version: the newest deploy-history
ring entry that differs from the current state, falling back to the newest distinct prior
state from manifest history. A SHA may be given in full or as a short prefix of 7 or more
characters. Use `--deployable` to scope the rollback to a single deployable's recorded
version.

Without `--dry-run` the flat form writes the re-promotion to the manifest; with `--dry-run`
it prints the resolved plan and makes no changes.

```bash
cascade rollback --env prod --to v1.2.2
cascade rollback --env prod --to 111aaa --dry-run
cascade rollback --env prod --to v1.2.2 --deployable services
```

#### First-environment guard

The first environment in the chain (the build target) tracks trunk and is never promoted
into, so its deploy-history ring is structurally empty and it has no prior target to
resolve. Rollback fails fast on the first environment rather than resolve a silent wrong
target from an empty or stale ring. The trunk-native undo for the first environment is a
revert merge to the trunk branch, not a rollback. The guard is inert when no parsed config
is available to identify the first environment, so a state-only manifest still resolves
through the normal path.

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file (default `.github/manifest.yaml`) |
| `--key` | string | `ci` | Top-level manifest key |
| `--env` | string | - | Target environment to roll back (required) |
| `--to` | string | previous version | Prior version or SHA to re-promote (defaults to the previous version) |
| `--deployable` | string | - | Scope the rollback to a single deployable |
| `--actor` | string | `$GITHUB_ACTOR` | Actor recorded on the rollback |
| `--dry-run` | bool | false | Resolve and print the plan without modifying the manifest |
| `--json` | bool | false | Output the resolved plan as JSON |
| `--component` | string | - | Scope the rollback to a declared component (reads and records `state.components.<component>.<env>`) |

#### rollback preflight

Resolve the rollback target read-only and report it. This is the first stage of the
generated rollback workflow. It resolves the target SHA and version (from live state, the
deploy-history ring, or manifest history) and, with `--gha-output`, writes `target_env`,
`target_sha`, `target_version`, `target_source`, and `can_proceed` to `$GITHUB_OUTPUT` for
the deploy and finalize jobs. It writes no manifest state.

```bash
cascade rollback preflight --env prod --gha-output
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file (default `.github/manifest.yaml`) |
| `--key` | string | `ci` | Top-level manifest key |
| `--env` | string | - | Target environment to roll back (required) |
| `--to` | string | previous version | Prior version or SHA to re-promote (defaults to the previous version) |
| `--deployable` | string | - | Scope the rollback to a single deployable |
| `--gha-output` | bool | false | Write the resolved target to `$GITHUB_OUTPUT` |
| `--json` | bool | false | Print the resolved plan as JSON |
| `--component` | string | - | Scope the rollback to a declared component |

#### rollback finalize

Apply a resolved rollback and persist the updated manifest. This is the final stage of the
generated rollback workflow. It resolves the target, applies it to state (marking the
environment diverged so forward-promotion guards treat it as off-trunk until a promotion
rejoins it), and, with `--commit-push`, commits the manifest back to the trunk branch. The
state write is gated on the reported deploy results: if any in-scope deploy reports
`failure` or `cancelled`, or no in-scope deploy succeeded, the write is aborted and trunk
state is left unchanged.

```bash
cascade rollback finalize --env prod --commit-push
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file (default `.github/manifest.yaml`) |
| `--key` | string | `ci` | Top-level manifest key |
| `--env` | string | - | Target environment to roll back (required) |
| `--to` | string | previous version | Prior version or SHA to re-promote (defaults to the previous version) |
| `--deployable` | string | - | Scope the rollback to a single deployable |
| `--actor` | string | `$GITHUB_ACTOR` | Actor recorded on the rollback |
| `--commit-push` | bool | false | Commit and push the updated manifest to the trunk branch |
| `--component` | string | - | Scope the rollback to a declared component |

### next-version

Calculate the next semantic version.

```bash
cascade next-version \
  --environment prod \
  --base-sha abc123 \
  --head-sha def456
```

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--config`, `-c` | string | No | Path to manifest file |
| `--environment`, `-e` | string | Yes | Target environment |
| `--base-sha` | string | No | Base SHA (defaults to next env's SHA) |
| `--head-sha` | string | No | Head SHA (defaults to HEAD) |
| `--json` | bool | No | Output as JSON |
| `--component` | string | No | Declared component to scope the version to (multi-component manifests) |

Bump rules:

- Breaking change (`feat!`, `BREAKING CHANGE:`) triggers a major bump.
- Feature (`feat`) triggers a minor bump.
- Fix (`fix`) triggers a patch bump.
- Pre-release environments append an RC suffix (e.g., `v1.3.0-rc.0`).

`next-version` resolves [`tag_grammar`](/cascade/reference/manifest/#tag_grammar) from the
manifest and formats the calculated version under that grammar, so its output matches what
`orchestrate` cuts and what the generated release workflow publishes. With no `tag_grammar`
block the output keeps the historical `rc.N` shape shown above. A manifest with a custom
`prerelease_token` and `prerelease_separator`:

```yaml
ci:
  config:
    tag_grammar:
      prerelease_token: pre
      prerelease_separator: ""
```

changes the emitted shape:

```bash
cascade next-version --environment prod --base-sha abc123 --head-sha def456
# v1.3.0-pre0
```

### generate-changelog

Generate a markdown changelog from conventional commits.

```bash
cascade generate-changelog \
  --base-sha abc123 \
  --head-sha def456 \
  --repo owner/repo
```

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--base-sha` | string | Yes | Base commit SHA |
| `--head-sha` | string | Yes | Head commit SHA |
| `--repo` | string | Yes | Repository (`owner/repo`) |
| `--exclude-paths` | string | No | Comma-separated paths to exclude |
| `--contributors` | bool | No | Include contributors section |

Output:

```json
{
  "changelog": "### Features\n\n- Add user authentication ...",
  "has_breaking": false,
  "has_features": true,
  "has_fixes": true
}
```

Conventional-commit mapping:

| Type | Category | Included |
|------|----------|----------|
| feat | Features | Yes |
| fix | Bug Fixes | Yes |
| perf | Performance | Yes |
| docs / chore / ci / test / style / refactor | Routine | No |

Breaking changes are detected via:

- `!` suffix: `feat!: breaking change`
- Footer: `BREAKING CHANGE: description` (case-sensitive, line start)

### manage-release

Manage GitHub releases.

```bash
cascade manage-release \
  --action create \
  --repo owner/repo \
  --tag v1.2.0 \
  --changelog "Release notes here"
```

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
| `--component` | string | No | Declared component to scope RC-tag reaping to (multi-component manifests) |

Actions:

| Action | Description |
|--------|-------------|
| `create` | Create a new release |
| `update` | Update an existing release |
| `lock` | Mark as pre-release |
| `prerelease` | Re-tag a draft RC as a non-draft pre-release |
| `publish` | Finalize a release (drops the RC suffix) |
| `delete` | Delete a release |

Every action mutates GitHub releases or tags, so the global `--dry-run` flag prints the
resolved action and tag plan and issues no API request.

### external

Commands for multi-repo orchestration. `--config`, `--manifest-key`, and `--gha-output` are
persistent flags on the `external` parent command itself, so they apply to every
subcommand. See [Coordinate multiple repos](/cascade/guides/multi-repo/) for the workflow.

#### external persistent flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config` | string | - | Path to manifest file |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--gha-output` | bool | false | Write outputs to `$GITHUB_OUTPUT` |

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

| Flag | Type | Required | Description |
|------|------|----------|-------------|
| `--source-repo` | string | Yes | Source repository (e.g., `org/cdk-infra`) |
| `--deploy-name` | string | Yes | Deploy name |
| `--environment` | string | Yes | Target environment |
| `--sha` | string | Yes | Commit SHA from source repo |
| `--version` | string | No | Version from source repo |
| `--artifacts` | string | No | Artifacts JSON |
| `--dry-run` | bool | No | Preview mode |

This is typically called by the satellite's `external-update.yaml` workflow after deploying
to dev.

### schema

Print the manifest JSON Schema. Point your editor at it for autocomplete, type checking,
and hover docs while authoring `.github/manifest.yaml`. See the
[Manifest reference](/cascade/reference/manifest/) for editor registration.

```bash
# Print the schema to stdout
cascade schema

# Write the schema to a file
cascade schema --output manifest.schema.json
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--output`, `-o` | string | stdout | Write the schema to a file instead of stdout |

The same schema is published at
`https://stablekernel.github.io/cascade/manifest.schema.json`. `lint` remains the
authority for semantic and cross-field rules; the schema covers structure, types, enums,
and hover docs.

### environments

Emit a per-environment configuration file an operator applies to GitHub's Environments REST
API. cascade emits the file; the operator applies it. cascade never calls the GitHub API.

```bash
cascade environments
```

The output is a wrapper. The top-level `environments` is an array with one entry per
manifest environment, in the manifest's `environments` order. Each entry has:

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

The per-environment settings come from the inline fields on each
`config.environments` entry:

```yaml
config:
  environments:
    - dev
    - test
    - name: prod
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
- `deployment_branch_policy`, mapped from the manifest `branch_policy`: `protected` becomes
  `{protected_branches: true, custom_branch_policies: false}`; `custom` becomes
  `{protected_branches: false, custom_branch_policies: true}`; `all` or unset becomes
  `null`, meaning all branches.

Everything else cascade cannot fully form from the manifest is surfaced under
`operator_todo` so the operator can finish it:

- `operator_todo.required_reviewers` lists user and team slugs, NOT the body. The REST API
  requires a numeric reviewer id that the manifest does not carry, so the operator resolves
  each slug to an id and adds it to the body's `reviewers` array.
- `operator_todo.secrets` and `operator_todo.variables` list the expected env-scoped secret
  and variable names. cascade emits names only, never values; the operator creates them
  with values through the environment-secrets and environment-variables APIs.
- `branch_patterns` and `tag_patterns` (custom policy only) are created through the
  deployment-branch-policies API and are surfaced under `operator_todo`.

The output is deterministic: the same manifest yields byte-identical output, and
environments follow the manifest order. This is the sibling of the `branch-protection`
command, using the same emit-a-config-file pattern (operator applies; cascade never calls
the API). See [Add or change environments](/cascade/guides/environments/) for the
task-oriented walkthrough.

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--output`, `-o` | string | stdout | Write to this path instead of stdout (`-` also means stdout) |

### branch-protection

Produce the branch-protection settings for a cascade-managed trunk. The command has two
modes. By default it emits the JSON body for an operator to apply and makes no API call.
Pass `--apply` and cascade PUTs the body to GitHub's branch-protection API for you using a
caller-supplied scoped token.

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

#### Applying directly with `--apply`

Instead of piping the JSON to `gh`, pass `--apply` and cascade sends the PUT itself. It
transmits only the `.protection` object; the `operator_todo` guidance is never part of the
request. The default emit behavior is unchanged: without `--apply` cascade still only
prints or writes the JSON.

```bash
cascade branch-protection --apply --repo my-org/my-app --branch main
```

With `--apply`, `--branch` is the real protection target rather than just a label on the
guidance note. cascade resolves the target repository from `--repo`, falling back to the
`GITHUB_REPOSITORY` environment variable, and the REST API base from `--api-url`, falling
back to `GITHUB_API_URL` and then `https://api.github.com`. A missing token or repository
is reported before any network call, and a non-2xx response from GitHub (for example a
`403` from an under-scoped token) is surfaced with GitHub's own rejection message.

Combine `--apply` with `--dry-run` to preview the exact PUT target without sending the
request; a token is not required to preview.

Applying branch protection requires a token with repo-admin authority (the
`Administration: write` permission). The workflow `GITHUB_TOKEN` does not carry that
authority, so `--apply` needs a scoped personal access token supplied through `--token` or
the `GITHUB_TOKEN` environment variable. Prefer the environment variable over the flag so
the token stays out of process arguments and shell history.

```bash
GITHUB_TOKEN=ghp_your_admin_pat \
  cascade branch-protection --apply --repo my-org/my-app --branch main
```

You can also pass `--output` alongside `--apply` to keep the emitted JSON on disk for your
records; cascade writes the file first and then applies.

#### What ends up required, and why it is safe

The required status checks contain only the cascade-controlled `Setup` and `Finalize` jobs.
These are the orchestrate workflow's two steps jobs; cascade knows their exact check-run
names and both run on every pipeline run. Because of that, `.protection` applied as-is never
creates a required check that can never report, so it never blocks a pull request on its
own.

The reusable-workflow caller jobs (validate, build, deploy) are deliberately left out of
the required contexts. cascade knows each caller's display-name prefix (for example
`Build (my-app)`) but not the inner job name that GitHub appends to form the real check-run
context, which is `<DisplayName> / <inner-job>`. That inner job lives in your reusable
workflow, which cascade does not author. Requiring a bare prefix would never match and would
block every pull request, so cascade lists those prefixes under
`operator_todo.complete_these_contexts` as `<DisplayName> / <inner-job>` placeholders
instead. Replace `<inner-job>` with the job name inside each reusable workflow, then add the
completed strings to `required_status_checks.contexts` when you want them required.

In the default emit mode the `--branch` flag only labels the guidance note (with `--apply`
it is the real apply target, as described above). Either way the required contexts are the
same across branches and environments because they are the orchestrate-workflow steps jobs,
so `--env` would not change them and is not offered.

This command complements the hotfix branch-protection advisory (see
[Run a hotfix](/cascade/guides/hotfix/)): the advisory prints ready-to-run `gh` commands for
env branches, while `branch-protection` emits the full PUT body for the trunk.

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | auto-detect | Path to manifest file |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--branch` | string | `main` | Branch the protection targets; with `--apply` this is the real apply target, otherwise it labels the guidance note only and does not change the required contexts |
| `--output`, `-o` | string | stdout | Write to this path instead of stdout (`-` also means stdout); honored alongside `--apply` to keep the JSON for your records |
| `--apply` | bool | `false` | PUT the `.protection` body to GitHub instead of emitting JSON (requires a repo-admin token) |
| `--token` | string | `GITHUB_TOKEN` | Scoped repo-admin token used for `--apply`; falls back to the `GITHUB_TOKEN` environment variable |
| `--repo` | string | `GITHUB_REPOSITORY` | `owner/repo` the apply targets; falls back to the `GITHUB_REPOSITORY` environment variable |
| `--api-url` | string | `GITHUB_API_URL` | REST API base for `--apply`; falls back to `GITHUB_API_URL`, then `https://api.github.com` |

### reconcile

Adopt an external governed action-pin change (for example a Dependabot bump landing in a
generated workflow) back into the manifest's `action_pins`, then regenerate every workflow
the manifest produces so cascade's owned output agrees with it again. `reconcile` never
pushes, commits, or merges; wiring a CI job (or running it by hand) to drive it, and to
commit and push its result, stays the caller's job.

```bash
cascade reconcile --changed-file .github/workflows/orchestrate.yaml
```

`reconcile` reads the files named by `--changed-file` (repeatable) as data, scanning each
line by line for a governed `uses:` reference, plus the manifest itself. It never reads a
pin back out of a file the manifest generates: every generated file is exclusively a
regenerate target, so a run that touches nothing relevant is a safe no-op and generation
stays a pure offline function of the manifest. When a changed file carries a bump for an
action cascade governs, `reconcile` writes that ref verbatim into the manifest's
`action_pins`, keyed by action path, and regenerates. See the
[Manifest reference](/cascade/reference/manifest/) for what that write looks like under
`pin_mode: tag` and `pin_mode: sha`.

`reconcile` has three modes, selected by flag:

| Mode | Flag | Behavior |
|------|------|----------|
| Default | (none) | Reconciles a user repo's manifest: adopts the bump into `action_pins` and regenerates. |
| Detector | `--check` | Read-only: reports whether a governed pin changed and writes a data-only JSON artifact (`--check-output`) naming the changed refs; writes nothing else. |
| Own-repo | `--own-repo` | Reconciles cascade's own `action_pins.yaml` manifest (a full re-marshal, since cascade owns that file) rather than a user manifest, and regenerates. |

`--check` and `--own-repo` are mutually exclusive: the detector is read-only, and own-repo
mode is a second write target, so combining them is rejected.

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config`, `-c` | string | `<root>/.github/manifest.yaml` | Path to the manifest file |
| `--manifest-key` | string | `ci` | Top-level key inside the manifest |
| `--root` | string | `.` | Repository root `reconcile` scans and writes relative to |
| `--changed-file` | string (repeatable) | - | A changed source file to scan for a governed pin bump |
| `--check` | bool | false | Read-only detector mode: report relevance and write a JSON artifact |
| `--check-output` | string | `pin-reconcile-result.json` | Path to write the check-mode JSON artifact |
| `--own-repo` | bool | false | Reconcile cascade's own `action_pins.yaml` manifest |
| `--action-pins` | string | - | Path to `action_pins.yaml` (own-repo mode) |

### reset

Reset releases and state for testing.

```bash
cascade reset --state --push
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--state` | bool | false | Reset the state section in the manifest |
| `--dry-run` | bool | false | Preview without executing |
| `--push` | bool | false | Push state changes (requires `--state`) |
| `--repo` | string | cwd | Path to repository |
| `--config` | string | auto-detect | Path to manifest file |
| `--manifest-key` | string | `ci` | Top-level key |

Deletes all GitHub releases and tags. With `--state`, also clears the state section.

## Environment variables

cascade reads the following environment variables. It reads no `LOG_LEVEL` (verbosity is
controlled by the `--trace` and `--json` flags) and no `GITHUB_RUN_ID` (promote finalize
takes `--run-id` as an explicit flag).

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | GitHub API token for releases and other API calls |
| `GITHUB_ACTOR` | Default actor for commits and promotions |
| `GITHUB_REPOSITORY` | `owner/repo`, used as a fallback target for API-calling commands |
| `GITHUB_API_URL` | REST API base URL; falls back to `https://api.github.com` |
| `GITHUB_SERVER_URL` | GitHub server base URL (for example on GitHub Enterprise) |
| `GH_TOKEN` | Alternative token honored where a GitHub token is expected |
| `RELEASE_TOKEN` | Token used for release and state-write operations that need elevated scope |
| `NO_COLOR` | Disable colored output when set |

## Exit codes

cascade uses three process exit codes.

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error (the default for any failure) |
| `2` | Operational failure returned by `verify` |

Code `2` is owned by `verify`: it returns an error that carries an explicit exit code so
callers can distinguish an operational failure (a missing or invalid manifest) from drift,
which exits `1`. Every other command that fails returns the default exit code `1`. This
matches the exit-code table in the [`verify`](#verify) section above.

## JSON output

Many commands accept `--json` (or `--gha-output` inside Actions) for machine-readable output; `detect-changes` always writes its JSON result to stdout, so a step can capture it directly:

```yaml
- name: Detect changes
  id: detect
  run: |
    cascade detect-changes \
      --config .github/manifest.yaml \
      --base-sha "${{ github.event.before }}" \
      --head-sha "${{ github.sha }}"
```

## Debugging

Raise log verbosity with the global `--trace` flag:

```bash
cascade --trace lint --config .github/manifest.yaml
```

Trace logs include:

- File matching details
- Dependency resolution steps
- API request and response info

## Wayfinding

- **Prerequisite**: [Getting started](/cascade/start/getting-started/). Install cascade
  and generate your first pipeline.
- **Next**: [Manifest reference](/cascade/reference/manifest/). Every manifest field the
  commands above read.
