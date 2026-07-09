---
title: Governance companions
description: Turn on the opt-in pull-request companions that catch workflow drift, preview a plan before merge, validate the manifest, and guard a merge queue.
---

Cascade generates a working pipeline from the required manifest fields alone. The governance companions are optional extra workflows you turn on when you want the pipeline to police itself on every pull request: catch drift before it lands, show a plan before merge, validate the manifest as a check, and guard a merge queue. Each is a single manifest toggle. This guide covers what each one does and when it earns its place. For the exact fields, see [Companion workflows](/cascade/reference/manifest/#companion-workflows-opt-in); for the emitted files, see [Opt-in companions](/cascade/reference/generated-workflows/#opt-in-companions).

## Drift check: keep generated output honest

The generated workflows are build output. If someone hand-edits `.github/workflows/orchestrate.yaml` or the manifest changes without a regenerate, the committed files no longer match what the manifest says they should be. The drift check catches exactly that: it runs [`cascade verify`](/cascade/reference/cli/#verify) on every pull request and fails the check when committed workflows fall out of sync with the manifest.

```yaml
# .github/manifest.yaml
ci:
  config:
    drift_check:
      enabled: true
      comment: true
```

Enable it when the generated workflows are committed to the repository, which is the normal case, and you want a hard gate against silent hand-edits and forgotten regenerates. The check job is read-only (`contents: read`). Setting `comment: true` also emits a fork-safe companion that posts the result as a sticky pull request comment, so a contributor sees the drift inline rather than digging into a failed check. When you turn the comment on, consider [`pin_mode: sha`](/cascade/reference/manifest/#action-pinning) to remove floating-tag exposure on that one write-scoped job.

## PR preview: see the plan before merge

The preview companion renders what the pipeline would do for a pull request without deploying anything. It is read-only, so reviewers can read the plan on the pull request itself instead of reconstructing it from the manifest in their heads.

```yaml
ci:
  config:
    pr_preview:
      enabled: true
      comment: true
```

Enable it when your pull requests change pipeline behavior often enough that reviewers benefit from seeing the resolved plan in context. `comment: true` posts or updates a sticky preview comment so the latest plan is always the one shown. It changes no state and runs no deploys, so it is safe to leave on broadly.

## Validate check: fail fast on a broken manifest

The validate check runs manifest validation as its own pull-request check, so a malformed or invalid manifest fails early and clearly rather than surfacing later as a confusing generate or orchestrate error.

```yaml
ci:
  config:
    validate_check:
      enabled: true
```

Enable it when more than one person edits the manifest, or when you want the manifest held to the schema on every change. It is the cheapest companion to run and the one that gives the clearest failure message when a manifest edit is wrong.

## Merge queue: validate the merge-group candidate

When the repository uses GitHub's merge queue, the merge-queue companion adds a `merge_group`-triggered lane that validates the queued candidate: it runs `cascade parse-config` and a dry-run `cascade orchestrate setup` against the merge-group commit before it is allowed to land.

```yaml
ci:
  config:
    merge_queue:
      enabled: true
```

Enable it when you have turned on GitHub merge queues for the repository and want the same manifest and orchestration checks applied to the combined merge candidate, not just to each pull request in isolation. Wiring the merge-queue trigger itself is handled by the `merge_group` entry under [`extra_triggers`](/cascade/reference/manifest/#extra_triggers); this companion adds the validation that runs on it.

## Wayfinding

**Prerequisite:** [Getting started](/cascade/start/getting-started/) for a manifest that already generates a pipeline.

**Related:** [Simulate and verify](/cascade/guides/simulate-and-verify/) for running these same checks locally before you push, and [Pin and reconcile actions](/cascade/guides/action-pins/) for the reconcile companion that adopts external pin bumps.

**Reference:** [Companion workflows](/cascade/reference/manifest/#companion-workflows-opt-in) for every field and default.
