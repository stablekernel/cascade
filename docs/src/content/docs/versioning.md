---
title: Versioning and schema compatibility
description: How the cascade manifest schema is versioned and how the CLI decides whether it can read a given manifest, including compatibility rules and the hotfix version segment.
---

The cascade manifest is the contract between your repository and the cascade CLI. This document describes how the manifest schema is versioned and how the CLI decides whether it can read a given manifest.

## `schema_version`

Every manifest may declare a schema version under `ci.config`:

```yaml
ci:
  config:
    schema_version: 1
    trunk_branch: main
    # ...
```

`schema_version` is a single monotonic integer, the "schema major". It is not a semver string. It identifies which breaking-change generation of the schema the manifest is written for.

### Why an integer

The manifest evolves additively. New capabilities arrive as new optional fields, new enum values, or new nested blocks, each with a sensible default. An older CLI ignores fields it does not recognize, and a newer CLI fills in defaults for fields an older manifest omits. Because of this, additive changes never change `schema_version`. The integer only moves when a change is genuinely breaking:

- a field is removed,
- a field is re-typed,
- the default behavior of an existing field changes.

A semver string would imply minor and patch schema axes that, given the additive-only design, never need to exist.

## Compatibility rules

The CLI knows two bounds:

- `CurrentSchemaVersion` is the highest schema version this CLI understands. A manifest that omits `schema_version` is assumed to target this version.
- `MinSchemaVersion` is the oldest schema version this CLI still reads.

On load, the CLI applies the following rules:

| Manifest `schema_version` | CLI behavior |
| --- | --- |
| equal to `CurrentSchemaVersion` | Accepted silently. |
| omitted or `0` | Accepted with a warning; assumed to be `CurrentSchemaVersion`. Pin it explicitly. Because `schema_version` is an `int` field with `omitempty`, an explicit `schema_version: 0` is encoded identically to an absent field and is treated the same way, as omitted. |
| between `MinSchemaVersion` and `CurrentSchemaVersion - 1` | Accepted with a warning; the CLI still reads it. See the migration table below. |
| below `MinSchemaVersion` (and not `0`) | Rejected. The schema generation is no longer supported; follow the migration table. |
| above `CurrentSchemaVersion` | Rejected. The manifest needs a newer CLI; upgrade the `cli_version` pin. A newer schema may rely on changed semantics this CLI would mis-handle, so it does not guess. |
| negative | Rejected as invalid. |

A rejected manifest is a fatal, generation-blocking condition: the CLI reports the error and does not produce workflows. A warning is non-fatal and is surfaced on stderr and in the `warnings` field of `parse-config` JSON output.

## Schema-version to CLI-version matrix

| Schema version | First CLI version | Status |
| --- | --- | --- |
| 1 | (current) | Supported |

This table is updated whenever `schema_version` is bumped.

## Deprecation window

A CLI supports the current schema version and the immediately preceding one (N-1). When a new schema major lands, CLIs that ship with it continue to read the previous major with a warning. A subsequent major may drop support for the oldest major, at which point manifests at that version are rejected with a pointer to the migration entry in [CHANGELOG.md](https://github.com/stablekernel/cascade/blob/main/CHANGELOG.md).

## Reserved shape: per-component versioning

The manifest reserves the shape for independently versioned components that share one manifest. Three slots are frozen at `schema_version` 1:

- A top-level `components` map, keyed by component name, where each entry carries an optional `path` (the subtree the component owns) and `tag_prefix` (its version-tag prefix).
- A matching `state.<env>.components` map that records the per-component version and SHA for an environment.
- A `latest_release.components` map that records the per-component published release.

These slots parse and pass structural validation today, but carry no generator, state, or runtime behavior. A manifest may declare them without changing any generated workflow. Component names must be job-ID-safe (letters, digits, hyphens, underscores) and a configured `path` must be relative with no `..` segments, so a later release can attach behavior without re-typing the fields.

That later release attaches behavior additively, so it does not bump `schema_version`: a manifest written against the reserved shape stays valid, and the schema-version-to-CLI matrix above is unchanged.

## Reserved shape: progressive rollout

The manifest reserves the shape for progressive rollout on a deploy callback. A deploy may declare a `rollout:` block with a `type` of `default`, `rolling`, `canary`, or `blue_green`, and an optional sub-block matching that type.

The `canary:` sub-block reserves four fields:

- `percent`, the initial canary weight, an integer from 1 to 100.
- `bake_time`, the soak duration before promotion, written as a Go duration string (for example `30m`).
- `promote_callback`, a local workflow path that performs the promotion.
- `rollback_callback`, a local workflow path that performs the rollback.

The `blue_green:` sub-block reserves one field:

- `switch`, the workflow path that performs the cutover.

These fields parse and pass structural validation today, but carry no generator behavior. A manifest declaring them produces byte-identical generated workflows, so the reserved shape is safe to adopt now. Attaching behavior to these fields later is additive and does not bump `schema_version`.

`matrix:` and `rollout:` are separate canonical concerns: `matrix:` describes the fan-out a callback runs across, and `rollout:` describes how a release advances through a callback. There is no shared `strategy:` block that combines them.

## Reserved shape: GitOps deploy target

The manifest reserves the shape for a GitOps-mirror deploy variant on a deploy. A deploy may declare a `deploy_target:` block with a `mode` of `dispatch` (the default, the existing external/notify cross-repo model) or `gitops` (push a rendered field into a dedicated config repo).

The `gitops` variant reserves these enriched fields:

- `branch`, the target branch for the GitOps write (an env-to-branch mapping); the default is the target repo's default branch.
- `track_sha`, a boolean that, when true, records the post-push HEAD SHA of the target repo into state.

A matching per-env deploy state slot, `target_sha`, reserves room to record the reconciled GitOps-repo HEAD SHA so a future implementation can key promotion off it.

`branch` and `track_sha` are meaningful only when `mode` is `gitops`. These fields parse and pass structural validation today, but carry no generator behavior. A manifest declaring them produces byte-identical generated workflows, so the reserved shape is safe to adopt now. Attaching behavior to these fields later is additive and does not bump `schema_version`.

## Reserved shape: telemetry sink

The manifest reserves a vendor-neutral telemetry seam under `config.telemetry`. The seam carries `enabled` and an `adapter` value (for example `none` or `datadog`); the vendor stays a value behind the adapter, so no vendor client is baked into cascade. The reserved shape adds two enriched fields:

- `webhook`, a generic JSON-POST sink with a `url` (the destination the run posts telemetry to) and a `secret_name` (the name of a GitHub Actions secret holding the auth token). `secret_name` is a reference to a secret, never an inline token value.
- `job_summary`, a boolean that toggles the run-UI summary table. It is omitted when unset, so an unset value stays distinct from an explicit `false`; default-on behavior arrives in a later release.

These fields parse and pass structural validation today, but carry no generator or emit behavior. A manifest declaring them produces byte-identical generated workflows, so the reserved shape is safe to adopt now. Attaching behavior to these fields later is additive and does not bump `schema_version`.

## Reserved shape: version-intent overrides

cascade derives the next version from conventional commits. Some version intent cannot be expressed that way, for example forcing a pre-release line or a specific exact version for a release. The manifest reserves, under `release:`, a `version_overrides:` block that addresses maintainer-committed override files carrying that intent:

- `dir`, a relative directory pointer to the override files. It must be a relative path with no `..` segments. Empty means the implementation default (reserved).

Only the addressing pointer is frozen in v1. The override-file format and the fold-into-version-calculation behavior are additive and arrive post-1.0; any future override values map onto the existing version primitives (the bump level and the pre-release line) rather than introducing a parallel scheme. This block parses and passes structural validation today, but carries no generator, state, or runtime behavior. A manifest declaring it produces byte-identical generated workflows, so the reserved shape is safe to adopt now, and attaching behavior later does not bump `schema_version`.

## Migrations

Each `schema_version` bump is recorded with a `Migration` section in [CHANGELOG.md](https://github.com/stablekernel/cascade/blob/main/CHANGELOG.md) describing exactly what changed and the steps to update a manifest from the previous version. There are no migrations yet: the current schema version is the first.

## Supported release line

### 0.x (current)

This is the active development line. Bug fixes, security patches, and new capabilities all land here. No stability guarantee is made for the CLI command surface or the manifest schema between 0.x releases. Additive changes arrive without a `schema_version` bump. Breaking changes (field removals, type changes, behaviour changes) increment `schema_version` and carry a `Migration` entry in [CHANGELOG.md](https://github.com/stablekernel/cascade/blob/main/CHANGELOG.md).

### 1.0

When cascade reaches v1.0 the following guarantees apply:

- The CLI command surface (flags, subcommands, exit codes, JSON output shapes) follows semver: breaking changes require a major version bump.
- The manifest schema follows the integer-major versioning described in this document. An additive change never bumps `schema_version`; only a breaking change does.
- The N-1 schema deprecation window (described above) is honoured across all 1.x releases.

Older tags outside the current release line do not receive backported fixes. See [SECURITY.md](https://github.com/stablekernel/cascade/blob/main/SECURITY.md) for the security-patch policy.

## Hotfix version segment

A hotfix applies a single trunk commit onto an environment pinned to an older trunk base (see the Hotfix section of [Workflows](/cascade/workflows/)). The version cascade allocates for a hotfix depends on whether the environment's current version is still in flight (an rc) or already published.

### rc-based (unpublished) base

When the environment holds an rc version, the hotfix appends a nested `hotfix.M` segment:

```
v1.4.0-rc.2          -> v1.4.0-rc.2.hotfix.1   (first hotfix)
v1.4.0-rc.2.hotfix.1 -> v1.4.0-rc.2.hotfix.2   (second hotfix, stacked)
```

The dotted form is deliberate. Under semver precedence the pre-release field list for `v1.4.0-rc.2.hotfix.1` is `["rc", "2", "hotfix", "1"]`, which sorts strictly above `rc.2` and strictly below `rc.3`:

```
v1.4.0-rc.2 < v1.4.0-rc.2.hotfix.1 < v1.4.0-rc.2.hotfix.2 < v1.4.0-rc.3
```

A hotfix version therefore slots cleanly between its base rc and the next rc, and it never collides with the orchestrator's rc sequence. The rc-shaped tag and draft cleanup logic matches the plain `<prefix>X.Y.Z-rc.N` shape for the configured `tag_prefix` (the default `v`, a custom prefix such as `rel-`, or no prefix), so it is inert on hotfix tags; hotfix tags and drafts are cleaned up explicitly when the divergence ends.

### Published (no rc) base

When the environment holds a published version with no rc segment (for example `v1.3.0`), a hotfix is a **normal patch bump**, not a `-hotfix.M` shape:

```
v1.3.0 -> v1.3.1   (first hotfix)
v1.3.1 -> v1.3.2   (next free patch)
```

cascade allocates the next free patch by reconciling against existing tags, so the hotfix does not collide with a patch the normal release flow may also mint. There is no `vX.Y.Z-hotfix.M` form; the nested `hotfix.M` segment applies only to rc-based, still-in-flight versions.

## Version bump reference

| Change type | CLI semver impact | `schema_version` impact |
| --- | --- | --- |
| New optional manifest field with a sensible default | patch | none |
| New CLI subcommand or flag | minor | none |
| Changed default behaviour of an existing field | major | bump |
| Field removed or re-typed | major | bump |
| CLI flag or subcommand removed | major | none |
