# Security requirements

This document states what users can and cannot expect from cascade with respect
to security. It complements the [security policy](../SECURITY.md), which covers
supported versions and how to report a vulnerability, and the
[hardening guide](https://stablekernel.github.io/cascade/security/hardening/),
which gives a step-by-step checklist for configuring GitHub safely. Read this
document to understand the trust model and the boundary of cascade's
responsibility before adopting it.

## What cascade is, in security terms

cascade is a build-time compiler. It reads a manifest and emits GitHub Actions
workflow YAML that you commit to your own repository and run on your own runners.
cascade is not a hosted service, does not run at deploy time, does not hold your
credentials, and does not act as a broker between your repository and your
deployment targets. This shapes the entire security model: the workflows cascade
generates run with whatever permissions, tokens, branch protection, and
environment gates you configure in your own GitHub organization.

Securing a cascade deployment is therefore a shared responsibility. cascade is
responsible for generating safe workflow YAML and for the integrity of its own
released binaries. You are responsible for the GitHub and cloud configuration the
generated workflows run within.

## Trust model

### Same-organization, shared-token model

cascade's cross-repo coordination assumes a same-organization trust model. When a
primary repository owns an environment chain for artifacts built in other
repositories, those repositories coordinate through a dispatch token that you
provision. That token is the trust boundary. cascade assumes the repositories
participating in a cascade are operated by the same organization and trust each
other to the extent that the shared token allows. cascade does not add an
independent authentication or authorization layer on top of GitHub's; the
identity and permission model is GitHub's, scoped by the tokens and environment
protections you configure.

This means callback authentication between coordinating repositories is out of
scope for cascade. If you need to constrain what a coordinating repository can
do, you do it with GitHub's mechanisms: least-privilege tokens, environment
protection rules, branch protection, and per-job permissions on the callbacks.

### What you control

The security of a running cascade pipeline is determined by configuration you
own:

- The dispatch token and its scope.
- Branch protection on the trunk branch.
- GitHub environment protection rules and required reviewers on deployment
  environments.
- The permissions block and secrets available to your build and deploy
  callbacks.
- Whether generated workflows pin actions by SHA (cascade supports
  `pin_mode: sha` to emit SHA-pinned references for the actions it manages).

The [hardening guide](https://stablekernel.github.io/cascade/security/hardening/)
walks through configuring each of these.

### What cascade controls

- The correctness and safety of the workflow YAML it generates, including
  hardening against injection in the values it interpolates into generated
  workflows.
- The integrity of cascade's own released binaries, distributed over HTTPS as
  GitHub Releases whose checksums (covering the archives) are signed with cosign
  keyless signing and a GPG detached signature, with the verification procedure
  documented in [release-verification.md](./release-verification.md) and the GPG
  public key published at
  [cascade-release-public-key.asc](./cascade-release-public-key.asc).
- Enforcing the schema-version compatibility check so a CLI version refuses to
  operate on a manifest shape it does not understand, failing closed with a
  clear error rather than producing undefined output.

## In scope

cascade takes responsibility for:

- **Safe generation.** Generated workflows are constructed so that values flowing
  from the manifest and from the runtime context are handled safely, including
  guarding against shell and workflow-expression injection in generated steps.
- **No silent credential handling.** cascade does not read, store, or transmit
  your secrets. Secrets are GitHub Actions secrets that your callbacks reference;
  cascade only passes non-secret metadata (artifact identifiers, versions, SHAs)
  between stages.
- **Integrity of distribution.** Released binaries are published over HTTPS, and
  the release checksums that cover the archives are signed with both cosign
  keyless signing and a GPG detached signature, accompanied by SLSA build
  provenance. The published GPG public key
  ([cascade-release-public-key.asc](./cascade-release-public-key.asc)) and the
  step-by-step verification procedure
  ([release-verification.md](./release-verification.md)) let you verify what you
  downloaded. The private GPG signing key is never stored on the distribution
  site.
- **Fail-closed version handling.** A manifest with an incompatible schema
  version is rejected rather than processed with undefined behavior.
- **Coordinated vulnerability response.** Vulnerabilities reported privately are
  triaged and fixed under the timelines in the [security policy](../SECURITY.md).

## Out of scope

The following are explicitly not cascade's responsibility. Treating them as such
would be a security mistake:

- **Your GitHub and cloud configuration.** Branch protection, environment gates,
  token scoping, OIDC trust policies, and runner security are yours to configure.
  cascade generates workflows that respect them, but cannot enforce them on your
  behalf.
- **The contents and behavior of your callbacks.** cascade calls your build and
  deploy workflows through a documented contract and never inspects or modifies
  their logic. The security of what those workflows do (what they pull, what they
  push, what they have access to) is yours.
- **Inter-repository authentication beyond the shared token.** cascade assumes a
  same-organization, shared-token trust model. It does not authenticate
  coordinating repositories independently; that boundary is the token you
  provision and the GitHub permissions around it.
- **Your registries and deployment targets.** cascade never touches your
  container registry, package registry, or deployment target directly. It is a
  metadata courier.
- **Runtime secrets management.** cascade does not manage, rotate, or store
  secrets. Use GitHub Actions secrets and your organization's secret management.

## Reporting

If you find a security issue in cascade itself (for example a way to make the
generator emit unsafe workflow YAML, or a flaw in release integrity), report it
privately as described in the [security policy](../SECURITY.md). Do not open a
public issue for security vulnerabilities.
