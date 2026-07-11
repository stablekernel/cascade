---
title: Promote a release
description: Trigger a promotion, pick a mode, and know what to watch as it advances an environment.
---

Promotion moves a build already validated in one environment into the next one, without rebuilding it. It runs as the generated `promote.yaml` workflow; see [Promote workflow anatomy](/cascade/reference/generated-workflows/#promote-workflow-anatomy) for the job-by-job structure.

## Trigger a promotion

Dispatch it from the Actions tab or the CLI:

```bash
gh workflow run promote.yaml -f mode=default
```

With no other inputs, `mode=default` advances the chain by exactly one step: the next environment, or the release stage at the top of the chain.

## Promote modes and gates

`mode` is a dropdown generated from your `environments` list, in position order (last = release stage, second-to-last = prerelease stage):

| Mode | Behavior |
|------|----------|
| `default` | Advance one logical step from the environment currently ahead. |
| `<from>-to-<to>` (for example `dev-to-prod`) | Cascade through every environment between `from` and `to`, deploying and finalizing each one in turn. |

A breaking-change gate sits at the prerelease-to-release boundary regardless of mode. If the commits being promoted include a breaking change, the run stops there unless you pass `allow_breaking_changes: true`. A repository can turn the gate off for good by setting [`allow_breaking_changes: true`](/cascade/reference/manifest/#allow_breaking_changes) in the manifest config, so the per-run input is not needed.

| Input | Type | Default | Purpose |
|-------|------|---------|---------|
| `mode` | choice | `default` | See above. |
| `force` | boolean | `false` | Continue past a failed step (default mode only). |
| `allow_breaking_changes` | boolean | `false` | Required to cross the prerelease-to-release boundary with a breaking change. |
| `dry_run` | boolean | `false` | Resolve and print the plan; skip deploys and state writes. |
| `deploys` | string | `all` | See [Selective deploys](#selective-deploys). |
| `rollback_on_failure` | boolean | `true` | See below. |
| `allow_downgrade` | boolean | `false` | Required to promote a build whose version is lower than the target's current version; the release stage always requires it. |

## Atomic promotion and rollback-on-failure

With `rollback_on_failure: true` (the default), a promotion is all-or-nothing. Preflight records the target environment's current SHA as `rollback_sha`; if any deploy job fails, the deploys that already succeeded are rolled back to that SHA. Either every deploy lands or none does. Set it to `false` for a non-atomic promotion that leaves whatever succeeded in place.

This is a different knob from `rollout.fail_fast` and `rollout.max_parallel` on a `deploys[].rollout` entry in the manifest. Those two control the GitHub Actions `strategy:` block on that one deploy's matrix job (whether one failed matrix leg cancels the rest, and how many legs run in parallel); `rollback_on_failure` controls what promote does across deploys after a failure. See [the deploy strategy block](/cascade/reference/generated-workflows/#the-deploy-strategy-block) for the manifest-to-YAML mapping. Everything else under `rollout` (`type`, `canary`, `blue_green`) is reserved and has no effect on generated output.

## Selective deploys

Use `deploys` to limit which deployables run:

```yaml
deploys: "app,infra"   # only these two
deploys: "all"         # default: every configured deploy
```

Promote also skips a deploy on its own when there is nothing to do: it compares the target's last-deployed SHA per deployable against the source SHA and runs only the ones with real changes in their trigger paths. Selective deploys and this per-deployable change detection combine, so `deploys: "app,infra"` still skips `infra` if nothing under its triggers changed.

## Publish and version determination

When the manifest has a `publish:` callback, crossing the prerelease-to-release boundary adds a publish step once per configured build. See [Publish](/cascade/reference/generated-workflows/#publish) for the exact dispatch payload.

For the release stage, version is the latest semver tag auto-incremented from conventional commits since that tag: major for a breaking change, minor for a feature, patch for a fix. There is no manual override input; the commit history determines the bump. The prerelease suffix (`-rc.N` by default; configurable via [`tag_grammar`](/cascade/reference/manifest/#tag_grammar)) is dropped at this boundary.

## What to watch

- **Preflight** fails fast on a bad source/target pair, a non-ancestor SHA, or the breaking-change gate; the run log names which one.
- **Deploy jobs** run as a matrix; check each deployable's own job for its callback's output, not just the aggregate status.
- **Rollback jobs** only appear when `rollback_on_failure: true` and a deploy actually failed; their presence in the run means something needed reverting.
- **Finalize** is where state, changelog, and release actually get written. A promotion that finishes deploy but fails finalize has redeployed without updating recorded state; rerun finalize rather than the whole promotion.

## Wayfinding

**Prerequisite:** [Getting started](/cascade/start/getting-started/) for a running pipeline with at least one promotable environment.

**Next:** [Run a hotfix](/cascade/guides/hotfix/) for the case a normal promotion cannot serve: patching one diverged environment without dragging in every intervening trunk commit.
