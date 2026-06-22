---
title: Adoption Guide
description: A journey-oriented guide to adopting cascade - the mental model, building a pipeline from scratch, migrating an existing one, and wiring tools like release-please and goreleaser into reusable-workflow callbacks.
---

This guide ties the reference docs together into one path: how to think about cascade, how to build a pipeline from nothing, and how to migrate an existing pipeline (and existing tools) onto it. If you have never run cascade, start with [Getting Started](/cascade/getting-started/) for installation, then come back here for the bigger picture.

## Mental model

cascade owns **orchestration**. It generates the GitHub Actions workflows that promote a commit through your environments, hold per-environment state, pin each promotion to a specific SHA, enforce a breaking-change gate at the release boundary, and provide hotfix, rollback, and cross-repo artifact tracking. It derives versions and changelogs from your commit history.

You own **the verbs**. Build, deploy, validate, and publish are *your* logic, supplied as reusable workflows that cascade calls with a fixed input contract. cascade never runs your scripts inline; it calls a `workflow_call` reusable workflow you point at. That is the central rule of adoption: **every callback must be a reusable workflow.** Inline `run:` and `shell:` callbacks were removed.

The flow in one line: you write a manifest plus callback workflows, run `cascade generate-workflow`, and commit the generated orchestration workflows into your repository. From then on, GitHub Actions runs them: cascade orchestrates on merge, promotes between environments, and releases at the terminal environment.

cascade fits two shapes equally. A **deploy-through-environments** pipeline promotes a commit across one or more environments and releases at the end. A **release-only** repo deploys nowhere and just ships versioned releases; you omit `environments` and still get conventional-commit version derivation, changelog, and release management. Both are first-class. The rest of this guide covers the environment-promotion path in full and calls out where release-only repos diverge.

## Prerequisites

- **GitHub Actions enabled** on the repository, with trunk-based development (a single primary branch).
- **Conventional Commits - required.** cascade derives the semver bump, the changelog, and breaking-change detection entirely from Conventional Commit messages. This is not optional. Commits that do not follow the convention are not processed correctly and version derivation can fail. See [Versioning and schema compatibility](/cascade/versioning/).
- **GitHub setup**: branch and tag protection, the secrets your callbacks consume, and scoped tokens. If you deploy, also configure a GitHub environment for each deploy stage; a release-only repo has no environments to set up and skips that part. The [Security and Hardening](/cascade/security/hardening/) checklist is the authoritative list; wire it up before your first production release.
- The `cascade` CLI for local generation (Go 1.25+ to `go install`; in Actions, the setup action installs it for you). See [Getting Started](/cascade/getting-started/).

## Build a pipeline from scratch

**Fast path:** `cascade init` does the first three steps below for you. It scaffolds the manifest and the callback stubs, verifies them through the real generator, and writes them into your repository:

```bash
cascade init --topology two-env          # dev, prod
cascade init --envs staging,production   # your own ordered names
```

Pick a preset with `--topology` (`no-env`, `two-env`, `three-env`, `four-env`) or supply your own ordered list with `--envs`. Then jump to step 4 to generate and commit. The walkthrough below explains each piece `init` produces, so you understand what you are filling in. See the [CLI Reference](/cascade/cli-reference/#init) for every flag.

### 1. Choose your environments

Environments are **positional**, not named by meaning. cascade attaches no semantics to a name like `prod`; it reads the list by position:

- The **last** environment is the release stage (typically prod).
- The **second-to-last** is the prerelease environment.
- The crossing into the last environment is the publish boundary, where the breaking-change gate runs and the publish callback fires.

So `environments: [dev, test, prod]` means dev is first, test is the prerelease stage, and prod is the release stage. The names are yours to choose; only the order carries meaning. See the [Manifest Reference](/cascade/configuration/) for the full structural rules, including zero-environment (release-only) mode.

**Release-only repos:** if you ship releases without deploying anywhere, omit `environments` entirely and skip straight to releases. The breaking-change gate and publish boundary still apply at the release; you just have no environments to promote across. Scaffold this shape with `cascade init --topology no-env`, and see [No-env (release-only)](#topologies) below.

### 2. Write a minimal manifest

A three-environment manifest with one build, one deploy, optional validation, and an optional publish callback:

```yaml
ci:
  config:
    trunk_branch: master
    environments: [dev, test, prod]
    cli_version: v2.0.4

    validate:
      workflow: .github/workflows/validate.yaml

    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers: ["src/**", "Dockerfile", "go.mod"]

    deploys:
      - name: services
        workflow: .github/workflows/deploy-services.yaml
        depends_on: [app]   # receives the build's outputs as inputs

    publish:
      workflow: .github/workflows/publish.yaml

  state:
    dev: {}
    test: {}
    prod: {}
```

cascade manages `state:` and `latest_release:`; the empty skeleton is enough. See the [Manifest Reference](/cascade/configuration/) for every field.

For autocomplete and inline validation while you edit the manifest, register the JSON Schema with your editor. See [Editor support](/cascade/configuration/#editor-support).

### 3. Provide the callback workflows

Each callback is a reusable workflow with an `on: workflow_call` trigger. cascade passes a fixed set of inputs and reads back any `outputs:` you declare. The exact, full YAML for each lives in the [Callback Contract](/cascade/callback-contract/); the contract below is the summary.

| Callback | cascade passes (inputs) | You return (outputs) |
|----------|-------------------------|----------------------|
| **Validate** | `environment`, `sha`, `dry_run` | none required |
| **Build** | `environment`, `sha`, `dry_run`, plus custom inputs | `artifact_id` (recommended), plus custom |
| **Deploy** | `environment`, `sha`, `dry_run`, plus the build's declared outputs (for example `image_tag`, `artifact_id`) | custom (optional) |
| **Changelog** (custom) | `changelog_base_sha`, `head_sha`, `repo` | `changelog` |
| **Publish** | `build_name`, `old_version`, `new_version`, `sha`, `artifact_id` | none required |

Two mechanics to internalize:

- **Output chaining.** cascade parses your build workflow for declared `on.workflow_call.outputs`. When a deploy declares `depends_on: [app]`, every output the `app` build declares (say `image_tag`) is forwarded to the deploy as an input of the same name automatically. Declare outputs explicitly or they will not chain.
- **Dry run.** Every callback receives `dry_run` and should guard mutating steps with `if: ${{ !inputs.dry_run }}`.

A minimal deploy callback skeleton, receiving `image_tag` from its build dependency:

```yaml
name: Deploy Services
on:
  workflow_call:
    inputs:
      environment: { type: string, required: true }
      sha:         { type: string, required: true }
      dry_run:     { type: boolean, required: false, default: false }
      image_tag:   { type: string, required: true }   # from the app build's outputs
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: ${{ inputs.environment }}   # protection gate lives here, not on the caller
    steps:
      - uses: actions/checkout@v4
        with: { ref: ${{ inputs.sha }} }
      - if: ${{ !inputs.dry_run }}
        run: ./deploy.sh "${{ inputs.image_tag }}"
```

The `environment:` key must sit on the job **inside** your reusable workflow. GitHub Actions rejects `environment:` on a job that calls a reusable workflow with `uses:`, so the caller cascade generates cannot carry the gate; your workflow applies it. Full build, deploy, validate, and publish skeletons are in the [Callback Contract](/cascade/callback-contract/).

### 4. Generate and commit

```bash
cascade generate-workflow --config .github/manifest.yaml
```

Commit the generated orchestration workflows (`orchestrate`, `promote`, release) alongside your callbacks. Review the generated YAML before adopting it, as the hardening checklist advises.

### 5. Runtime flow

- **Orchestrate on merge.** A merge to trunk triggers the orchestrate workflow, which runs validate and build for the first environment and records state.
- **Promote between environments.** A `promote` dispatch advances the recorded SHA to the next environment, re-running the relevant callbacks against it. Promotion is SHA-pinned, so what you tested is what advances.
- **Release at the terminal environment.** Crossing into the last environment publishes the final semver release (from the RC), runs the breaking-change gate, and fires the publish callback to retag artifacts.

Two capabilities sit alongside the main flow. **Hotfix** lets you patch a released version through an integration branch without dragging unreleased trunk changes along. **Rollback** re-promotes a prior environment snapshot from recorded state. See [Architecture](/cascade/architecture/) for how state and promotion underpin both.

## Migrate an existing pipeline to cascade

Map your current pieces onto cascade's split of responsibility. The recurring question is: does cascade take this over, or does it stay yours behind a callback?

| You have today | In cascade | What changes |
|----------------|-----------|--------------|
| A deploy script or job | A **deploy** reusable-workflow callback | Move the script into a `workflow_call` workflow; cascade calls it with `environment`, `sha`, `dry_run`, plus build outputs. |
| A build/package step | A **build** callback | Same move; declare `artifact_id` (and any tags) as outputs so they chain to deploys and to publish. |
| Hand-rolled env-promotion logic (scripts gating dev to staging to prod) | cascade's **promotion cascade** | cascade owns this. Delete your promotion glue; cascade orchestrates, pins SHAs, and gates the release boundary. |
| Manual or tool-driven version bumping | **Conventional-commit-driven** version derivation | cascade owns it and it is **required**. Your bump logic goes away; commit messages drive the semver. |
| A changelog tool (release-please, git-cliff) | A **changelog** callback, or keep the tool and disable cascade's changelog | Two valid paths (see below). |
| A release tool (goreleaser) | An external **release** wired via `release.tag`, or disable cascade's release and keep the tool | Two valid paths (see below). |

The clean line: **cascade takes over orchestration, promotion, state, versioning, and the release boundary.** It does **not** take over how you build, deploy, validate, or (optionally) cut changelogs and release artifacts. Those stay yours, expressed as callbacks.

## Wiring existing tooling

### Keep release-please (or git-cliff)

Two options, both supported by the `changelog:` section of the [Manifest Reference](/cascade/configuration/):

- **Custom changelog callback.** Point `changelog.workflow` at a reusable workflow that wraps your tool. cascade passes `changelog_base_sha`, `head_sha`, and `repo`; your workflow must return a `changelog` output. cascade uses that text when it cuts the release.
- **Disable and keep yours as-is.** Set `changelog.disabled: true` and let your existing release-please workflow run independently on its own trigger. cascade stops generating a changelog; everything else (promotion, release) still works.

### Keep goreleaser

cascade has **no separate "release callback"** that receives `build_name`/`old_version`/`new_version`. Releasing is either cascade's own job or your external tool. Two options via the `release:` section:

- **External release tool.** Keep your goreleaser callback as a normal build/deploy callback that emits a tag output, and set `release.tag: goreleaser.tag` (the `callback.output` reference). cascade defers the tag to your tool's output.
- **Disable and keep goreleaser standalone.** Set `release.disabled: true` to turn off cascade's release management and run goreleaser on your own trigger.

Omitting the `release:` section entirely uses cascade's defaults: it creates releases with conventional-commit changelogs.

### Conventional commits are required

cascade's version derivation, breaking-change detection, and default changelog are **conventional-commit-only by design**. If your history contains commits that do not follow the convention, version derivation can fail and breaking changes will be missed. Adopt the convention before (or as part of) migrating. Details in [Versioning and schema compatibility](/cascade/versioning/).

## Hardening checklist pointer

Before a production promotion, confirm at minimum:

- Branch protection plus **CODEOWNERS on `.github/workflows/**`** so generated and callback workflows require review.
- **Environment protection rules** (required reviewers, branch/tag policy) on each deploy environment, declared inside the reusable deploy workflow.
- **Scoped tokens** for cross-repo dispatch and release API calls; prefer a GitHub App or short-lived token over a broad PAT.
- **Audit and integrity**: pinned actions, immutable registry tags, OIDC with a tight trust policy.

The full, ordered checklist is in [Security and Hardening](/cascade/security/hardening/). Work through it there; the points above are the highlights, not the whole list.

## Topologies

Environment count is structural. Pick the shape that matches your project:

- **No-env (release-only)** - omit `environments`; scaffold with `cascade init --topology no-env`. The fit for any repo that ships versioned releases without deploying anywhere: libraries, CLIs, SDKs, published container images, and schemas or packages consumed by other repos. You still get conventional-commit version derivation, changelog, and release management. Hotfix and rollback need two or more environments, so they do not apply here.
- **2-env** - `dev` + `prod`. Smallest promotion chain with a prerelease stage.
- **3-env** - `dev` + `staging` + `prod` (or `pre` + `staging` + `prod`). The common default.
- **4-env** - `dev` + `staging` + `pre` + `prod`. Adds a dedicated prerelease stage before production.

Worked example repositories are not published yet (examples TBD). Until then, the [Getting Started](/cascade/getting-started/) walkthrough and the [Callback Contract](/cascade/callback-contract/) skeletons are the reference implementations.
