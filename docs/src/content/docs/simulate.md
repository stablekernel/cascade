---
title: Local Simulation
description: Preview a promotion, release, rollback, or hotfix locally with cascade simulate. What the what-if engine validates, the before/after state diff and effect sequence it prints, how deploy stubs and outcome injection work, and where its scope ends.
---

`cascade simulate` answers one question before you trigger a real pipeline: if I take this action against the manifest as it stands today, what would cascade do? It replays the same orchestration logic the live workflows use, in record-only mode, and prints a before/after state diff plus the ordered sequence of steps the orchestration would take. Nothing runs. No GitHub call is made, no container starts, no git command executes, and your manifest is never modified.

Use it to sanity-check a promotion target, to see which environment a rollback would land on, to confirm a hotfix allocates the version you expect, or to preview how a failed deploy would gate the rest of the run.

## What it validates, and what it does not

This is the most important thing to understand about the tool, so read it before you trust a result.

`cascade simulate` validates cascade's **orchestration**: the state transitions, and the run, skip, and gate decisions that move an artifact through your environments and across the prerelease/release boundary. It does **not** run your build and deploy scripts. Those workflows never execute in a simulation.

A green simulation therefore means the orchestration would sequence correctly given the inputs you supplied. It is not a test that your deploy actually works. Build and deploy callbacks are recorded as stubbed steps with a simulated outcome (success by default), so the orchestration sequences exactly as it would if those callbacks had returned that outcome. To exercise your real scripts you still need the live pipeline, or the integration simulator described at the end of this page.

Every simulation prints this boundary as a closing note so it stays in view:

```
Note: build and deploy results are simulated, not executed. cascade validates orchestration, not your build and deploy scripts.
```

## How it runs

The engine reads your manifest, copies it to a temporary file, computes the hypothetical transition against that copy, and discards the copy. The original bytes are untouched. The computation is deterministic: environment keys are sorted and run-stamped timestamps are excluded, so the same inputs always produce the same report.

By default the manifest is auto-detected at `.github/manifest.yaml`. Point at another file with `--config`.

```bash
cascade simulate <action> [flags]
```

The four actions are `promote`, `release`, `rollback`, and `hotfix`.

## Reading the output

Every action prints two parts.

**State diff** shows what would change in the manifest state, per environment. Each line names a field and its before and after values, for example `version: (none) -> v1.2.0-rc.1`. When nothing would change, the diff reads `(no state change)`.

**Effects (in order)** is the ordered sequence of steps the orchestration would take. Each step carries a disposition in brackets:

| Disposition | Meaning |
|-------------|---------|
| `run` | The orchestration would carry this step out. |
| `skip` | The orchestration would skip this step as a no-op. |
| `gate` | The step is held back behind a gate or guard, for example a finalize blocked by a failed deploy. |

## Promote

Preview moving the current artifact one environment forward, or, in cascade mode, through every intermediate hop to a target.

```bash
cascade simulate promote
cascade simulate promote --mode cascade --target dev-to-prod
```

Against a chain of `dev -> uat -> prod` where `dev` holds `v1.2.0-rc.1` and `uat` is empty:

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

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--mode` | `default` | Promotion mode: `default` advances one environment, `cascade` carries state through every hop to the target. |
| `--target` | (none) | Cascade target, for example `dev-to-prod`. |

## Release

Preview crossing the prerelease/release boundary. The report includes the prerelease or publish marker the orchestration would emit, which is the headline decision at this stage.

```bash
cascade simulate release
```

For a library or CLI project with no environments, where `prerelease` holds `v1.0.0-rc.0`, the crossing publishes:

```
Simulating: release (prerelease/publish crossing)
State diff:
  prerelease:
    version: v1.0.0-rc.0 -> (none)
    sha:     a1b2c3d4e5f6 -> (none)
  release:
    version: (none) -> v1.0.0
    sha:     (none) -> a1b2c3d4e5f6
Effects (in order):
  1. [run] write state release (sha a1b2c3d, version v1.0.0)
  2. [run] release publish v1.0.0 (rc v1.0.0-rc.0, sha a1b2c3d)

Note: build and deploy results are simulated, not executed. cascade validates orchestration, not your build and deploy scripts.
```

`release` takes no action-specific flags beyond the shared ones below.

## Rollback

Preview reverting an environment to a prior state. Target resolution is pinned to the in-state deploy-history ring, so the simulation never reads a git repository. With no `--to`, the engine resolves the previous distinct state from that ring.

```bash
cascade simulate rollback --env prod
cascade simulate rollback --env prod --to v1.0.0
```

For a `prod` env currently on `v2.0.0` with one prior ring snapshot at `v1.0.0`:

```
Simulating: rollback (env=prod, to=previous)
State diff:
  prod:
    version: v2.0.0 -> v1.0.0
    sha:     newsha0000000 -> oldsha0000000
    divergence: no -> yes
    previous ring: 1 -> 2
Effects (in order):
  1. [run] revert prod (to sha oldsha0, version v1.0.0 (from previous-ring))
  2. [run] write state prod (sha oldsha0, version v1.0.0)

Note: build and deploy results are simulated, not executed. cascade validates orchestration, not your build and deploy scripts.
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--env` | (required) | Environment to roll back. |
| `--to` | previous distinct state | Target SHA or version. |
| `--deployable` | (none) | Scope the rollback to a single deployable. |

## Hotfix

Preview applying one or more trunk commits as a hotfix to an environment. The engine allocates the next hotfix version, snapshots the prior state into the ring, and writes the divergence fields.

```bash
cascade simulate hotfix --env uat --fix fixaaa1110000
cascade simulate hotfix --env uat --fix fixaaa1110000,fixbbb2220000 --merge-sha mergesha00000
```

For a `uat` env on `v1.0.0-rc.1` carrying a single fix commit:

```
Simulating: hotfix (env=uat, commits=1)
State diff:
  uat:
    version: v1.0.0-rc.1 -> v1.0.0-rc.1.hotfix.1
    sha:     basesha000000 -> fixaaa1110000
    divergence: no -> yes
    previous ring: 0 -> 1
Effects (in order):
  1. [run] apply patch uat (commit fixaaa1)
  2. [run] write state uat (diverge env onto integration branch)
  3. [run] release create uat (tag v1.0.0-rc.1.hotfix.1)
  4. [run] release prerelease uat (tag v1.0.0-rc.1.hotfix.1)

Note: build and deploy results are simulated, not executed. cascade validates orchestration, not your build and deploy scripts.
```

Each carried commit yields its own `apply patch` step. With multiple commits the environment advances to `--merge-sha`, the resolution-branch tip, defaulting to the first fix commit when omitted.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--env` | (required) | Environment to hotfix. |
| `--fix` | (required) | Comma-separated trunk commit SHAs the hotfix carries. |
| `--merge-sha` | first fix commit | Resolution-branch tip the environment advances to. |

## Deploy stubs and outcome injection

Because real callbacks never run, each build and deploy a manifest declares is recorded as a stubbed step instead of an execution. By default every stub resolves to success, so the orchestration sequences as if all callbacks had passed.

To preview gating, inject a per-callback outcome with `--deploy-result name=outcome`, where outcome is `success`, `failure`, or `skipped`. The flag is repeatable, so you can set an outcome for each callback by name. A deploy that did not succeed holds back the simulated finalize, mirroring how the live finalizers refuse to record trunk state when a deploy fails.

With a manifest that declares a `services` deploy, a successful run records the stub and finalizes normally:

```
Effects (in order):
  1. [run] deploy uat (from dev (sha a1b2c3d, version v1.2.0-rc.1))
  2. [run] deploy services (simulated success (not executed))
  3. [run] write state uat (sha a1b2c3d, version v1.2.0-rc.1)
  4. [run] release prerelease v1.2.0 (rc v1.2.0-rc.1, sha a1b2c3d)
  5. [skip] promote prod (no change required)
```

Inject a failure and the finalize is gated:

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

## Shared flags

These flags apply to every action.

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `.github/manifest.yaml` | Path to the manifest file. |
| `--actor` | (none) | Actor performing the hypothetical action. |
| `--deploy-result` | (none) | Simulated outcome for a build or deploy callback, `name=success\|failure\|skipped`. Repeatable. |
| `--json` | `false` | Emit the result as deterministic JSON instead of the human report. |

## JSON output

Pass `--json` for a machine-readable result. The structure carries the action, its description, the full diff, and the effect list, suitable for piping into `jq` or asserting on in a script.

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
        "version": { "field": "version", "from": "(none)", "to": "v1.2.0-rc.1", "changed": true },
        "sha": { "field": "sha", "from": "(none)", "to": "a1b2c3d4e5f6", "changed": true }
      }
    ]
  }
}
```

## A higher-fidelity simulator is planned

`cascade simulate` is the fast, dependency-free way to check orchestration decisions, and it covers most day-to-day what-if questions. It deliberately stops at the orchestration boundary.

A second, higher-fidelity simulator is planned. It will run the generated workflows locally with [act](https://github.com/nektos/act) and a local [Gitea](https://about.gitea.com/) server, exercising the real workflow YAML end to end with stubbed deploys, at the cost of requiring Docker. That work is tracked in the [issue tracker](https://github.com/stablekernel/cascade/issues). Until it lands, reach for the live pipeline when you need to validate the workflows or your deploy scripts themselves.
