# Roadmap

This roadmap describes where cascade is headed over roughly the next year. It is
a statement of intent, not a contract: priorities shift as users adopt cascade
and report what they need. Dated, issue-level work is tracked in
[GitHub Issues](https://github.com/stablekernel/cascade/issues); this document
is the higher-level picture.

## What cascade is

cascade is a compiler for release and promotion pipelines. You describe your
environments, builds, deploys, and release rules in one manifest, and cascade
generates the GitHub Actions workflows that run them. The generated workflows are
plain YAML you commit and review; cascade never sits between your repository and
your runners at deploy time. This "compile, do not control" stance is the
organizing principle of the project, and it shapes everything on this roadmap.

## Guiding principles

These hold across every release and will not change without a major-version
discussion:

- **Compiler, not a control plane.** cascade produces workflows you own and run.
  It does not run a hosted service, hold your credentials, or act as a
  deployment broker at runtime.
- **The manifest is the source of truth.** Configuration and live deployment
  state live in one reviewed file in your repository.
- **Additive, compatible evolution.** New manifest fields are optional with
  sensible defaults. Existing fields are not removed or renamed within a major
  version.
- **Your logic stays yours.** Build and deploy steps are your reusable
  workflows, called through a documented contract. cascade is a metadata courier
  between stages.

## Near-term focus: hardening toward a stable v1

The current series is the 0.x line. The goal of this period is to close the gap
between what the manifest can describe and what the generator emits, and to
raise the project's assurance level so a 1.0 release means a frozen, dependable
manifest contract. Planned work includes:

- **Complete the generator surface.** A few GitHub Actions capabilities are
  modeled in the manifest shape but not yet fully emitted: environment gates,
  OIDC token configuration, and per-environment runner overrides. Closing these
  is on the direct path to v1.
- **Deepen end-to-end coverage.** Continue expanding the live and containerized
  end-to-end suites so generated workflows are exercised against real GitHub
  behavior, including edge cases such as empty builds, cross-repo coordination,
  hotfix-to-any-environment, and rollback to a previous version.
- **Raise and publish test coverage.** Grow statement coverage and report it
  publicly so adopters can see the project's quality signal.
- **Documentation and onboarding.** Keep the getting-started, adoption,
  callback-contract, and hardening guides accurate as the surface grows, and
  smooth the path from an existing pipeline to a cascade-generated one.

Supply-chain assurance is now in place: release checksums are signed with cosign
keyless signing and a GPG detached signature, SLSA build provenance is attested,
the build is reproducible, and users can verify what they download using the
published GPG key and the procedure in
[docs/release-verification.md](./docs/release-verification.md).

## The v1 milestone

v1.0.0 marks the point where the manifest schema is frozen as a stable contract.
After v1:

- Existing manifest fields keep their meaning across the entire v1 series.
- New capability arrives as new optional fields, never as breaking changes to
  what already works.
- A manifest that validates against v1 keeps validating for the life of v1.

The schema field shapes were established early as the v1 baseline. Minor releases
before v1 may add optional fields; they will not remove or rename existing ones.

## Looking past v1

These are directions under consideration, not commitments. They will be shaped
by what adopters actually ask for:

- Richer promotion policies and gating expressed declaratively in the manifest.
- Broader patterns for cross-repo and multi-artifact coordination.
- Better preview and explanation of what a generated pipeline will do before it
  runs.
- Quality-of-life improvements to scaffolding and migration from existing
  pipelines.

## What cascade will not do

Saying no keeps the tool coherent. cascade intentionally does not, and does not
plan to:

- **Become a hosted service or control plane.** cascade will not run your
  deployments, hold your secrets, or insert a runtime broker between your
  repository and your runners. It compiles workflows and steps out of the way.
- **Own your build and deploy logic.** cascade will not replace your build
  scripts or deploy tooling. Those stay in your reusable workflows; cascade
  orchestrates around them through the callback contract.
- **Target CI systems other than GitHub Actions.** cascade compiles to GitHub
  Actions. Supporting a second CI backend is out of scope for the foreseeable
  future.
- **Touch your registries or deployment targets directly.** cascade passes
  artifact identifiers and versions between stages. It does not push images,
  publish packages, or call cloud APIs on your behalf.
- **Replace conventional commits with a bespoke versioning scheme.** cascade
  derives versions and changelogs from conventional commits by design.
- **Break the manifest contract for convenience.** Backward compatibility within
  a major version is a hard rule, not a preference.

## How this roadmap changes

This document is revised as the project moves. Proposed changes to direction are
discussed in [GitHub Issues](https://github.com/stablekernel/cascade/issues) and
land through pull requests against this file, following the process in
[GOVERNANCE.md](./GOVERNANCE.md).
