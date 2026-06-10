# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
The manifest schema is versioned with a single integer `schema_version`; the
schema-version compatibility policy is documented in
[docs/versioning.md](docs/versioning.md). A `Migration` section is added to any
release that bumps `schema_version`.

## [Unreleased]

### Added

- `cascade rollback` command: re-promotes a prior recorded version for a given
  deployable, reading version history from the manifest state store. (#69)
- GHA artifact passing between build jobs in a single orchestrate run: build and
  deploy callbacks may declare an `artifact:` block with `upload` and `downloads`
  sub-keys; the generator emits `actions/upload-artifact` and
  `actions/download-artifact` steps automatically. (#68)
- Per-deployable version recorded on promote finalize: the manifest state now
  tracks which version of each deployable is live per environment, making
  `cascade status` and rollback reliable. (#67)
- Per-callback `runs_on`, `permissions` (including OIDC `id-token`), and
  `concurrency` on inline-run jobs. (#66)
- `cascade status` subcommands for read-only state inspection: per-environment
  summary, per-deployable detail, and raw JSON output. (#61)
- `strategy.matrix` on build jobs in the orchestrate workflow: build callbacks
  that declare `matrix:` get a GHA `strategy:` block; `max-parallel` and
  `fail-fast` are emitted when non-zero/non-default. (#60)
- `extra_triggers` on the orchestrate workflow: `schedule`, `repository_dispatch`,
  `workflow_run`, and `merge_group` triggers are wired from the manifest. (#57)
- `concurrency` blocks on promote, release, and external-update workflows. (#55)
- `schema_version` field on the manifest (`ci.config.schema_version`). The CLI
  reads it on load and enforces a compatibility contract: a manifest written for
  a newer schema than the CLI understands is rejected (upgrade the CLI), a
  manifest below the supported minimum is rejected (migrate), and a manifest
  that omits the field is accepted with a warning. `parse-config` surfaces
  non-fatal advisories in a new `warnings` field on its JSON output. (#43, #63)
- `docs/versioning.md` describing the schema-version compatibility policy, the
  schema-version to CLI-version matrix, and the deprecation window.
- Inline run callbacks: cascade-owned callback jobs may use `run:` / `shell:`
  in place of `uses:` (reusable workflow). The `workflow` XOR `run` constraint
  is enforced at parse time. (#62)
- Frozen v1 manifest schema field shapes: all field names, types, and `omitempty`
  behaviour are frozen for the v1 schema generation. (#45)

### Fixed

- Self-host CI trunk branch corrected from `master` to `main`; orchestrate
  workflow now triggers on `main` pushes. (#58)
- `e2e`: parallel runs no longer silently skip when `orchestrate.yaml` is absent;
  the suite now fails fast with a clear error. (#47)

### Maintenance

- OpenSSF Scorecard hardening: all generated and self-hosting workflows pin
  third-party action refs by SHA and declare least-privilege `permissions`. (#46)
- Merge-queue (`merge_group`) and nightly schedule triggers added to the e2e
  workflow. (#59)
- Dependency bumps: `actions/checkout` v6, `actions/setup-go` v6,
  `actions/upload-artifact` v7, `goreleaser/goreleaser-action` v7,
  `github.com/spf13/cobra` v1.10.2. (#48, #49, #52, #53, #54)

## [0.1.0] - 2026-06-09

Initial release of cascade: a trunk-based CI/CD orchestrator for GitHub Actions.

### Added

- `cascade generate` / `cascade parse-config` CLI: reads a YAML manifest and
  emits a complete set of GitHub Actions workflow files (orchestrate, promote,
  release, hotfix, external-update).
- Manifest-driven configuration: trunk branch, environments, deployables,
  build/deploy/promote/release/rollout/rollback callback wiring, change-detection
  paths, and trigger rules all declared in a single `manifest.yaml`.
- `cascade promote`, `cascade release`, and `cascade finalize` state-machine
  commands for managing the deployment lifecycle.
- `docs/` suite: architecture overview, callback contract, CLI reference,
  configuration reference, and getting-started guide.
- Self-hosted CI: cascade manages its own build, release, e2e, and promote
  workflows using the same manifest.

[Unreleased]: https://github.com/stablekernel/cascade/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/stablekernel/cascade/releases/tag/v0.1.0
