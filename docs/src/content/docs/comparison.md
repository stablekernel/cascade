---
title: Why Cascade
description: What cascade is, who it is for, and how it relates to release tooling, promotion control planes, and CI-as-code generators.
---

This page helps you decide whether cascade fits your repository, and explains how it
relates to the adjacent tools you may already use. The short version: cascade is a
compiler, not a control plane. It reads a manifest and writes plain GitHub Actions
workflows that you own. There is no platform to run, no cluster, and no agent.

## What cascade is

cascade is a Go CLI that compiles a single declarative manifest into native GitHub
Actions workflows for multi-environment release and promotion. It also derives versions
and changelogs from your Conventional Commits.

You run `cascade generate-workflow` once. From then on the generated workflows own their
own execution. The output is ordinary YAML that lives in your repository under
`.github/workflows/`. If you stop using cascade tomorrow, the workflows it wrote keep
running exactly as they did, because they are yours. Nothing about them depends on a
hosted runtime.

That is the whole identity: cascade is build-time tooling that produces artifacts you
keep, not a system you adopt and depend on at runtime.

## Who it is for, and when to use it

cascade earns its keep when you promote a built artifact through a chain of
environments. It is a strong fit when most of these hold:

- You deploy to **two or more environments** (say dev, test, prod) and want the *same*
  artifact promoted through them, never rebuilt per stage.
- You are on **GitHub Actions** and would rather own your deploy logic in reusable
  workflows than run a separate CD platform.
- You want **promotion gates, hotfix-to-any-environment, and rollback** without
  hand-wiring that state machine.
- You can adopt **Conventional Commits**, from which cascade derives versions,
  changelogs, and the breaking-change gate.

## When not to use it

cascade is likely overkill for a single environment with a plain build-and-release on
push, or for a repository with no deployments at all. (The no-environment mode still
gives you Conventional-Commit versioning and releases if you want just that.)

A few deliberate non-goals are worth stating plainly, because they shape what cascade
will and will not do for you:

- **Trunk-based only.** cascade promotes *from trunk*: you merge to one trunk branch and
  cascade promotes that line through your environments. If you run release branches or a
  GitFlow model today, adopting cascade means moving promotion onto a trunk-based flow.
  That is a deliberate shift. cascade is a practical vehicle for it, but it does not
  model long-lived release branches.
- **You own the deploy logic.** Build, deploy, validate, and publish are *your* logic,
  supplied as reusable (`workflow_call`) workflows that cascade calls with a fixed input
  contract. cascade never runs your scripts inline and never reaches into your callback
  logic.
- **It never rebuilds artifacts per stage.** cascade promotes the artifact that was
  built once, pinning each promotion to a specific SHA. It does not rebuild between
  environments.
- **It is a metadata courier.** cascade passes artifact identifiers and versions between
  stages. It never touches your container registry, package registry, or deployment
  target directly. You construct those operations yourself in your callbacks.

If you need a tool that runs your deployments for you, manages a cluster, or owns the
runtime path to production, cascade is the wrong layer. See the next section for tools
built for that job.

## How cascade relates to adjacent tools

The space around cascade is crowded, but most tools sit on a single axis. cascade sits
at the intersection of three, and on each axis it has a different goal from the
specialists there. None of the comparisons below are about better or worse; they are
about different jobs. In several cases the right answer is to use cascade *alongside*
one of these tools.

### Release, versioning, and changelogs from Conventional Commits

These tools turn your commit history into versions, changelogs, and releases.

| Tool | What it does well | How cascade differs |
|---|---|---|
| [release-please](https://github.com/googleapis/release-please) | Maintains a standing release pull request; merging it cuts the tag and release. | cascade derives versions and changelogs too, but its focus is promoting an artifact across environments rather than the standalone release-PR flow. The two pair well: you can let release-please run the release PR inside a callback while cascade owns promotion. |
| [semantic-release](https://github.com/semantic-release/semantic-release) | Fully automated version, changelog, and publish on every qualifying commit. | cascade ties releasing to a promotion lifecycle (draft, prerelease, published) rather than a single publish step. |
| [Changesets](https://github.com/changesets/changesets) | Author-written change files, strong for multi-package JS monorepos. | cascade reads Conventional Commits rather than change files, and centers environments rather than package graphs. |
| [GoReleaser](https://goreleaser.com/) | Builds and publishes Go (and other) release artifacts and packages. | cascade does not build or publish artifacts itself; it can call GoReleaser as a build or publish callback. |

These tools and cascade are **complementary, not mutually exclusive.** You can point
cascade's changelog or release step at your own workflow, or switch that step off, and
let a tool like release-please or GoReleaser keep doing what it already does inside a
reusable-workflow callback while cascade owns the promotion across environments. See the
[Adoption Guide](/cascade/adoption/) for wiring this up.

### Multi-environment promotion and progressive rollout

These tools move releases through environments at runtime, and several add progressive
rollout strategies.

| Tool | What it does well | How cascade differs |
|---|---|---|
| [Argo CD](https://argo-cd.readthedocs.io/) + [Argo Rollouts](https://argoproj.github.io/rollouts/) | GitOps continuous reconciliation for Kubernetes, with progressive rollout strategies. | cascade is a build-time generator, not a reconciling controller, and is not tied to Kubernetes. |
| [Kargo](https://kargo.akuity.io/) | Stage-to-stage promotion of "Freight" through environments. | cascade shares this promotion mental model but is not a control plane you run; it emits Actions YAML instead. |
| [Spinnaker](https://spinnaker.io/) | Mature multi-cloud deployment pipelines with rich stages. | cascade keeps the pipeline as GitHub Actions you own, rather than a separate pipeline platform. |
| [Octopus Deploy](https://octopus.com/) | Release management and deployment automation across many targets. | cascade does not run deployments or hold a server-side release database; state lives in your manifest. |
| [Harness](https://www.harness.io/) | A broad platform spanning CI, CD, and feature management. | cascade is a focused CLI, not a platform; it generates workflows and then steps out of the way. |

The important distinction across this whole row: these are **runtime control planes you
adopt.** You run them (or pay for them), they hold pipeline state, and they often assume
Kubernetes. cascade takes a different shape: it generates native GitHub Actions you keep,
holds state in your manifest in your repository, and has nothing running between
promotions. If you already operate one of these platforms and it serves you, cascade is
not trying to replace it. cascade is for teams who would rather stay inside GitHub
Actions than take on a separate runtime.

### CI-as-code generators

These tools generate or run CI configuration so you do not hand-write it.

| Tool | What it does well | How cascade differs |
|---|---|---|
| [projen](https://projen.io/) | Generates and continuously manages project config (including CI) from code. | cascade generates a narrow, promotion-focused set of workflows rather than managing whole-project config, and reads a manifest rather than a program. |
| [Dagger](https://dagger.io/) | Portable pipelines as code, executed by a custom engine. | cascade emits plain Actions YAML that runs on stock GitHub runners, with no engine to run. |
| [Earthly](https://earthly.dev/) | Repeatable, containerized build definitions. | cascade does not define builds; it orchestrates and promotes the builds your callbacks define. |

cascade overlaps with these on "do not hand-write your CI," but its goal is narrower and
more opinionated: it models multi-environment promotion specifically, and its output is
GitHub-native rather than a custom runtime or general scaffolding.

## What cascade generates

cascade emits ordinary GitHub Actions YAML and standard GitHub objects. As of today, a
generated pipeline includes:

- **Orchestrate, promote, release, and rollback workflows** that move a single artifact
  through your environments, pinned to a specific SHA and never rebuilt per stage.
- **GitHub Releases**, including release-asset upload and the release lifecycle (draft,
  prerelease, published) with release-candidate tag cleanup.
- **Merge queue** configuration on the trunk integration path.
- **Concurrency** blocks so overlapping runs do not collide.
- **A GitHub Environment gate**, threaded to your deploy callback as the `environment`
  input. (Because every deploy is a reusable-workflow caller job, the actual
  `environment:` declaration lives inside the workflow you point cascade at; see the
  [Callback Contract](/cascade/callback-contract/).)
- **Run summaries** via `$GITHUB_STEP_SUMMARY` for plan and preview output.
- **Top-level `GITHUB_TOKEN` permission scoping** on the generated workflows.

What cascade does not generate is just as important. cascade does not build or publish
your artifacts, does not run your deployments, and does not own any runtime path to
production. Those are your callbacks. For the exact inputs and outputs cascade exchanges
with your workflows, see the [Callback Contract](/cascade/callback-contract/); for the
full design and ownership boundary, see [Architecture](/cascade/architecture/).
