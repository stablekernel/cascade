---
title: Run a hotfix
description: Patch an environment pinned to an older trunk base without dragging in every commit between its base and the fix.
---

A hotfix applies one or more trunk commits onto an environment that is pinned to an older trunk base, without advancing it past every intervening commit. Reach for it only when an environment must run exactly `base + fix` and nothing else; the standard promote flow cannot express that. This guide covers the operator path. For the generated `cascade-hotfix.yaml` job structure, see [Hotfix and rollback workflows](/cascade/reference/generated-workflows/#hotfix-and-rollback-workflows).

## What a hotfix does (roll-forward first)

The fix always lands on trunk first. cascade refuses to apply a commit that is not already an ancestor of trunk tip, so a hotfix never introduces a commit that exists only on a side branch. If the intervening commits between an environment's base and the fix are acceptable, merge to trunk and run a normal promotion instead; nothing diverges. The hotfix path is for the narrower case where the environment cannot take those other commits yet.

## Start a hotfix

Dispatch `cascade-hotfix.yaml` with the fix commit(s) and the target environment:

```bash
gh workflow run cascade-hotfix.yaml \
  -f commit=<fix-sha> \
  -f target_env=test
```

The `plan` job fetches env branches and tags and runs `cascade hotfix plan`. The `apply` job then cherry-picks the fix onto a per-environment integration branch and opens a resolution pull request labeled `cascade-hotfix` (or `cascade-hotfix-conflict` if the cherry-pick collides). Merging that pull request runs build and finalize, which writes the diverged state, tags the hotfix version, and creates the release.

:::caution[Known limitation: the hotfix deploy jobs are placeholders]
The deploy and rollback jobs in `cascade-hotfix.yaml` do not yet invoke the configured deploy workflow. Each one holds the target's GitHub `environment:` gate, prints the deploy it stands in for, and succeeds. A green hotfix run therefore cherry-picks, builds, and finalizes (state, tag, release), but does not deploy the fix. Ship it yourself by running the configured deploy workflow against the hotfix merge SHA. The standalone `cascade-rollback.yaml` workflow is not affected; its deploy jobs invoke the real deploy callbacks.
:::

Finalize is rerun-safe. It records the diverged state on trunk, then creates the hotfix tag and release. If the run fails partway (for example the release API errors after the state commit), rerun the finalize job: the rerun completes the missing tag or release with the version already recorded, without double-applying state or allocating a new version.

## Environment branches and the stale-branch self-heal

When an environment needs to diverge, the fix is staged on `env/<env>` (for example `env/test`), created on demand at the environment's recorded state SHA. The cherry-pick itself lands on `hotfix/<env>/<short-sha>`, based on `env/<env>`.

Before staging a new cherry-pick, the plan reconciles `env/<env>` against the environment's recorded state SHA. Three outcomes:

- **Absent**: created fresh at the recorded SHA.
- **Tip matches**: left untouched.
- **Tip has drifted**: cascade force-resets it back to the recorded SHA, but only when both hold: the environment is not already recorded as diverged, and a real single-flight check against the repository finds no open `cascade-hotfix` or `cascade-hotfix-conflict` pull request. If either check fails, the plan aborts rather than risk discarding in-flight work.

Pass `--repo owner/repo` to enable the single-flight check; without it, a stale tip aborts the run instead of self-healing. `--dry-run` reports the plan without acting on it.

## Elevation

A hotfix can target an environment higher in the chain than the one immediately above the first. cascade elevates the fix bottom-up across every environment between the first and the target, skipping any environment where the commit is already present. Every commit applied to an environment is recorded in that environment's `patches`, so elevation is cumulative, not just the first hop. The first environment is never a hotfix target: a fix reaches it only by merging to trunk.

On conflict, the chain halts at that environment; the pull request body lists which environments are still pending. Resolve the conflict by checking out the working branch, fixing it, and force-pushing:

```bash
git fetch && git switch hotfix/<env>/<short-sha>
# resolve conflicts, then
git push --force-with-lease
```

Re-dispatch targeting the same environment afterward to resume the chain from where it stopped.

## Version grammar

A hotfix allocates its own version segment so it sorts correctly relative to the pre-release sequence it interrupts. See [Hotfix version grammar](/cascade/reference/versioning/#hotfix-version-grammar) for the full derivation; the short form is `-rc.N.hotfix.M` by default (configurable via [`tag_grammar`](/cascade/reference/manifest/#tag_grammar)) for an unpublished base, or the next free patch for an already-published base.

## What to watch

- **plan** surfaces branch-protection suggestions as `::notice::` lines when `env/*` has no required checks configured; cascade never creates protection rules itself.
- **The resolution pull request** is the audit record even when no human touches it. It merges as the configured `state_token`, not `GITHUB_TOKEN`, because a `GITHUB_TOKEN` merge does not emit the event that triggers build/deploy/finalize.
- **A diverged environment blocks normal promotion** until the divergence clears. Promotion into it later requires the incoming trunk SHA to contain every recorded patch; dropping one is refused unless explicitly forced.
- **Prod is a valid target.** The deploy job binds to the GitHub `environment:` of the target, so required reviewers and wait timers apply exactly as they do for a normal promotion. The job behind that gate is still the placeholder described above; approval does not run the configured deploy workflow.

## Wayfinding

**Prerequisite:** [Promote a release](/cascade/guides/promote/) for the normal path a hotfix is the exception to.

**Next:** [Roll back an environment](/cascade/guides/rollback/) for undoing a bad deploy instead of patching one forward.
