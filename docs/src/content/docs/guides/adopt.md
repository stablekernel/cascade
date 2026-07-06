---
title: Adopt an existing pipeline
description: Map an existing CI/CD pipeline onto cascade, keep the tools you already use, and split a monolithic workflow into callbacks.
---

This guide is for a repository that already ships code through some pipeline today. It shows how to map your existing stages onto cascade's callbacks, keep tools like release-please or goreleaser, and split a monolithic workflow apart.

## When to adopt vs start fresh

If you have no pipeline yet, skip this page. Go straight to [Getting Started](/cascade/start/getting-started/) and build a green-field pipeline there; re-walking that tutorial here would just duplicate it.

Come back to this page once you have:

- An existing lint/test, build, or deploy job in GitHub Actions (or another CI system).
- A release tool such as release-please, git-cliff, or goreleaser you want to keep.
- A single monolithic workflow that does everything in one pass, which you need to split before cascade can orchestrate it.

## Map your existing stages to callbacks

cascade owns orchestration: promotion, state, versioning, and the release boundary. You keep the verbs: build, deploy, validate, and publish stay your logic, each supplied as a `workflow_call` reusable workflow that cascade calls with a fixed input contract.

| You have today | In cascade | What changes |
|----------------|-----------|--------------|
| A lint/test job | A **validate** callback | Move the checks into a `workflow_call` workflow; cascade calls it with `environment`, `sha`, `dry_run` before build. If validate runs in the same pass as build and deploy today, see [Split a monolithic pipeline](#split-a-monolithic-pipeline). |
| A build/package step | A **build** callback | Move the step into a `workflow_call` workflow; declare `artifact_id` (and any tags) as outputs so they chain to deploys and to publish. |
| A deploy script or job | A **deploy** callback | Move the script into a `workflow_call` workflow; cascade calls it with `environment`, `sha`, `dry_run`, plus the build's declared outputs. If build and deploy are fused today, see [Split a monolithic pipeline](#split-a-monolithic-pipeline). |
| A release-tagging or retag step | A **publish** callback | Move it into a `workflow_call` workflow; cascade calls it once per build at the release boundary with `build_name`, `old_version`, `new_version`, `sha`, `artifact_id`. |
| Hand-rolled env-promotion scripts | cascade's promotion chain | Delete your promotion glue. cascade orchestrates, pins SHAs, and gates the release boundary for you. |
| Manual or tool-driven version bumping | Conventional-commit-driven version derivation | Your bump logic goes away; commit messages drive the semver, and this is required, not optional. |
| A changelog tool (release-please, git-cliff) | A **changelog** callback, or keep the tool standalone | See [Keep release-please or git-cliff](#keep-release-please-or-git-cliff). |
| A release tool (goreleaser) | An external release via `release.tag`, or keep the tool standalone | See [Keep goreleaser](#keep-goreleaser). |

Every callback is a reusable workflow with an `on: workflow_call` trigger; see the [Callback contract](/cascade/reference/callbacks/) for the full input/output shape of each callback type. A minimal manifest expressing the mapping above:

```yaml
project: my-service
schema_version: 1
trunk_branch: main
cli_version: v0.9.1

environments: [dev, staging, prod]

validate:
  workflow: .github/workflows/validate.yaml

builds:
  - name: app
    workflow: .github/workflows/build-app.yaml
    triggers: ["src/**", "Dockerfile", "go.mod"]

deploys:
  - name: app
    workflow: .github/workflows/deploy-app.yaml
    depends_on: [app]
```

## Keep release-please or git-cliff

Two supported paths, both configured under `changelog:` in the [manifest reference](/cascade/reference/manifest/):

- **Wrap it as a callback.** Point `changelog.workflow` at a reusable workflow that wraps your tool. cascade passes `changelog_base_sha`, `head_sha`, and `repo`; your workflow returns a `changelog` output that cascade uses when it cuts the release.
- **Disable and keep it standalone.** Set `changelog.disabled: true`. Your existing changelog workflow keeps running on its own trigger; cascade stops generating a changelog, and everything else (promotion, release) still works.

## Keep goreleaser

cascade has no dedicated "release callback" that receives `build_name`/`old_version`/`new_version`. Releasing is either cascade's own job or your external tool, configured under `release:`:

- **External release tool.** Keep your goreleaser step as a normal build or deploy callback that emits a tag output, then set `release.tag` to that `callback.output` reference. cascade defers the tag to your tool's output.
- **Disable and keep goreleaser standalone.** Set `release.disabled: true` to turn off cascade's release management and run goreleaser on its own trigger.

Omitting `release:` entirely uses cascade's defaults: it creates releases with conventional-commit changelogs.

## Split a monolithic pipeline

A common starting point is one workflow or job that lints, tests, builds, deploys, and tags a release in a single pass, often rebuilding the artifact at every environment. cascade cannot orchestrate that shape directly, so splitting it apart is the main adaptation work of adopting cascade.

cascade calls each stage separately: validate, then build, then deploy, then (at the release boundary) publish. Split your monolith along those seams:

- **Validate** (the lint/test portion) becomes its own callback that cascade runs before build.
- **Build** and **deploy** become separate callbacks. This split is load-bearing, not stylistic: cascade builds the artifact once, on the first environment, and promotes that same SHA-pinned artifact through every later environment, running only deploy there. A build step still living inside deploy would rebuild at every environment and break that guarantee.
- **Publish** (the release-tagging or retag step) becomes its own callback that fires once per build at the release boundary.

To make the build/deploy split:

1. **Extract the build** into its own `workflow_call` workflow that declares its artifact identifier (`artifact_id`, and any tag like `image_tag`) under `on.workflow_call.outputs`. cascade captures `artifact_id` into state and forwards declared outputs to dependent deploys.
2. **Extract the deploy** into its own `workflow_call` workflow that receives that identifier as an input of the same name. With `depends_on: [<build>]`, cascade chains the build's outputs into the deploy automatically.
3. **Stop rebuilding in deploy.** Remove any build steps from the deploy path. The deploy consumes the artifact identifier it is handed; the GitHub Environment gate lives on the job inside this deploy workflow, not on the caller cascade generates.

The result is the same logic you have today, separated along the seams cascade promotes and releases across. See the [Callback contract](/cascade/reference/callbacks/) for full validate, build, deploy, and publish skeletons.

## Choose a topology

Environment count is structural, and positional: the last environment is always the release stage, the second-to-last is the prerelease stage. Pick the shape that matches your project:

| Topology | Environments | Fits |
|----------|---------------|------|
| No-env | `environments` omitted | Libraries and CLIs that release without deploying anywhere. |
| 2-env | `dev`, `prod` | Smallest promotion chain with a prerelease stage. |
| 3-env | `dev`, `staging`, `prod` | The common default. |
| 4-env | `dev`, `staging`, `pre`, `prod` | Adds a dedicated prerelease stage before production. |

`cascade init --topology <name>` scaffolds any of these for you, or use `--envs` for a custom ordered list (for example `--envs staging,production`). See [Add or change environments](/cascade/guides/environments/) once your topology is running and you need to reshape it.

---

**Prerequisite:** [Getting Started](/cascade/start/getting-started/) for install and a first pipeline.
**Next:** [Add or change environments](/cascade/guides/environments/).
