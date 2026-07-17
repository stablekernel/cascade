---
title: Pin and reconcile actions
description: Pin every third-party action from one manifest source, choose tag or resolved-SHA mode, and adopt an external pin bump back into the manifest.
---

Cascade owns the third-party action references inside the workflows it generates. Rather than let `uses:` pins scatter and drift across many files, every pin resolves from one place: the manifest and a single committed pin table. This guide covers setting the pin style, overriding an individual action, and adopting an outside pin bump (for example a Dependabot update) back into the manifest so generated output agrees again. For the field definitions, see the [`pin_mode`](/cascade/reference/manifest/#action-pinning) and [`action_pins`](/cascade/reference/manifest/#action-pinning) reference.

## One source for every pin

Generated workflows are build output, so their action pins are build output too. Cascade emits each third-party action (for example `actions/checkout` and `actions/github-script`) from a single committed pin table, and you change a pin by editing the manifest, never by hand-editing the generated YAML. A hand-edited pin is reported as drift by the next [`cascade verify`](/cascade/reference/cli/#verify) and overwritten by the next regenerate. Keeping the source single is what makes the pins auditable: one table, one place to review, one place to bump.

## Choose tag or resolved-SHA mode

`pin_mode` sets the reference style for every third-party action Cascade emits:

```yaml
# .github/manifest.yaml
ci:
  config:
    trunk_branch: main
    pin_mode: sha
```

| Mode | Emits | Use it when |
|------|-------|-------------|
| `tag` (default) | `actions/checkout@v5` | You want readable, low-friction pins and trust the action major tag. |
| `sha` | `actions/checkout@0123...` with the version as a trailing comment | You want an immutable pin that a moved tag cannot repoint under you. |

Under `sha`, the pin resolves to a 40-character commit SHA and the human-readable version rides along as a comment, so a reviewer still sees which release the commit belongs to. Resolved-SHA mode is the stronger supply-chain posture and pairs naturally with `cli_version_sha` for the Cascade self-action pin.

## Override a single action

`action_pins` overrides the built-in reference for individual actions, keyed by action path. The value is the bare reference emitted after `@` for that action, a tag or a commit SHA, and it applies regardless of `pin_mode`. An override cannot repoint an action to a different owner or repository:

```yaml
ci:
  config:
    trunk_branch: main
    pin_mode: sha
    action_pins:
      actions/checkout: "0123456789abcdef0123456789abcdef01234567"
```

That emits `uses: actions/checkout@0123456789abcdef0123456789abcdef01234567`. An action that is neither in the built-in table nor overridden is emitted unchanged.

## Adopt an external pin bump

When an outside tool bumps a governed action in a generated workflow, that file now disagrees with the manifest. `cascade reconcile` closes the gap: it reads the changed source files, adopts the moved reference into the manifest's `action_pins`, and regenerates every workflow the manifest produces so the owned output agrees again. It reads only what you pass and the manifest; it never reads a pin back out of a generated file, and it never pushes, commits, or merges.

```bash
cascade reconcile \
  --changed-file .github/workflows/orchestrate.yaml \
  --changed-file .github/workflows/promote.yaml
```

Two flags shape the run:

- `--changed-file` names a changed source file to scan for a governed pin bump. Repeat it for each file the bump touched.
- `--check` runs the read-only detector instead: it reports whether the change is relevant and writes a data-only JSON artifact (path set by `--check-output`), writing nothing else. Use it to gate the adopting step in CI. This is the only read-only mode; a plain `reconcile` run adopts and regenerates in place.

## Automate it as a companion

Rather than run `reconcile` by hand, enable the reconcile companion so an external pin bump is detected and adopted in CI. Setting [`reconcile.enabled: true`](/cascade/reference/manifest/#reconcile) emits two workflows: a fork-safe read-only detector (`cascade-reconcile-check.yaml`) and an adopting companion (`cascade-reconcile-companion.yaml`) that writes the moved reference into `action_pins` and regenerates. Route the adoption commit with `reconcile.commit`: `append` pushes onto the triggering pull request branch, while `followup` opens a separate pull request, which is the safer choice when you automerge on green.

```yaml
ci:
  config:
    trunk_branch: main
    reconcile:
      enabled: true
      source: dependabot
      commit: followup
```

## Wayfinding

**Prerequisite:** [Getting started](/cascade/start/getting-started/) for a manifest that already generates workflows.

**Related:** [Governance companions](/cascade/guides/companions/) for the drift and preview checks that keep generated output honest on every pull request.

**Reference:** [`pin_mode`](/cascade/reference/manifest/#action-pinning), [`action_pins`](/cascade/reference/manifest/#action-pinning), and [`cascade reconcile`](/cascade/reference/cli/#reconcile).
