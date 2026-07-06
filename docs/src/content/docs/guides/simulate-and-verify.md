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
    version: (none) -> v1.2.0-rc.1
    sha:     (none) -> a1b2c3d4e5f6
Effects (in order):
  1. [run] deploy uat (from dev (sha a1b2c3d, version v1.2.0-rc.1))
  2. [run] write state uat (sha a1b2c3d, version v1.2.0-rc.1)
  3. [run] release prerelease v1.2.0 (rc v1.2.0-rc.1, sha a1b2c3d)
  4. [skip] promote prod (no change required)

Note: build and deploy results are simulated, not executed. cascade validates orchestration, not your build and deploy scripts.
```

The other three actions follow the same shape:

```bash
cascade simulate release                                   # preview the prerelease/release boundary
cascade simulate rollback --env prod                        # preview reverting an environment
cascade simulate hotfix --env uat --fix <sha>[,<sha>...]     # preview applying trunk commits as a hotfix
```

Each step in the effect list carries a disposition: `run` (the orchestration would carry it out), `skip` (a no-op), or `gate` (held back behind a guard, such as a finalize blocked by a failed deploy). `rollback` resolves its target from the in-state deploy-history ring, never from git, so it needs `--env` and optionally `--to`. `hotfix` needs `--env` and `--fix`; with multiple fix commits, add `--merge-sha` to name the resolution-branch tip. Full flag lists for every action live in the [CLI reference](/cascade/reference/cli/).

## Deploy stubs and outcome injection

Real build and deploy callbacks never run in a simulation. Each one is recorded as a stubbed step instead, and every stub resolves to `success` by default, so the orchestration sequences as if all callbacks had passed.

To preview gating, inject a per-callback outcome with `--deploy-result name=outcome` (`success`, `failure`, or `skipped`; repeatable):

```bash
cascade simulate promote --deploy-result services=failure
```

```
Effects (in order):
  1. [run] deploy uat (from dev (sha a1b2c3d, version v1.2.0-rc.1))
  2. [run] deploy services (simulated failure (not executed))
  3. [gate] write state uat (deploy "services" simulated failure; trunk state left unchanged)
  4. [run] release prerelease v1.2.0 (rc v1.2.0-rc.1, sha a1b2c3d)
  5. [skip] promote prod (no change required)
```

A `skipped` outcome is never a failure, but it does not count as a success either. When every configured deploy is skipped, nothing was deployed, so the finalize still gates.

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
