# Governance

This document describes how cascade is governed: who makes decisions, how those
decisions are made, and how the project continues if the maintainer becomes
unavailable. It is written to be honest about the project's current scale rather
than to describe an aspirational structure that does not yet exist.

## Project status

cascade is an open-source project published under the `stablekernel`
organization and licensed under Apache-2.0. Today it is a single-maintainer
project. One person holds the maintainer role and is responsible for the
direction, review, and release of the codebase. This is a normal and supported
shape for a focused tool, and the rest of this document is written with that
reality in mind.

## Roles and responsibilities

### Maintainer

The maintainer is the steward of the project. There is currently one maintainer.
The maintainer is responsible for:

- Setting and communicating the project's direction, recorded in
  [ROADMAP.md](./ROADMAP.md).
- Reviewing and merging pull requests.
- Triaging issues and security reports.
- Cutting releases and managing release signing keys and credentials.
- Enforcing the [Code of Conduct](./CODE_OF_CONDUCT.md).
- Keeping the documentation, security policy, and governance current.

The maintainer has write access to the repository and custody of the release
signing material and the credentials used by the release pipeline.

### Contributors

Anyone who opens an issue, proposes a change, improves documentation, or helps
others is a contributor. Contributors do not need any special permission to
participate. Contributions arrive as pull requests and are accepted under the
[Developer Certificate of Origin](https://developercertificate.org/) as
described in [CONTRIBUTING.md](./CONTRIBUTING.md). Contributors do not have
write access; their changes land through maintainer review.

### Users

Users adopt cascade in their own repositories. Their feedback, bug reports, and
adoption experiences are a primary input to the roadmap. Users are encouraged to
open issues and discussions.

## How decisions are made

cascade uses a benevolent-maintainer model. The maintainer is the final
decision-maker, and decisions are made in the open wherever practical.

- **Routine changes** (bug fixes, documentation, additive manifest fields,
  internal refactors) are decided through normal pull-request review. The
  maintainer reviews against the project's correctness, test, and guardrail bar
  and merges when it passes.
- **Significant changes** (new manifest surface, changes to the generated
  workflow contract, anything that affects backward compatibility) start as a
  GitHub issue that states the problem and the proposed approach before code is
  written. This gives users a chance to weigh in. The maintainer makes the final
  call and records the rationale on the issue or pull request.
- **Disagreements** are resolved through discussion on the relevant issue or
  pull request. When consensus is not reached, the maintainer decides and
  documents the reasoning so the decision is reviewable later.

### Proposing a change

1. Open an issue describing the problem and, if you have one, a proposed
   solution. For anything beyond a small fix, do this before writing code.
2. Discuss the approach. The maintainer will confirm direction or suggest an
   alternative.
3. Submit a pull request that follows [CONTRIBUTING.md](./CONTRIBUTING.md),
   including tests for new functionality.
4. The maintainer reviews, requests changes if needed, and merges.

## Compatibility and release authority

The maintainer is the authority on backward compatibility. cascade keeps the
manifest schema additive within a major version: new fields are optional, and no
existing field is removed or renamed before the next major version. The release
process and version policy are described in
the [architecture documentation](https://stablekernel.github.io/cascade/architecture/)
and the schema versioning guide.

## Changing this document

This governance model can change as the project grows, for example by adding
maintainers or moving to a formal council. Changes to governance are themselves
proposed and discussed through a pull request against this file.

## Continuity and succession

A single-maintainer project carries an obvious risk: if the maintainer becomes
unavailable, the project can stall. This section describes how cascade is set up
to survive the loss of any one person and to keep operating within about a week.
The plan is deliberately concrete about what exists today and what is a
maintainer follow-up.

### What keeps working without the maintainer

cascade is designed so that most of the project's value does not depend on the
maintainer being present:

- The source, history, license, and documentation are public in Git. Anyone can
  clone, fork, build, and run cascade. The Apache-2.0 license permits a fork to
  continue the project.
- Released binaries are published as GitHub Releases and the module is available
  through the Go module proxy, so existing users are unaffected by a gap in
  maintenance.
- The build, test, and release pipeline is defined as code in the repository, so
  a successor with the right credentials can reproduce a release by following the
  documented process rather than reconstructing tribal knowledge.

### Succession

The intent is for the project to have a designated backup maintainer who can
step in. Until a second maintainer is formally added, succession follows this
order:

1. **Designated backup maintainer.** The maintainer names a backup who has, or
   can be granted, the access described below. The backup is empowered to triage
   issues, accept changes, and cut releases.
2. **Organization owners.** Because the repository lives under the
   `stablekernel` GitHub organization, organization owners can restore
   administrative access to the repository, grant write access to a successor,
   and re-establish release credentials if the maintainer is unreachable.
3. **Community fork.** As a last resort, the Apache-2.0 license guarantees the
   community can fork and continue the project. This is the floor, not the plan.

### Operating the project during a handover

A successor with repository write access and the credentials below can perform
every routine maintainer task within a week:

- **Issues.** Create, label, triage, and close issues through the GitHub UI.
- **Changes.** Review and merge pull requests through the GitHub UI. The
  contribution bar (tests, lint, guardrails) is documented in
  [CONTRIBUTING.md](./CONTRIBUTING.md) so a successor can apply it consistently.
- **Releases.** Cut a release by following the documented release process. The
  pipeline is code in `.github/workflows`; the human inputs are the signing key
  and the pipeline credentials described next.

### Key and access custody

The items below are what a successor needs. Concrete custody arrangements are
maintainer follow-ups and are intentionally not embedded in this file.

- **Repository administration.** Held by the maintainer and recoverable by
  `stablekernel` organization owners.
- **Release signing material.** The private signing key used for releases lives
  only as the `CASCADE_RELEASE_GPG_KEY` repository secret and is never on the
  distribution site. The key has no passphrase: a passphrase would be stored as a
  repository secret too, the same trust boundary as the key, so it would add a
  maintained item without adding protection. The key is disposable: a successor
  who cannot reach the existing secret simply generates a new signing key,
  replaces the secret, and publishes the new public key. No key escrow is needed.
- **Release pipeline credentials.** Any tokens or secrets the release pipeline
  needs are stored as GitHub Actions secrets on the repository or organization,
  which organization owners can rotate.
  *Maintainer follow-up: document each required secret and confirm an
  organization owner can rotate it.*
- **Public verification material.** The public signing key and the verification
  procedure are published with the project so users can verify releases
  regardless of who cut them. The GPG public key is committed at
  [docs/cascade-release-public-key.asc](./docs/cascade-release-public-key.asc)
  and the verification steps are documented in
  [docs/release-verification.md](./docs/release-verification.md).

### Maintainer follow-ups for full continuity

- [x] Publish the public signing key and the user-side verification procedure.
      The GPG public key is committed at
      [docs/cascade-release-public-key.asc](./docs/cascade-release-public-key.asc)
      and the procedure is documented in
      [docs/release-verification.md](./docs/release-verification.md).
- [x] Confirm `stablekernel` organization owners can restore repository
      administration if the maintainer is unreachable. The organization has
      multiple owners and the repository has multiple administrators, so
      repository access is not a single point of failure.
- [ ] Name a backup maintainer with explicit authority to triage, merge, and
      release.
- [ ] Document every release-pipeline secret and confirm an owner can rotate it.
