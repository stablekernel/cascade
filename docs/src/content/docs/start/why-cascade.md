---
title: Why Cascade
description: What Cascade is, who it is for, and how it compares to release tooling, promotion control planes, and CI-as-code generators.
---

This page helps you decide whether Cascade fits your repository, and explains how it relates to the adjacent tools you may already use. The short version: Cascade is a compiler, not a control plane. It reads a manifest and writes plain GitHub Actions workflows that you own. There is no platform to run, no cluster, and no agent.

## What Cascade is

Cascade is a Go CLI that compiles a single declarative manifest into native GitHub Actions workflows for multi-environment release and promotion. It also derives versions and changelogs from your Conventional Commits.

You run `cascade generate-workflow` once. From then on the generated workflows own their own execution. The output is ordinary YAML that lives in your repository under `.github/workflows/`. If you stop using Cascade tomorrow, the workflows it wrote keep running exactly as they did, because they are yours. Nothing about them depends on a hosted runtime.

That is the whole identity: Cascade is build-time tooling that produces artifacts you keep, not a system you adopt and depend on at runtime.

## Who it is for, and when to use it

Cascade earns its keep when you promote a built artifact through a chain of environments. It is a strong fit when most of these hold:

- You deploy to **two or more environments** (say dev, test, prod) and want the *same* artifact promoted through them, never rebuilt per stage.
- You are on **GitHub Actions** and would rather own your deploy logic in reusable workflows than run a separate CD platform.
- You want **promotion gates, hotfix-to-any-environment, and rollback** without hand-wiring that state machine.
- You can adopt **Conventional Commits**, from which Cascade derives versions, changelogs, and the breaking-change gate.

## When not to use it

Cascade is likely more than you need for a single environment with a plain build-and-release on push, where its promotion machinery would sit unused.

A repository with no deployments at all, such as a library or CLI, is a different case. Its no-environment mode still gives you Conventional-Commit versioning, changelogs, and releases out of the box. If you would otherwise wire that together yourself across several moving parts, having those aspects managed for you can make Cascade a good fit even with nothing to deploy.

A few deliberate non-goals are worth stating plainly, because they shape what Cascade will and will not do for you:

- **Trunk-based only.** Cascade promotes *from trunk*: you merge to one trunk branch and Cascade promotes that line through your environments. If you run release branches or a GitFlow model today, adopting Cascade means moving promotion onto a trunk-based flow. That is a deliberate shift. Cascade is a practical vehicle for it, but it does not model long-lived release branches.
- **You own the deploy logic.** Build, deploy, validate, and publish are *your* logic, supplied as reusable (`workflow_call`) workflows that Cascade calls with a fixed input contract. Cascade calls build and deploy as separate stages, so a pipeline that fuses them into one workflow today gets split into a build callback and a deploy callback on adoption. Cascade never runs your scripts inline and never reaches into your callback logic.
- **It never rebuilds artifacts per stage.** Cascade promotes the artifact that was built once, pinning each promotion to a specific SHA. It does not rebuild between environments.
- **It is a metadata courier.** Cascade passes artifact identifiers and versions between stages. It never touches your container registry, package registry, or deployment target directly. You construct those operations yourself in your callbacks.

If you need a tool that runs your deployments for you, manages a cluster, or owns the runtime path to production, Cascade is the wrong layer. See the next section for tools built for that job.

## How Cascade relates to adjacent tools

The space around Cascade is crowded, but most tools sit on a single axis. Cascade sits at the intersection of three, and on each axis it has a different goal from the specialists there. None of the comparisons below are about better or worse; they are about different jobs. In several cases the right answer is to use Cascade *alongside* one of these tools.

### Release, versioning, and changelogs from Conventional Commits

These tools turn your commit history into versions, changelogs, and releases.

| Tool | What it does well | How Cascade differs |
|---|---|---|
| [release-please](https://github.com/googleapis/release-please) | Maintains a standing release pull request; merging it cuts the tag and release. | Cascade derives versions and changelogs too, but its focus is promoting an artifact across environments rather than the standalone release-PR flow. The two pair well: you can let release-please run the release PR inside a callback while Cascade owns promotion. |
| [semantic-release](https://github.com/semantic-release/semantic-release) | Fully automated version, changelog, and publish on every qualifying commit. | Cascade ties releasing to a promotion lifecycle (draft, prerelease, published) rather than a single publish step. |
| [Changesets](https://github.com/changesets/changesets) | Author-written change files, strong for multi-package JS monorepos. | Cascade reads Conventional Commits rather than change files, and centers environments rather than package graphs. |
| [GoReleaser](https://goreleaser.com/) | Builds and publishes Go (and other) release artifacts and packages. | Cascade does not build or publish artifacts itself; it can call GoReleaser as a build or publish callback. |

These tools and Cascade are **complementary, not mutually exclusive.** You can point Cascade's changelog or release step at your own workflow, or switch that step off, and let a tool like release-please or GoReleaser keep doing what it already does inside a reusable-workflow callback while Cascade owns the promotion across environments. See the [adoption guide](/cascade/guides/adopt/) for wiring this up.

### Multi-environment promotion and progressive rollout

These tools move releases through environments at runtime, and several add progressive rollout strategies.

| Tool | What it does well | How Cascade differs |
|---|---|---|
| [Argo CD](https://argo-cd.readthedocs.io/) + [Argo Rollouts](https://argoproj.github.io/rollouts/) | GitOps continuous reconciliation for Kubernetes, with progressive rollout strategies. | Cascade is a build-time generator, not a reconciling controller, and is not tied to Kubernetes. |
| [Kargo](https://kargo.akuity.io/) | Stage-to-stage promotion of "Freight" through environments. | Cascade shares this promotion mental model but is not a control plane you run; it emits Actions YAML instead. |
| [Spinnaker](https://spinnaker.io/) | Mature multi-cloud deployment pipelines with rich stages. | Cascade keeps the pipeline as GitHub Actions you own, rather than a separate pipeline platform. |
| [Octopus Deploy](https://octopus.com/) | Release management and deployment automation across many targets. | Cascade does not run deployments or hold a server-side release database; state lives in your manifest. |
| [Harness](https://www.harness.io/) | A broad platform spanning CI, CD, and feature management. | Cascade is a focused CLI, not a platform; it generates workflows and then steps out of the way. |

The important distinction across this whole row: these are **runtime control planes you adopt.** You run them (or pay for them), they hold pipeline state, and they often assume Kubernetes. Cascade takes a different shape: it generates native GitHub Actions you keep, holds state in your manifest in your repository, and has nothing running between promotions. If you already operate one of these platforms and it serves you, Cascade is not trying to replace it. Cascade is for teams who would rather stay inside GitHub Actions than take on a separate runtime.

### CI-as-code generators

These tools generate or run CI configuration so you do not hand-write it.

| Tool | What it does well | How Cascade differs |
|---|---|---|
| [projen](https://projen.io/) | Generates and continuously manages project config (including CI) from code. | Cascade generates a narrow, promotion-focused set of workflows rather than managing whole-project config, and reads a manifest rather than a program. |
| [Dagger](https://dagger.io/) | Portable pipelines as code, executed by a custom engine. | Cascade emits plain Actions YAML that runs on stock GitHub runners, with no engine to run. |
| [Earthly](https://earthly.dev/) | Repeatable, containerized build definitions. | Cascade does not define builds; it orchestrates and promotes the builds your callbacks define. |

Cascade overlaps with these on "do not hand-write your CI," but its goal is narrower and more opinionated: it models multi-environment promotion specifically, and its output is GitHub-native rather than a custom runtime or general scaffolding.

## What you get

Cascade emits ordinary GitHub Actions YAML and standard GitHub objects. A generated pipeline includes:

- **Orchestrate, promote, hotfix, and rollback workflows** that move a single artifact through your environments, pinned to a specific SHA and never rebuilt per stage.
- **GitHub Releases**, including release-asset upload and the release lifecycle (draft, prerelease, published) with release-candidate tag cleanup.
- **Merge queue** configuration on the trunk integration path, when you opt in.
- **Concurrency** blocks so overlapping runs do not collide.
- **A GitHub Environment gate**, threaded to your deploy callback as the `environment` input. Because every deploy is a reusable-workflow caller job, the actual `environment:` declaration lives inside the workflow you point Cascade at.
- **Run summaries** via `$GITHUB_STEP_SUMMARY` for plan and preview output.
- **Top-level `GITHUB_TOKEN` permission scoping**, plus least-privilege per-callback `permissions:` blocks, on the generated workflows.

What Cascade does not generate is just as important. Cascade does not build or publish your artifacts, does not run your deployments, and does not own any runtime path to production. Those are your callbacks. For the exact file set and per-workflow anatomy, see [Generated workflows reference](/cascade/reference/generated-workflows/); for the exact inputs and outputs Cascade exchanges with your workflows, see the [callback contract](/cascade/reference/callbacks/); for the full design and ownership boundary, see [Architecture](/cascade/internals/architecture/).

---

**Prerequisite:** none, this is the entry point.
**Next:** [How Cascade works](/cascade/start/how-it-works/) for the mental model.
