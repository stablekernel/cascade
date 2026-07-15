---
title: Security and hardening
description: Cascade's trust model, the shared-responsibility split between cascade and your organization, and a concrete checklist for hardening a generated pipeline.
---

Cascade generates GitHub Actions workflow definitions and coordinates promotion across environments. Those workflows are committed to, and run inside, your own repositories, under your own runners, branch protection, and environment gates. Security is shared: cascade emits sound, reviewable, least-privilege workflow definitions, and your organization configures the GitHub and cloud controls that decide what those workflows are allowed to do.

This page is written for a security reviewer evaluating cascade before adoption, or auditing a pipeline already running it. It covers the trust model, what cascade ships secure by construction today, what remains your organization's job, and a checklist to work through.

## Trust model

Cascade is a build-time tool, not a runtime service. It reads your manifest and writes workflow YAML that you commit and review in your own repository. There is no cascade-operated service in the request path at run time, and no cascade-held credential: every workflow runs under your repository's runners, your `GITHUB_TOKEN`, and your organization's policies. If you deleted every cascade binary today, the workflows it already generated would keep running unchanged, because they are ordinary GitHub Actions YAML, not calls back to a cascade service.

### Same-organization, shared-token model

Cross-repo coordination (a satellite repository signaling its primary, or a primary dispatching to a satellite) uses a same-organization, shared-token model. One repository hands off to another by firing a `repository_dispatch` or `workflow_dispatch` call authenticated with a token you provision: typically a fine-grained personal access token or a GitHub App installation token, held as a repository or organization secret.

That token is the trust boundary for cross-repo coordination. Any party that holds it can trigger the coordinated workflow in the target repository. Cascade does not add an additional identity or signature check on top of it today: possession of the token is authorization. Treat it as a production credential, scope it as narrowly as your GitHub App or PAT model allows, and rotate it on the same cadence as any other privileged credential. See [Coordinate multiple repos](/cascade/guides/multi-repo/) for how the token is wired into `external` and `notify` blocks.

### Callback auth scope

A callback (validate, build, deploy, publish) is a reusable workflow you author, invoked with `workflow_call` from a cascade-generated caller job. The callback runs with whatever `permissions:` and `secrets:` the caller job grants it, nothing more: cascade does not pass a blanket token or an implicit credential into your callback. What a callback can do to your cloud, registry, or deployment target is entirely a function of the `permissions:` block and secrets you wire onto it (see [Least privilege](#least-privilege-per-callback-permissions) below), not anything cascade injects.

This means the callback's blast radius is bounded by two things you control: the manifest's `permissions:` entry for that callback, and the environment gate (if any) the callback's own workflow declares. Cascade surfaces the second requirement rather than hiding it: see [GitHub Environments as a gate](#github-environments-as-a-gate) below.

## Shared responsibility

### What cascade provides, secure by construction

These properties hold for generated output today, unconditionally:

- **Local reusable workflows are commit-pinned.** Workflows referenced as `./.github/...` are pinned to the calling commit, so your own callbacks resolve from a fixed commit rather than a moving branch.
- **Every callback is a reusable workflow.** Validate, build, deploy, and publish callbacks run as reusable workflows referenced by `workflow:`. Cascade does not emit inline scripts on your behalf; the script your pipeline runs is code you author and review in a workflow file, not text generated from the manifest.
- **The reusable-deploy gate boundary is surfaced at generate time.** GitHub does not allow a job-level `environment:` on a job that calls a reusable workflow. When you wire a reusable deploy, cascade warns you at generation time that the environment gate must live in the called workflow, so the requirement is explicit rather than silently dropped.
- **Third-party actions are pinned through a single manifest.** See [Action pinning](#action-pinning) below.
- **Callback permissions are least-privilege by default.** See [Least privilege](#least-privilege-per-callback-permissions) below.
- **Generated workflows and commits are deterministic and reviewable.** Output is plain YAML committed to your repository. You can diff it, review it, and pin it before it ever runs.
- **The artifact identifier is tracked end to end.** Cascade records an artifact digest in pipeline state alongside the human-readable version tag, and can resolve it back later.

### What your organization must configure

GitHub and your cloud own these controls; cascade cannot set them for you:

- **Branch protection** on your trunk: require reviews and status checks, restrict who can push, and require signed commits where appropriate. The `branch-protection` command (see the [CLI reference](/cascade/reference/cli/)) emits the matching settings, or applies them for you with `--apply` given a repo-admin token (the workflow `GITHUB_TOKEN` cannot hold repo-admin scope).
- **Tag protection or rulesets** so release and version tags cannot be moved or forged.
- **Environment protection rules** with required reviewers, wait timers, and deployment branch or tag policies on production environments. For reusable deploys, this gate lives in the called workflow, not the calling job.
- **CODEOWNERS on workflow files** (`.github/workflows/**`) so changes to the pipeline itself require owner review.
- **Restricted Actions settings**: an allow-list of permitted actions and reusable workflows, fork-PR run approval, and a default `GITHUB_TOKEN` that is read-only.
- **An OIDC trust policy** in your cloud, scoped to specific repository, environment, and ref, issuing short-lived role sessions instead of long-lived static credentials.
- **Environment-scoped secrets** so production credentials are available only to the gated production job, not to every job in the repository.
- **Scoped, short-lived tokens** for cross-repo coordination: prefer a GitHub App over a broad personal access token, and never replicate one long-lived token across many repositories.
- **Artifact integrity controls** in your registry: immutable tags and registry RBAC so a published artifact cannot be swapped after it is produced.

### What remains future work

A short, honest list of things cascade does not yet do, so you do not assume they exist:

- Built-in authenticated cross-repo coordination (identity or signature verification on the receiving end) beyond the shared dispatch token described above.
- SHA-pinning as the default `pin_mode` (SHA pinning itself ships today; only the default value is still `tag`; see [Action pinning](#action-pinning)).
- Deploying by immutable digest by default, rather than by version tag.
- Provenance attestation on generated or built artifacts.

## Action pinning

Third-party action pinning is shipped today, not a roadmap item. Two mechanisms back it:

- **`pin_mode`**, a manifest field with two values: `tag` (default) and `sha`. It controls how cascade renders every generated action reference. In `sha` mode, generated workflows pin third-party actions to a full commit SHA instead of a moving tag, closing the class of supply-chain risk where a tag is retargeted after review.
- **The embedded `action_pins` manifest**, a map of action name to pinned ref, shipped inside cascade and overridable per-project. It is the single source cascade consults when rendering every `uses:` line for an action it manages, so a pin bump is a one-line manifest change rather than a search-and-replace across every generated workflow.

```yaml
ci:
  config:
    pin_mode: sha
    action_pins:
      actions/checkout: a1b2c3d4e5f6...
```

The only thing that remains future work is changing the *default* from `tag` to `sha`. If you want SHA pinning today, set `pin_mode: sha` in your manifest; do not wait for a default change, and do not describe SHA pinning to a reviewer as "planned."

## Least privilege: per-callback permissions

Cascade emits a job-level `permissions:` block scoped to each individual callback-calling job, driven by a `permissions:` map on that callback in the manifest. This is real, generated output today, not a design goal:

```yaml
builds:
  - name: app
    workflow: ./.github/workflows/build-app.yaml
    permissions:
      contents: read
      id-token: write
```

Because the block is per-job, one callback's grant never leaks to another job in the same workflow. A build callback that only needs to read the repository and mint an OIDC token carries exactly `contents: read` and `id-token: write`, not the broader default permission set GitHub Actions grants a workflow that declares no `permissions:` block at all.

Scoped `secrets:` follow the same shape: a callback's `secrets:` entry names exactly the secrets that callback receives, rather than the reusable-workflow default of inheriting every secret in scope. Combine the two and a compromised or misbehaving callback is bounded to the permissions and secrets its own manifest entry names, not the union of everything the pipeline touches.

## OIDC

The per-callback `permissions:` block is also how cascade wires OpenID Connect. Setting `id-token: write` on a callback's `permissions:` map is the standard GitHub Actions mechanism for letting that job request a short-lived OIDC token, which your cloud provider's trust policy exchanges for a scoped, time-limited role session. Because the grant is per-callback, only the callbacks that actually deploy or publish need to carry `id-token: write`; a validate or lint callback does not.

Cascade does not configure the receiving side: the OIDC trust policy in AWS, GCP, or Azure that decides which repository, environment, and ref may exchange a token for a role is a control you own (see [Shared responsibility](#what-your-organization-must-configure) above). Scope that trust policy tightly; a `permissions: id-token: write` grant is only as safe as the trust policy on the other end of the exchange.

## GitHub Environments as a gate

GitHub Environments are the shipped answer for gating a deploy with required reviewers, wait timers, and branch or tag restrictions. The `gha_environment` field on an environment's `environments` entry wires a cascade-managed environment (native GitHub deployments, `environment_url`) onto that environment's deploy jobs, and the `environments` command emits the matching per-environment settings (`required_reviewers`, `wait_timer`, `branch_policy`) for an operator to apply through the GitHub API or UI.

The one structural caveat: GitHub does not allow a job-level `environment:` on a job that calls a reusable workflow. A reusable deploy's gate has to live inside the called workflow, and cascade warns at generation time when a manifest wires a reusable deploy without one. See [Add or change environments](/cascade/guides/environments/) for the full setup walkthrough.

## Verifying cascade releases

The controls above harden the pipelines cascade generates. This section covers the other supply-chain question: proving that the cascade binary you install is the one this repository's release workflow built. Every release ships three independent verification paths, and all three fail closed.

Note the boundary: these guarantees cover cascade's own release artifacts. Provenance for artifacts your generated pipeline builds remains your registry's job (see [What remains future work](#what-remains-future-work)).

### Cosign signatures (recommended)

Releases are signed with keyless [cosign](https://github.com/sigstore/cosign) via Sigstore, so there is no key to distribute or manage. Download `checksums.txt`, `checksums.txt.bundle`, and the archives from the [release page](https://github.com/stablekernel/cascade/releases), then verify the checksums file:

```bash
cosign verify-blob \
  --bundle=checksums.txt.bundle \
  --certificate-identity-regexp='^https://github.com/stablekernel/cascade' \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
  checksums.txt
```

The bundle carries the signature and the Sigstore-issued certificate together; cosign checks the chain and confirms the signature was produced by a GitHub Actions run in this repository. Then confirm the archives match:

```bash
sha256sum -c checksums.txt
```

### GPG signatures

If you prefer traditional GPG, import the maintainer key shipped at [`docs/cascade-release-public-key.asc`](https://github.com/stablekernel/cascade/blob/main/docs/cascade-release-public-key.asc), then verify the `.asc` signature from the release page:

```bash
gpg --import docs/cascade-release-public-key.asc
gpg --verify checksums.txt.asc checksums.txt
sha256sum -c checksums.txt
```

### SLSA build provenance

Releases also carry SLSA provenance in GitHub's attestation store, verifiable with the GitHub CLI:

```bash
gh attestation verify cascade_VERSION_linux_amd64.tar.gz \
  --repo stablekernel/cascade \
  --certificate-identity https://github.com/stablekernel/cascade/.github/workflows/release.yaml@refs/tags/vVERSION
```

This proves the artifact was built by the release workflow for that exact tag, not on someone's laptop.

### Reproducing the build

Builds are bit-for-bit reproducible with GoReleaser. Check out the release tag, use the Go toolchain pinned in `go.mod`, and build with the release flags:

```bash
git clone https://github.com/stablekernel/cascade.git && cd cascade
git checkout vVERSION
goreleaser build --single-target --clean --skip-post-hooks --id cascade
sha256sum dist/cascade_linux_amd64/cascade
```

A checksum matching the official `checksums.txt` proves the release binary corresponds to the tagged source.

## Hardening checklist

Work through this when standing up or reviewing a cascade pipeline. The order moves from the cross-repo trust boundary outward to the surrounding controls.

1. **Secure the cross-repo dispatch token.** Store it as an environment-scoped secret, scope it to only the permissions the coordination needs, prefer a GitHub App or a short-lived token over a broad personal access token, and rotate it. Do not replicate one long-lived token across many repositories.
2. **Protect trunk and tags, and add CODEOWNERS.** Require review on `main` and on `.github/workflows/**`, and protect release and version tags from being moved or deleted. Run `branch-protection --apply` with a repo-admin token, or apply the emitted JSON manually.
3. **Turn on `pin_mode: sha`.** SHA pinning is shipped; it just is not the default. Set it explicitly if your threat model includes a retargeted third-party action tag.
4. **Scope every callback's `permissions:` and `secrets:`.** Do not lean on reusable-workflow secret inheritance. List exactly the secrets and permission scopes each callback needs, and add `id-token: write` only to callbacks that actually deploy or publish.
5. **Gate every production deploy with a GitHub Environment.** Set `gha_environment` on that environment's `environments` entry, run the `environments` command, and apply the emitted settings. For reusable deploys, place the `environment:` gate inside the called workflow.
6. **Scope your cloud's OIDC trust policy narrowly.** Restrict it to the specific repository, environment, and ref that should be allowed to exchange a token, and issue short-lived sessions instead of long-lived static credentials.
7. **Restrict your repository's Actions settings.** Allow-list the specific actions and reusable workflows your pipeline needs, require approval for fork-PR runs, and set the default workflow token to read-only.
8. **Protect artifact integrity in the registry.** Use immutable tags and registry RBAC so a published artifact cannot be replaced, and deploy by the recorded digest where your deploy target supports it.
9. **Review the generated YAML before adopting it**, especially anything copied from example repositories, and replace example defaults (a `tag` pin mode, inherited secrets) with the hardened settings above.

## Wayfinding

**Prerequisite:** [Generated workflows](/cascade/reference/generated-workflows/) for the anatomy of what these controls apply to.

**Next:** [Architecture](/cascade/internals/architecture/) for how these boundaries fit the rest of the system design.
