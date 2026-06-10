# Versioning and schema compatibility

The cascade manifest is the public contract between your repository and the
cascade CLI. This document describes how the manifest schema is versioned and
how the CLI decides whether it can read a given manifest.

## `schema_version`

Every manifest may declare a schema version under `ci.config`:

```yaml
ci:
  config:
    schema_version: 1
    trunk_branch: main
    # ...
```

`schema_version` is a single monotonic integer — a "schema major" — not a semver
string. It identifies which breaking-change generation of the schema the
manifest is written for.

### Why an integer

The manifest evolves additively. New capabilities arrive as new optional fields,
new enum values, or new nested blocks, each with a sensible default. An older
CLI ignores fields it does not recognize, and a newer CLI fills in defaults for
fields an older manifest omits. Because of this, **additive changes never change
`schema_version`**. The integer only moves when a change is genuinely breaking:

- a field is removed,
- a field is re-typed,
- the default behavior of an existing field changes.

A semver string would imply minor and patch schema axes that, given the
additive-only design, never need to exist.

## Compatibility rules

The CLI knows two bounds:

- `CurrentSchemaVersion` — the highest schema version this CLI understands. A
  manifest that omits `schema_version` is assumed to target this version.
- `MinSchemaVersion` — the oldest schema version this CLI still reads.

On load, the CLI applies the following rules:

| Manifest `schema_version` | CLI behavior |
| --- | --- |
| equal to `CurrentSchemaVersion` | Accepted silently. |
| omitted or `0` | Accepted with a warning; assumed to be `CurrentSchemaVersion`. Pin it explicitly. Because `schema_version` is an `int` field with `omitempty`, an explicit `schema_version: 0` is encoded identically to an absent field and is treated the same way — as omitted. |
| between `MinSchemaVersion` and `CurrentSchemaVersion - 1` | Accepted with a warning; the CLI still reads it. See the migration table below. |
| below `MinSchemaVersion` (and not `0`) | Rejected. The schema generation is no longer supported; follow the migration table. |
| above `CurrentSchemaVersion` | Rejected. The manifest needs a newer CLI; upgrade the `cli_version` pin. A newer schema may rely on changed semantics this CLI would mis-handle, so it does not guess. |
| negative | Rejected as invalid. |

A rejected manifest is a fatal, generation-blocking condition: the CLI reports
the error and does not produce workflows. A warning is non-fatal and is surfaced
on stderr and in the `warnings` field of `parse-config` JSON output.

## Schema-version to CLI-version matrix

| Schema version | First CLI version | Status |
| --- | --- | --- |
| 1 | (current) | Supported |

This table is updated whenever `schema_version` is bumped.

## Deprecation window

A CLI supports the current schema version and the immediately preceding one
(N-1). When a new schema major lands, CLIs that ship with it continue to read
the previous major with a warning. A subsequent major may drop support for the
oldest major, at which point manifests at that version are rejected with a
pointer to the migration entry in [CHANGELOG.md](../CHANGELOG.md).

## Migrations

Each `schema_version` bump is recorded with a `Migration` section in
[CHANGELOG.md](../CHANGELOG.md) describing exactly what changed and the steps to
update a manifest from the previous version. There are no migrations yet: the
current schema version is the first.

## Supported release line

**0.x (current)** — the active development line. Bug fixes, security patches,
and new capabilities all land here. No stability guarantee is made for the CLI
command surface or the manifest schema between 0.x releases; additive changes
arrive without a `schema_version` bump, but breaking changes (field removals,
type changes, behaviour changes) increment `schema_version` and carry a
`Migration` entry in [CHANGELOG.md](../CHANGELOG.md).

**1.0** — when cascade reaches v1.0 the following guarantees apply:

- The CLI command surface (flags, subcommands, exit codes, JSON output shapes)
  follows semver: breaking changes require a major version bump.
- The manifest schema follows the integer-major versioning described in this
  document. An additive change never bumps `schema_version`; only a breaking
  change does.
- The N-1 schema deprecation window (described above) is honoured across all
  1.x releases.

Older tags outside the current release line do not receive backported fixes.
See [SECURITY.md](../SECURITY.md) for the security-patch policy.

## Version bump reference

| Change type | CLI semver impact | `schema_version` impact |
| --- | --- | --- |
| New optional manifest field with a sensible default | patch | none |
| New CLI subcommand or flag | minor | none |
| Changed default behaviour of an existing field | major | bump |
| Field removed or re-typed | major | bump |
| CLI flag or subcommand removed | major | none |
