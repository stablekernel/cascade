# Assurance case

An assurance case is a structured argument, backed by evidence, that a system is
acceptably secure for its intended use. This document presents cascade's
assurance case: the claim, the threat model and trust boundaries that scope it,
the argument that secure-design principles are applied, and the argument that
common implementation weaknesses are countered.

It complements the [security requirements](./security-requirements.md), which
state what is in and out of scope, and the [security policy](../SECURITY.md),
which covers reporting and supported versions.

## Top-level claim

cascade can be adopted without introducing security risk beyond the GitHub and
cloud configuration a user already controls, provided the user follows the
documented hardening guidance. Specifically:

1. The workflow YAML cascade generates does not introduce injection or
   privilege-escalation vulnerabilities of its own.
2. cascade does not handle, store, or leak user secrets.
3. cascade's released binaries can be verified to come from the project,
   unmodified.

The rest of this document argues each part and points at the evidence.

## System and intended use

cascade is a Go command-line tool that compiles a declarative manifest into
GitHub Actions workflows. The user commits those workflows and runs them on their
own runners. cascade also runs as the per-step CLI invoked from inside the
generated workflows. cascade is not a service: it has no hosted component, no
runtime broker, and no custody of user credentials. Its intended use is by teams
on GitHub Actions who want to promote built artifacts through a chain of
environments from a trunk-based flow.

## Trust boundaries

The boundaries that scope this assurance case are:

- **Manifest authoring boundary.** The manifest is authored and reviewed by the
  user in their own repository, under branch protection. cascade treats the
  manifest as trusted configuration but still validates it against the schema and
  treats its string values as untrusted data when interpolating them into
  generated workflows.
- **Generated-workflow boundary.** The output of generation is YAML the user
  reviews and commits. Once committed, it runs under the user's GitHub
  permissions, environment gates, and token scopes. cascade's responsibility ends
  at producing safe YAML; the runtime authority is GitHub's and the user's.
- **Same-organization, shared-token boundary.** Cross-repo coordination assumes
  repositories within one organization that trust each other to the extent of a
  user-provisioned dispatch token. That token is the boundary. cascade does not
  add independent authentication between coordinating repositories; see
  [security requirements](./security-requirements.md).
- **Distribution boundary.** Released binaries cross from the project to users
  over HTTPS as GitHub Releases whose `checksums.txt` is signed with both cosign
  keyless signing and a GPG detached signature, with SLSA build provenance
  attested for the artifacts. The private GPG signing key never lives on the
  distribution site, so compromise of the distribution channel does not let an
  attacker forge a verifiable release. The verification procedure is in
  [docs/release-verification.md](./release-verification.md) and the published GPG
  public key is committed at
  [docs/cascade-release-public-key.asc](./cascade-release-public-key.asc).

## Threat model

The relevant threat actors and the threats cascade defends against:

### T1: Injection through manifest or runtime values into generated workflows

An attacker who can influence a value that flows into a generated workflow (a
manifest field, a branch name, a commit subject, an artifact identifier, an
external-update input) tries to break out of the intended context and execute
arbitrary commands or workflow expressions.

**Mitigation.** cascade treats such values as untrusted when generating
workflows and when emitting GitHub Actions step outputs. Generated steps are
constructed so that interpolated values cannot escape into shell or workflow
expression evaluation. Injection hardening for these paths has shipped and is
covered by tests. This is the primary attack surface for a workflow compiler and
receives the most attention.

### T2: A malicious or compromised coordinating repository

In the cross-repo model, a coordinating repository dispatches updates to the
primary. A compromised one could try to write bad state or trigger unintended
promotions.

**Mitigation and scoping.** This is bounded by the same-organization,
shared-token trust model. The dispatch token and the GitHub permissions around it
are the control. cascade serializes concurrent state writes so a malicious update
cannot corrupt the shared manifest through a race, but it does not, by design,
independently authenticate coordinating repositories. Users constrain blast
radius with least-privilege tokens, environment protection, and per-job
permissions, as documented in the hardening guide. This boundary is stated
plainly as out of scope for cascade-level authentication.

### T3: Tampered distribution

An attacker substitutes a malicious binary for a cascade release, or tampers with
a release in transit.

**Mitigation.** Releases are distributed over HTTPS and the release checksums,
which cover the archives, are cryptographically signed with cosign keyless
signing and a GPG detached signature, alongside SLSA build provenance attested
through GitHub. The GPG public key is published at
[docs/cascade-release-public-key.asc](./cascade-release-public-key.asc) and the
verification procedure is documented in
[docs/release-verification.md](./release-verification.md). The private GPG key is
held off the distribution site. Users can verify integrity before running a
binary.

### T4: Unsafe or undefined behavior on incompatible input

A manifest written for a different schema version is processed by a CLI that does
not understand it, producing undefined or unsafe output.

**Mitigation.** Every CLI invocation enforces the schema-version compatibility
check and fails closed with a clear error rather than guessing.

### T5: Secret exposure

cascade inadvertently reads, logs, or transmits a user secret.

**Mitigation.** cascade does not read or handle secrets. It passes only
non-secret metadata (artifact identifiers, versions, SHAs) between stages.
Secrets are GitHub Actions secrets referenced by the user's own callbacks, which
cascade never inspects.

### Out-of-scope threats

Consistent with the [security requirements](./security-requirements.md): the
security of the user's GitHub and cloud configuration, the behavior of the user's
own callbacks, inter-repository authentication beyond the shared token, the
user's registries and deployment targets, and runtime secrets management are not
cascade's responsibility. They are stated as out of scope so the boundary is
unambiguous.

## Argument: secure-design principles are applied

- **Least privilege.** cascade holds no standing credentials and runs with only
  the permissions the user grants the generated workflows. It supports
  SHA-pinning of the actions it manages and least-privilege per-job permissions
  in generated workflows so callbacks receive only the access they need.
- **Minimized attack surface.** cascade is a CLI with no network service, no
  listening port, and no persistent runtime. Its only external surfaces are the
  manifest it reads and the GitHub API it calls during a run.
- **Separation of responsibility.** Build and deploy logic stays in the user's
  callbacks, isolated behind a documented `workflow_call` contract. cascade is a
  metadata courier and never reaches into callback logic or touches deployment
  targets directly.
- **Fail closed.** Schema-version mismatches and invalid manifests are rejected
  rather than processed with undefined behavior.
- **Treat input as untrusted.** Manifest and runtime values are validated and
  handled as data when interpolated into generated workflows, regardless of their
  apparent trust level.
- **Defense in depth.** cascade's safe generation sits on top of the user's
  GitHub controls (branch protection, environment gates, scoped tokens), and the
  hardening guide directs users to enable those layers.

## Argument: common implementation weaknesses are countered

- **Injection (the dominant class for a workflow compiler).** Values
  interpolated into generated workflows and step outputs are handled so they
  cannot escape into shell or workflow-expression execution. This path is
  hardened and tested.
- **Memory-safety weaknesses.** cascade is written in Go, a memory-safe language
  with bounds checking and no manual memory management, which removes the classic
  buffer-overflow and use-after-free weakness classes by construction. (For this
  reason the OpenSSF crypto and unsafe-language criteria are not applicable.)
- **Supply-chain weaknesses.** Dependencies are pinned through Go modules with a
  checksum database; CI runs static analysis (golangci-lint and CodeQL) and
  OpenSSF Scorecard; Dependabot monitors the Go modules and GitHub Actions for
  known vulnerabilities and proposes updates; the actions used in the project's
  own workflows are SHA-pinned; and release checksums are signed with cosign
  keyless signing and a GPG detached signature, with SLSA build provenance and a
  documented verification procedure. Coverage is measured and reported.
- **Concurrency weaknesses.** Concurrent writes to the shared manifest state are
  serialized so they cannot corrupt state through a race.
- **Undefined behavior on bad input.** The schema validator and schema-version
  check reject malformed or incompatible manifests with clear errors.

## Evidence

- Source, history, and tests are public in the repository.
- Static analysis (golangci-lint, CodeQL) and OpenSSF Scorecard run in CI, and
  Dependabot tracks the Go modules and GitHub Actions; lint is part of the
  pre-merge bar.
- Unit, containerized end-to-end, and live fleet end-to-end suites exercise the
  generated workflows; see the
  [architecture documentation](https://stablekernel.github.io/cascade/architecture/).
- The OpenSSF Scorecard badge and the project's CI status are linked from the
  README.
- The release process signs the release checksums with cosign keyless signing
  and a GPG detached signature, attests SLSA build provenance, and produces a
  reproducible build. The published GPG public key
  ([docs/cascade-release-public-key.asc](./cascade-release-public-key.asc)) and
  the documented verification procedure
  ([docs/release-verification.md](./release-verification.md)) let users verify
  what they download.

## Maintaining this case

This assurance case is revisited as the threat surface changes, for example when
new manifest capability widens what flows into generated workflows. Changes land
through pull requests against this file, following the process in
[GOVERNANCE.md](../GOVERNANCE.md).
