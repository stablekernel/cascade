---
title: Roll back an environment
description: Re-deploy a prior version or SHA to a promoted environment, and wire an external system to trigger it.
---

Rollback re-deploys a prior version or SHA to a promoted environment, defaulting to the previous version (N-1). It reuses the same deploy callbacks promote drives; there is no separate rollback deploy path. See [Hotfix and rollback workflows](/cascade/reference/generated-workflows/#hotfix-and-rollback-workflows) for the generated job structure.

## The first-environment guard

Rollback covers promoted environments only. The first environment tracks trunk directly and is never promoted into, so it has no deploy history to roll back to. The workflow's environment dropdown offers only the promoted environments; targeting the first environment fails fast with that guidance. Roll it forward instead, by reverting the offending change on trunk.

## Trigger a rollback

Dispatch `cascade-rollback.yaml`:

```bash
gh workflow run cascade-rollback.yaml \
  -f environment=prod \
  -f target=v1.4.2
```

| Input | Default | Meaning |
|-------|---------|---------|
| `environment` | (required) | Environment to roll back. |
| `target` | previous version (N-1) | Prior version or SHA; omit to use the previous version. |
| `deployable` | whole environment | Limit the rollback to one deployable. |
| `dry_run` | `false` | Resolve and print the plan without deploying. |

A read-only preflight resolves the target, the deploy stage re-runs the configured deploy callbacks keyed on that SHA, and finalize writes the rolled-back state to trunk.

For a state-only correction, where the running system already reflects the older version and only the manifest needs to catch up, `cascade rollback --env prod --to v1.2.2` writes the state directly without invoking any deploy callback. Use the generated workflow for an actual re-deploy.

## The rollback manifest block

The rollback workflow is `workflow_dispatch`-only by default: byte-for-byte unchanged unless you configure it. Set `rollback.repository_dispatch` to let an external system (an alerting or incident pipeline) trigger the same rollback:

```yaml
rollback:
  repository_dispatch:
    types: [rollback-requested]
```

This adds a `repository_dispatch` trigger alongside the unchanged `workflow_dispatch`; every rollback parameter read then coalesces the manual input with the dispatch payload, so both paths resolve the same target. `repository_dispatch` carries no `inputs`, so the external caller supplies parameters in `client_payload`, keyed name-for-name against the manual inputs above:

```bash
gh api repos/my-org/my-repo/dispatches \
  -f event_type=rollback-requested \
  -F 'client_payload[environment]=prod' \
  -F 'client_payload[target]=v1.4.2'
```

At least one event type is required, and each may contain only letters, digits, dots, hyphens, and underscores. This is a real, emitted manifest field, not a placeholder; see the [manifest reference](/cascade/reference/manifest/) for the full block and its validation rules.

## What to watch

- **Preflight resolution** shows where the target came from (current state, the deploy-history ring, or manifest git history) so you can confirm it picked the SHA you meant, especially with a short SHA prefix.
- **A no-op result** means the environment (or deployable) is already at the resolved target; nothing runs.
- **Finalize marks the environment diverged.** A rolled-back environment behaves like a hotfixed one until the next forward promotion rejoins it to trunk.
- **`dry_run: true`** on either trigger path resolves and prints the plan without deploying or writing state, useful for confirming what an external dispatch would do before wiring it up live.

## Wayfinding

**Prerequisite:** [Promote a release](/cascade/guides/promote/) for the normal path rollback undoes.

**Next:** [Simulate and verify](/cascade/guides/simulate-and-verify/) to preview a rollback (or any promotion) before it runs for real.
