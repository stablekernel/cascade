---
title: How Cascade is tested
description: How cascade verifies its generated pipelines across two complementary layers. A hermetic act plus gitea harness runs on every pull request, and a live fleet exercises real GitHub Actions across every supported topology. The reconcile gate makes fleet results trustworthy, and the platform ceiling is validated by design rather than execution.
---

Cascade generates GitHub Actions workflows that promote a single artifact across environments. Because those workflows are the product, cascade does not stop at unit tests on its Go packages. It runs each generated pipeline end to end and asserts the behavior it produces: the releases it cuts, the state it writes, the labels and comments it posts, and the gates it refuses to cross.

That validation happens in two complementary layers, backed by unit tests on the pure logic underneath. This page describes both layers, the mechanism that makes the live layer trustworthy, the topology matrix it covers, and the honest edge of what neither layer can execute.

## Two layers, and why both exist

Every feature is covered in the layer where it can actually be exercised. The two layers are not redundant: each reaches conditions the other cannot.

### The act plus gitea harness (hermetic, every pull request)

The harness in `e2e/` stands up a local [gitea](https://about.gitea.com/) server and runs the generated workflows in a real [act](https://github.com/nektos/act) runner, inside testcontainers. It is hermetic and fast enough to run on every pull request, with no dependency on github.com.

Each scenario generates the full workflow set from a manifest, commits it, drives a trigger (a push, a dispatch, a merged pull request), and then asserts the outcome. That outcome is either a runtime `action` plus `expect` pair against the resulting state, or a structural `contains` assertion against the emitted YAML.

Because the harness owns the whole environment, it can drive paths the live fleet cannot synthesize on demand:

- **Emission and structural shape.** Whether a flag lands the right `on:` trigger, the right `concurrency:` block, the right `permissions:` map, or the right reusable-workflow reference, asserted directly against the emitted bytes.
- **Conflict and retry injection.** A hotfix cherry-pick that is guaranteed to conflict, so the conflict label and the halted downstream lane are exercised deterministically.
- **State-write contention.** The retry-on-conflict loop on the state commit, driven without needing two real runs to collide.
- **Reserved-shape round-trips.** Manifest blocks that are parsed and validated but not yet generated are confirmed to round-trip cleanly through verify.

### The live fleet (real GitHub, every release candidate)

The fleet (`.github/workflows/fleet-e2e.yaml`) fans out to a set of purpose-built example repositories under `stablekernel/cascade-example-*`, each running a `scenario-suite.yaml` of real dispatched and side-effect runs on real GitHub Actions. It validates on every release-candidate tag and auto-promotes the candidate only when the entire fan-out is green.

The fleet proves the things that only real GitHub can prove: a real release object transitioning from draft to prerelease to published, real release-candidate tags being reaped on publish, the Contents API state-write path, cross-repo dispatch between real repositories, and real branch protection being written through a scoped token.

The fleet fans out in sequenced lanes so peak live concurrency on its one shared token stays low, accepts a `repos` selector for running a single lane during development, and is cut and promoted under a nightly gate that releases only on a fully green fleet. The [Release orchestration](/cascade/internals/release-orchestration/) page documents that machinery in full.

### Unit tests (pure logic)

Underneath both layers, the Go packages carry conventional unit tests for the pure logic: version calculation, change detection, changelog assembly, manifest parsing and validation. These run on every build and are the fastest feedback loop.

## The reconcile gate: why a green fleet means something

A live suite that dispatches runs and walks away would prove nothing, because a fire-and-forget run could fail silently and the suite would still pass. The reconcile gate closes that hole and is what makes fleet coverage trustworthy.

Every run a suite causes is registered through the `register-run` action with the conclusion it is expected to reach. That covers both runs it dispatches and side-effect runs it triggers, such as a `pull_request: closed` finalize. It also includes **registered negatives**: a guard that must refuse, such as a divergence-promote block, is registered with an expected conclusion of `failure`, so a guard that silently stops refusing is caught.

At the end of a suite, a fail-closed reconcile job (`fleet-reconcile.yaml`) enumerates every run the repository produced in the scenario window and fails if any run is in the window but missing from the suite's ledger, or reached a conclusion other than the one registered. The gate is deliberately fail-closed: a ledger that merely failed to download is treated as a failure rather than as "no registered runs", so an expected-failure run can never slip through as benign.

The result: a green suite genuinely asserted everything it caused. There is no unaccounted run.

## The topology matrix

The example repositories span the supported pipeline shapes, so each topology is validated as its own live pipeline rather than inferred from one general case.

| Example repository | Topology it exercises |
|---|---|
| `cascade-example-single-env` | Single environment, with the standalone release lane (draft, prerelease, publish, release-candidate reaping) |
| `cascade-example-2env` | Two environments, matrix builds, promotion chain, pull-request preview |
| `cascade-example-3env` | Three environments, validate gate, environment-branch handling, signed auto-commit identity |
| `cascade-example-4env` | Four environments, cascade-mode promotion, breaking-change gate, hotfix, rollback |
| `cascade-example-release-only` | Release-only repository (no deploy environments), changelog and contributor assembly |
| `cascade-example-primary` | Primary repository receiving external-update and notify handoffs from satellites |
| `cascade-example-artifact-a`, `cascade-example-artifact-b` | Satellite repositories in an artifact-dependency graph that notify the primary |
| `cascade-example-rollback-dispatch` | The automated rollback entry point, where a real `repository_dispatch` payload drives a rollback and the reverted state is read back |
| `cascade-example-monorepo` | One repository with several components, each versioned, promoted, hotfixed, and rolled back independently in its own namespace |

The `no-environment` library shape is covered today in the act plus gitea harness; the other topologies above are validated in both layers.

## The coverage stance, honestly

Cascade does not claim full live coverage of every feature. It claims full coverage across the layers, with a documented ceiling.

- **Features run live in the fleet** wherever they are live-drivable: real releases, promotion chains, hotfix and rollback flows, cross-repo coordination, and state writes.
- **Features are asserted in the act plus gitea harness** when they are about emitted structure, or when a live suite cannot synthesize the condition on demand, such as a merge-queue lane without a configured queue, a commit guaranteed to break, or a concurrent state-write race.
- **Pure logic is covered by unit tests.**

### The real-GitHub platform ceiling

Some behavior depends on GitHub platform features and real cloud outcomes that neither layer fakes. These are validated by design and inspection rather than by execution, and that is a deliberate, honest boundary:

- **GitHub Environment protection** covers required reviewers and wait timers. These are account-level protection rules, not generated YAML. Cascade emits the `environment:` reference; whether an approval gate fires is GitHub enforcing your configured rule.
- **Native Deployments API objects.** The deployment create, in-progress, and status calls are asserted at the emission level; the resulting Deployment objects are GitHub-side.
- **The Verified signed-commit badge.** Cascade can drive a signed state commit, but the Verified badge is awarded by GitHub against your key, not something a local runner can produce.
- **GitHub App token minting.** The release-token and state-token app-mint steps are guarded to the real platform and asserted at emission; an installation token can only be minted against github.com.
- **Real cloud deploy outcomes.** Cascade orchestrates your build and deploy callbacks; what those callbacks do against your cloud is yours to test.

For each of these, cascade asserts the part it owns: the generated workflow structure and the orchestration around the call. It treats the platform-enforced outcome as a contract validated by review, because executing it would require faking the platform rather than testing cascade.

## Wayfinding

**Prerequisite**: [Architecture](/cascade/internals/architecture/) for the system this testing strategy validates.
**Next**: [Feature coverage matrix](/cascade/internals/coverage-matrix/) for the feature-by-feature trace.
