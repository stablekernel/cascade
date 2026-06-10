# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
The manifest schema is versioned with a single integer `schema_version`; the
schema-version compatibility policy is documented in
[docs/versioning.md](docs/versioning.md). A `Migration` section is added to any
release that bumps `schema_version`.

## [Unreleased]

### Added

- `schema_version` field on the manifest (`ci.config.schema_version`). The CLI
  reads it on load and enforces a compatibility contract: a manifest written for
  a newer schema than the CLI understands is rejected (upgrade the CLI), a
  manifest below the supported minimum is rejected (migrate), and a manifest
  that omits the field is accepted with a warning. `parse-config` surfaces
  non-fatal advisories in a new `warnings` field on its JSON output.
- `docs/versioning.md` describing the schema-version compatibility policy, the
  schema-version to CLI-version matrix, and the deprecation window.

[Unreleased]: https://github.com/stablekernel/cascade/commits/main
