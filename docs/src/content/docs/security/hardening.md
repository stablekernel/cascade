---
title: Security and hardening
description: Cascade's security model and a shared-responsibility guide to deploying it safely. It covers what cascade provides secure by construction versus what your organization configures in GitHub and your cloud, plus a concrete hardening checklist.
---

Cascade generates GitHub Actions workflow definitions and coordinates promotion across environments. Those workflows are committed to, and run inside, your own repositories, under your own runners, branch protection, and environment gates. Security is shared: cascade emits sound, reviewable workflow definitions, and your organization configures the GitHub and cloud controls that decide what those workflows are allowed to do.

This page describes the security model, then lays out both halves of the shared-responsibility split and gives you a checklist to harden a pipeline.

## Security model

Cascade is a build-time tool. It reads your manifest and writes workflow YAML that you commit and review in your own repository. There is no cascade-operated service in the request path at run time, and no cascade-held credential: every workflow runs under your repository's runners and your organization's policies.

Cross-repo coordination uses a same-organization, shared-token model. When one repository hands off to another (for example, a satellite repository signaling its primary), the handoff is driven by a dispatch token that you provision and hold. That token is the trust boundary for cross-repo coordination: any party that holds it can trigger the coordinated workflow. Securing the token, scoping it tightly, and pairing it with the GitHub controls below is your responsibility. Treat the token as a production credential.

Because the generated workflows live in your repository, they are deterministic and reviewable: you can read every job before you adopt it, pin it to a commit, and gate it with your own branch protection and environment rules.

## The shared-responsibility model

### What cascade provides (secure by construction)

These properties hold for generated output today:

- **Local reusable workflows are commit-pinned.** Workflows referenced as `./.github/...` are pinned to the calling commit, so your own callbacks resolve from a fixed commit rather than a moving branch.
- **Every callback is a reusable workflow.** Validate, build, and deploy callbacks run as reusable workflows referenced by `workflow:`. cascade does not emit inline scripts on your behalf, so the script your pipeline runs is code you author and review in a workflow file rather than text generated from the manifest.
- **The reusable-deploy gate boundary is surfaced at generate time.** GitHub does not allow a job-level `environment:` on a job that calls a reusable workflow. When you wire a reusable deploy, cascade warns you at generation time that the environment gate must live in the called workflow, so the requirement is explicit rather than silent.
- **Generated workflows and commits are deterministic and reviewable.** Output is plain YAML committed to your repository. You can diff it, review it, and pin it before it runs.
- **The artifact identifier is tracked end to end.** Cascade records an artifact digest in pipeline state alongside the human-readable version tag and can resolve it back later.

### What is planned (roadmap, not available today)

Treat these as future work rather than current guarantees:

- Built-in authenticated cross-repo coordination (identity or signature verification on the receiver) beyond the shared dispatch token. Until then, the token plus your GitHub controls are the boundary.
- Scoped, non-blanket secret passing by default for generated callers.
- Deploying by immutable digest by default, rather than by version tag.
- SHA-pinned third-party actions by default and provenance attestation.

### What your organization must configure

GitHub and your cloud own these controls; cascade cannot set them for you:

- **Branch protection** on your trunk: require reviews and status checks, restrict who can push, and require signed commits where appropriate.
- **Tag protection or rulesets** so release and version tags cannot be moved or forged.
- **Environment protection rules** with required reviewers, wait timers, and deployment branch or tag policies on production environments. For reusable deploys, place this gate in the called workflow.
- **CODEOWNERS on workflow files** (`.github/workflows/**`) so changes to the pipeline itself require owner review.
- **Restricted Actions settings:** an allow-list of permitted actions and reusable workflows, fork-PR run approval, and a default `GITHUB_TOKEN` that is read-only.
- **An OIDC trust policy** in your cloud scoped to specific repository, environment, and ref, issuing short-lived role sessions instead of long-lived static credentials.
- **Environment-scoped secrets** so production credentials are available only to the gated production job, not to every job in the repository.
- **Scoped, short-lived tokens** for cross-repo coordination: prefer a GitHub App over a broad personal access token, and never replicate one long-lived token across many repositories.
- **Artifact integrity controls** in your registry: immutable tags and registry RBAC so a published artifact cannot be swapped after it is produced.

## Hardening checklist

Work through this when standing up or reviewing a cascade pipeline. The order moves from the cross-repo trust boundary outward to the surrounding controls.

1. **Secure the cross-repo dispatch token.** Store it as an environment-scoped secret, scope it to only the permissions the coordination needs, prefer a GitHub App or a short-lived token over a broad personal access token, and rotate it. Do not replicate one long-lived token across many repositories.
2. **Protect trunk and tags, and add CODEOWNERS.** Require review on trunk and on `.github/workflows/**`, and protect release and version tags from being moved or deleted.
3. **Gate every production deploy with a GitHub Environment.** Deploys run as reusable workflows, so add the `environment:` gate, required reviewers, and deployment branch or tag policy inside the called workflow, since the calling job cannot carry the gate.
4. **Scope secrets to the job that needs them.** Replace blanket secret inheritance with an explicit list, and place production secrets behind an environment so only the gated production job can read them.
5. **Restrict Actions settings.** Allow-list the specific actions and reusable workflows your pipeline needs, require approval for fork-PR runs, and set the default workflow token to read-only.
6. **Use OIDC with a tightly scoped trust policy.** Scope cloud trust to the specific repository, environment, and ref, and issue short-lived sessions instead of long-lived static credentials.
7. **Pin actions and reusable workflows.** Pin third-party actions and the cascade action reference by commit SHA rather than tracking a moving tag.
8. **Protect artifact integrity in the registry.** Use immutable tags and registry RBAC so a published artifact cannot be replaced, and deploy by the recorded digest where your deploy supports it.
9. **Review the generated YAML before adopting it,** especially anything copied from example repositories, and replace example defaults (mutable pins, inherited secrets) with the hardened settings above.
