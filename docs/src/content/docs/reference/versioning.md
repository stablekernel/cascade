---
title: Versioning and schema compatibility
description: How the cascade manifest schema is versioned, which reserved fields are inert, and the hotfix version grammar.
---

The cascade manifest is the contract between your repository and the cascade CLI. This page describes how the manifest schema is versioned, which fields are reserved for future behavior, and how version numbers are allocated during a hotfix.

## Schema policy

Every manifest may declare a schema version under `ci.config`:

```yaml
ci:
  config:
    schema_version: 1
    trunk_branch: main
    # ...
```

`schema_version` is a single monotonic integer, the "schema major." It is not a semver string. It identifies which breaking-change generation of the schema the manifest is written for. The current version is `1`.

The manifest evolves additively: new capabilities arrive as new optional fields, new enum values, or new nested blocks, each with a sensible default. An older CLI ignores fields it does not recognize, and a newer CLI fills in defaults for fields an older manifest omits. Additive changes never bump `schema_version`. The integer only moves when a change is genuinely breaking: a field is removed, a field is re-typed, or the default behavior of an existing field changes.

## Compatibility rules

The CLI knows two bounds:

- `CurrentSchemaVersion` is the highest schema version this CLI understands. A manifest that omits `schema_version` is assumed to target this version.
- `MinSchemaVersion` is the oldest schema version this CLI still reads.

On load, the CLI applies these rules:

| Manifest `schema_version` | CLI behavior |
| --- | --- |
| equal to `CurrentSchemaVersion` | Accepted silently. |
| omitted or `0` | Accepted with a warning; assumed to be `CurrentSchemaVersion`. Pin it explicitly. Because `schema_version` is an `int` field with `omitempty`, an explicit `schema_version: 0` is encoded identically to an absent field and treated the same way. |
| between `MinSchemaVersion` and `CurrentSchemaVersion - 1` | Accepted with a warning; the CLI still reads it. See the migration table below. |
| below `MinSchemaVersion` (and not `0`) | Rejected. Follow the migration table. |
| above `CurrentSchemaVersion` | Rejected. The manifest needs a newer CLI; upgrade the `cli_version` pin. |
| negative | Rejected as invalid. |

A rejected manifest is fatal: the CLI reports the error and produces no workflows. A warning is non-fatal and surfaces on stderr and in the `warnings` field of `parse-config` JSON output.

### Schema-version to CLI-version matrix

| Schema version | First CLI version | Status |
| --- | --- | --- |
| 1 | (current) | Supported |

This table is updated whenever `schema_version` is bumped.

### Deprecation window

A CLI supports the current schema version and the immediately preceding one (N-1). When a new schema major lands, CLIs that ship with it continue to read the previous major with a warning. A subsequent major may drop support for the oldest major, at which point manifests at that version are rejected with a pointer to the migration entry in [CHANGELOG.md](https://github.com/stablekernel/cascade/blob/main/CHANGELOG.md).

## Reserved shapes

These fields parse and pass structural validation today but carry no generator, state, or runtime behavior. A manifest declaring them produces byte-identical generated workflows, so adopting the shape now is safe. Attaching behavior to any of them later is additive and does not bump `schema_version`.

### Per-component versioning

Three slots are frozen at `schema_version` 1 for independently versioned components that share one manifest:

- A top-level `components` map, keyed by component name, where each entry carries an optional `path` (the subtree the component owns) and `tag_prefix` (its version-tag prefix).
- A matching `state.<env>.components` map that records the per-component version and SHA for an environment.
- A `latest_release.components` map that records the per-component published release.

Component names must be job-ID-safe (letters, digits, hyphens, underscores), and a configured `path` must be relative with no `..` segments.

### Progressive rollout: canary and blue/green

A deploy may declare a `rollout:` block with a `type` of `default`, `rolling`, `canary`, or `blue_green`. Two fields on `rollout:` are live (see [Progressive rollout](#progressive-rollout) below); the type-specific sub-blocks are reserved.

The `canary:` sub-block reserves six fields:

- `percent`, the initial canary weight, an integer from 1 to 100.
- `bake_time`, the soak duration before promotion, written as a Go duration string (for example `30m`).
- `promote_callback`, a local workflow path that performs the promotion.
- `rollback_callback`, a local workflow path that performs the rollback.
- `steps`, the percent waves for a multi-step rollout (for example `[10, 50, 100]`).
- `analysis`, a workflow path that gates each wave.

The `blue_green:` sub-block reserves one field:

- `switch`, the workflow path that performs the cutover.

`matrix:` and `rollout:` are separate canonical concerns: `matrix:` describes the fan-out a callback runs across, and `rollout:` describes how a release advances through a callback. There is no shared `strategy:` block that combines them.

### GitOps deploy target

A deploy may declare a `deploy_target:` block with a `mode` of `dispatch` (the default, the existing external/notify cross-repo model) or `gitops` (push a rendered field into a dedicated config repo). The `gitops` variant reserves:

- `branch`, the target branch for the GitOps write (an env-to-branch mapping); default is the target repo's default branch.
- `track_sha`, a boolean that, when true, records the post-push HEAD SHA of the target repo into state.

A matching per-env deploy state slot, `target_sha`, reserves room to record the reconciled GitOps-repo HEAD SHA so a future implementation can key promotion off it. `branch` and `track_sha` are meaningful only when `mode` is `gitops`.

### Telemetry sink

The manifest reserves a vendor-neutral telemetry seam under `config.telemetry`: `enabled`, and an `adapter` value (for example `none` or `datadog`) so no vendor client is baked into cascade. Two enriched fields:

- `webhook`, a generic JSON-POST sink with a `url` (the destination the run posts telemetry to) and a `secret_name` (the name of a GitHub Actions secret holding the auth token, never an inline token value).
- `job_summary`, a boolean that toggles the run-UI summary table. It is omitted when unset, so an unset value stays distinct from an explicit `false`.

See the full field list in the [manifest reference](/cascade/reference/manifest/).

### Version-intent overrides

cascade derives the next version from conventional commits. Some version intent cannot be expressed that way, for example forcing a pre-release line or a specific exact version for a release. Under `release:`, a `version_overrides:` block addresses maintainer-committed override files carrying that intent:

- `dir`, a relative directory pointer to the override files. Must be relative with no `..` segments. Empty means the implementation default (reserved).

Only the addressing pointer is frozen in v1. The override-file format and the fold-into-version-calculation behavior are additive and arrive later; any future override values map onto the existing version primitives (the bump level and the pre-release line) rather than introducing a parallel scheme.

## Progressive rollout

Two fields on `rollout:` are live today, not reserved. `rollout.fail_fast` and `rollout.max_parallel` (deploys and publish only) are emitted directly into the deploy job's `strategy:` block: `fail_fast` sets `strategy.fail-fast` (defaulting to `false` when unset), and `max_parallel`, when greater than zero, sets `strategy.max-parallel`. A manifest that sets either field changes the generated workflow.

Only the `type`, `canary`, and `blue_green` sub-blocks remain reserved and inert, as described above. Setting `type: canary` or populating a `canary:`/`blue_green:` sub-block parses and validates but has no effect on generated output today.

## Migrations

Each `schema_version` bump is recorded with a `Migration` section in [CHANGELOG.md](https://github.com/stablekernel/cascade/blob/main/CHANGELOG.md) describing exactly what changed and the steps to update a manifest from the previous version. There are no migrations yet: the current schema version is the first.

## Supported release line

### 0.x (current)

This is the active development line. Bug fixes, security patches, and new capabilities all land here. No stability guarantee is made for the CLI command surface or the manifest schema between 0.x releases. Additive changes arrive without a `schema_version` bump. Breaking changes (field removals, type changes, behavior changes) increment `schema_version` and carry a `Migration` entry in [CHANGELOG.md](https://github.com/stablekernel/cascade/blob/main/CHANGELOG.md).

### 1.0

When cascade reaches v1.0 the following guarantees apply:

- The CLI command surface (flags, subcommands, exit codes, JSON output shapes) follows semver: breaking changes require a major version bump.
- The manifest schema follows the integer-major versioning described in this page. An additive change never bumps `schema_version`; only a breaking change does.
- The N-1 schema deprecation window is honored across all 1.x releases.

Older tags outside the current release line do not receive backported fixes. See [SECURITY.md](https://github.com/stablekernel/cascade/blob/main/SECURITY.md) for the security-patch policy.

## Hotfix version grammar

A hotfix applies one or more trunk commits onto an environment pinned to an older trunk base (see the [hotfix guide](/cascade/guides/hotfix/)). The version cascade allocates for a hotfix depends on whether the environment's current version is still in flight (a pre-release) or already published.

### rc-based (unpublished) base

When the environment holds a pre-release version, the hotfix appends a nested `hotfix.M` segment. With the default grammar (prerelease token `rc`, separator `.`):

```
v1.4.0-rc.2          -> v1.4.0-rc.2.hotfix.1   (first hotfix)
v1.4.0-rc.2.hotfix.1 -> v1.4.0-rc.2.hotfix.2   (second hotfix, stacked)
```

The dotted form is deliberate. Under semver precedence the pre-release field list for `v1.4.0-rc.2.hotfix.1` is `["rc", "2", "hotfix", "1"]`, which sorts strictly above `rc.2` and strictly below `rc.3`:

```
v1.4.0-rc.2 < v1.4.0-rc.2.hotfix.1 < v1.4.0-rc.2.hotfix.2 < v1.4.0-rc.3
```

A hotfix version therefore slots cleanly between its base pre-release and the next one, and it never collides with the orchestrator's pre-release sequence. The general shape is `<prefix>X.Y.Z-<token><separator>N.hotfix.M`, where the prefix, token, and separator come from the resolved [`tag_grammar`](/cascade/reference/manifest/#tag_grammar) (`v`, `rc`, and `.` unless configured otherwise); the nested `hotfix.M` segment itself is fixed and not reshaped by `tag_grammar`. The pre-release-shaped tag and draft cleanup logic matches this same resolved shape, so it is inert on hotfix tags; hotfix tags and drafts are cleaned up explicitly when the divergence ends.

### Published (no rc) base

When the environment holds a published version with no rc segment (for example `v1.3.0`), a hotfix is a normal patch bump, not a `-hotfix.M` shape:

```
v1.3.0 -> v1.3.1   (first hotfix)
v1.3.1 -> v1.3.2   (next free patch)
```

cascade allocates the next free patch by reconciling against existing tags, so the hotfix does not collide with a patch the normal release flow may also mint. There is no `vX.Y.Z-hotfix.M` form; the nested `hotfix.M` segment applies only to still-in-flight, pre-release versions.

## Tag grammar

The `-rc.N` shape used throughout this page is cascade's default pre-release grammar, not a fixed rule. An optional [`tag_grammar`](/cascade/reference/manifest/#tag_grammar) manifest block reshapes the prefix, the pre-release token, and the separator between the token and its number, so the general tag shape is `<prefix>X.Y.Z-<token><separator>N[.hotfix.M]`. A manifest that omits `tag_grammar` reproduces the historical grammar shown above byte-identically.

Two rules bound how far this configurability goes:

- **Read tolerance.** On read, cascade also recognizes a foreign pre-release shape (for example `beta.1` or `rc1`) and build metadata (for example `+build.5`) left over from before `tag_grammar` was adopted, so an existing repository's tag history stays visible to version discovery. cascade never emits those shapes itself, and a recognized foreign pre-release always sorts below its release.
- **Clean release boundary only.** Changing `tag_grammar` is supported at a clean release boundary, when no version is currently in flight. There is no mid-flight migration window that mixes two grammars across the same in-progress release.

See the [manifest reference](/cascade/reference/manifest/#tag_grammar) for the full field list and defaults.

## Version bump reference

| Change type | CLI semver impact | `schema_version` impact |
| --- | --- | --- |
| New optional manifest field with a sensible default | patch | none |
| New CLI subcommand or flag | minor | none |
| Changed default behavior of an existing field | major | bump |
| Field removed or re-typed | major | bump |
| CLI flag or subcommand removed | major | none |

## Wayfinding

**Prerequisite:** the [manifest reference](/cascade/reference/manifest/) for the full field surface these reserved shapes and the `rollout:` block sit inside.

**Next:** [Security](/cascade/security/) for the trust model, action pinning, and hardening checklist.
