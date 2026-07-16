---
title: Simulate and verify
description: Preview a promotion, release, rollback, or hotfix before it runs, then keep committed workflows honest with verify and plan.
---

Three commands let you check a change before or after it lands: `simulate` previews what an orchestration action would do against your manifest, `plan` previews what `generate-workflow` would change on disk, and `verify` fails a CI job when committed workflows drift from what the manifest would generate. All three are read-only.

## Simulate a promotion, release, rollback, or hotfix

`cascade simulate <action>` replays the same orchestration logic the live workflows use, in record-only mode, and prints a before/after state diff plus the ordered sequence of steps the orchestration would take. Nothing runs: no GitHub call, no container, no git command, and the manifest is never modified.

```bash
cascade simulate promote
```

```
Simulating: promote (mode=default)
State diff:
  uat:
    version:  v1.1.0 -> v1.2.0-rc.1
    sha:      a1b2c3d -> 9f8e7d6
    diff:     https://github.com/acme/app/compare/a1b2c3d...9f8e7d6
Effects (in order):
  1. deploy uat from dev
  2. write state uat (sha 9f8e7d6, version v1.2.0-rc.1)
  3. release prerelease v1.2.0 (rc v1.2.0-rc.1, sha 9f8e7d6)
  4. skipped promote prod (no change required)

Note: build and deploy results are simulated, not executed. cascade validates orchestration, not your build and deploy scripts.
```

Here `uat` already held a prior sha, so the diff moves it between two known shas and the `diff:` line links a compare view; a first deploy into an empty environment (`(none) -> <sha>`) has no prior sha to compare, so that line is omitted.

The other three actions follow the same shape:

```bash
cascade simulate release                                   # preview the prerelease/release boundary
cascade simulate rollback --env prod                        # preview reverting an environment
cascade simulate hotfix --env uat --fix <sha>[,<sha>...]     # preview applying trunk commits as a hotfix
```

The state diff shortens every sha to 7 characters, and when a change moves an environment between two known shas it adds a `diff:` line with a compare link. The repository is read from `GITHUB_REPOSITORY` (with `GITHUB_SERVER_URL`, defaulting to `https://github.com`) or, failing that, the `origin` remote of the working directory; when neither identifies a repository, the compare line is omitted.

Each step in the effect list carries a disposition. A `run` step (the orchestration would carry it out) is shown unadorned; a `skipped` step is a no-op; a `gated` step is held back behind a guard, such as a finalize blocked by a failed deploy. `rollback` resolves its target from the in-state deploy-history ring, never from git, so it needs `--env` and optionally `--to`. `hotfix` needs `--env` and `--fix`; with multiple fix commits, add `--merge-sha` to name the resolution-branch tip. Full flag lists for every action live in the [CLI reference](/cascade/reference/cli/).

## Hotfix cherry-pick chain preview

A hotfix into a mid-ladder environment does not touch that one environment alone: the fix elevates bottom-up from the second environment up to and including the target, one cherry-pick per environment along the way, so every environment the target promotes from carries the same commit. `simulate hotfix` previews that ordered chain in a `Cherry-pick chain` section, above the finalize effects for the target:

```bash
cascade simulate hotfix --env prod --fix <sha>
```

```
Simulating: hotfix (env=prod, commits=1)
State diff:
  prod:
    version:        v1.0.0 -> v1.0.1
    sha:            prodbas -> fixaaa1
    diff:           https://github.com/acme/app/compare/prodbas...fixaaa1
    divergence:     no -> yes
    previous ring:  0 -> 1
Cherry-pick chain (in order):
  1. cherry-pick uat (commit fixaaa1)
  2. cherry-pick prod (commit fixaaa1)
Effects (in order):
  1. apply patch prod (commit fixaaa1)
  2. write state prod (diverge env onto integration branch)
  3. release create prod (tag v1.0.1)

Note: build and deploy results are simulated, not executed. cascade validates orchestration, not your build and deploy scripts.
```

The chain lists the target as its last step, and an environment that already carries the fix (recorded in its applied-patch list) reads as `skipped cherry-pick <env> (already present)` rather than a fresh cherry-pick. The preview drives the same planner the live hotfix workflow uses, so the elevation order it shows is the order the workflow would follow, computed with no git and no network. On a multi-component manifest, `--component` scopes the chain to the selected component's environment ladder.

## Deploy stubs and outcome injection

Real build and deploy callbacks never run in a simulation. Each one is recorded as a stubbed step instead, and every stub resolves to `success` by default, so the orchestration sequences as if all callbacks had passed.

To preview gating, inject a per-callback outcome with `--deploy-result name=outcome` (`success`, `failure`, or `skipped`; repeatable):

```bash
cascade simulate promote --deploy-result services=failure
```

```
Effects (in order):
  1. deploy uat from dev
  2. deploy services (simulated failure (not executed))
  3. gated write state uat (deploy "services" simulated failure; trunk state left unchanged)
  4. release prerelease v1.2.0 (rc v1.2.0-rc.1, sha a1b2c3d)
  5. skipped promote prod (no change required)
```

A `skipped` outcome is never a failure, but it does not count as a success either. When every configured deploy is skipped, nothing was deployed, so the finalize still gates.

The same gate applies to `simulate rollback`. A rollback re-deploys a prior SHA, and the real finalize refuses the state write when that deploy did not succeed, so injecting a failed (or all-skipped) outcome shows the `write state` effect gated and leaves the previewed state unchanged. With `--deployable`, only the scoped deploy's outcome is in scope, matching the live gate.

## Multi-component (monorepo) manifests

When a manifest declares `components:`, each component owns an independent state ladder recorded under `state.components.<name>.<env>`, and there are no flat `state.<env>` rows. Scope a simulation to one component with `--component`; the engine reads and replays that component's recorded state through the same path the component-scoped `promote`, `rollback`, and `hotfix` commands use, so the what-if matches the run cascade would actually perform for that component:

```bash
cascade simulate promote --component api
cascade simulate rollback --component web --env prod
```

`--component` is required on a multi-component manifest: without it the simulation would replay against empty state and report only guard messages. The command rejects an omitted or unknown component with a clear error listing the declared names. A sibling component's rows never appear in another component's simulation. On a single-component manifest the flag is left empty and behavior is unchanged.

## `--mode default | cascade`

`promote` is the one action with a mode switch. `default` advances one environment; `cascade` carries state through every intermediate hop to a `--target`:

```bash
cascade simulate promote --mode cascade --target dev-to-prod
```

## JSON output

Pass `--json` on any action for a machine-readable result: the action, its description, the full diff, and the effect list, suitable for piping into `jq` or asserting on in a script.

```bash
cascade simulate promote --json
```

```json
{
  "action": "promote",
  "describe": "promote (mode=default)",
  "diff": {
    "envs": [
      {
        "environment": "uat",
        "version": { "field": "version", "from": "(none)", "to": "v1.2.0-rc.1", "changed": true }
      }
    ]
  }
}
```

## `verify` for drift

Where `simulate` previews an action, `verify` checks state: it compares the committed workflow and action files against what the manifest would generate right now, and reports drift without writing anything.

```bash
cascade verify
```

`verify` covers the complete generated file set (orchestrate, promote or release, external-update, validate-check, merge-queue, hotfix, rollback, pr-preview, drift-check, drift-comment, and the manage-release composite action) and also reports orphans: cascade-owned files left behind after a manifest change removes an environment or build. Pass `--allow-orphans` when stale generated files are expected.

`verify` owns a precise three-way exit contract, mirroring `diff(1)`:

| Exit | Meaning |
|------|---------|
| `0` | No drift. Every generated file is present and byte-identical, and no orphaned generated files remain. |
| `1` | Drift detected. A generated file is missing, its committed bytes differ, or a cascade-owned file is orphaned (unless `--allow-orphans`). |
| `2` | Operational failure. The manifest is missing or invalid, or another failure prevented the check from running at all. |

Wire it into CI as a single step, or set `drift_check.enabled: true` in the manifest and let `generate-workflow` emit the drift-check workflow for you:

```yaml
- run: cascade verify
```

## `plan`

`plan` is the human-facing preview counterpart to `verify`: a per-file unified diff of what `generate-workflow` would change, without writing anything.

```bash
cascade plan
```

A new file appears as a whole-file add, a changed file as a unified hunk, and a file already in sync produces no diff. When nothing is pending, `plan` prints a single `plan: N files, no pending changes` line. Read `plan` at the terminal to see what would change; wire `verify` into CI to enforce that nothing does. `plan` always exits `0` on success, whether or not it printed a diff; a non-zero exit means the preview itself could not run.

## Scope note: simulation fidelity is bounded

`cascade simulate` validates cascade's orchestration, the state transitions and run/skip/gate decisions, not your build and deploy scripts. A green simulation means the orchestration would sequence correctly given the inputs you supplied; it is not a test that your deploy actually works. To exercise your real scripts, use the live pipeline. A higher-fidelity local simulator that runs the generated workflows end to end with [act](https://github.com/nektos/act) and a local [Gitea](https://about.gitea.com/) server is planned but not yet shipped; track it in the [issue tracker](https://github.com/stablekernel/cascade/issues).

## Wayfinding

**Prerequisite**: [Getting started](/cascade/start/getting-started/) to have a manifest and generated workflows in place.
**Next**: [Manifest reference](/cascade/reference/manifest/) for every field these commands read.
