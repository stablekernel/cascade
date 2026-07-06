<h1 align="center">cascade</h1>

<!-- Row 1: identity & quality -->
<p align="center">
  <a href="https://pkg.go.dev/github.com/stablekernel/cascade"><img src="https://pkg.go.dev/badge/github.com/stablekernel/cascade.svg" alt="Go Reference"></a>
  <a href="https://goreportcard.com/report/github.com/stablekernel/cascade"><img src="https://goreportcard.com/badge/github.com/stablekernel/cascade" alt="Go Report Card"></a>
  <a href="./go.mod"><img src="https://img.shields.io/github/go-mod/go-version/stablekernel/cascade" alt="Go Version"></a>
  <a href="https://stablekernel.github.io/cascade/"><img src="https://img.shields.io/badge/docs-cascade-36D0C4" alt="Docs"></a>
</p>

<!-- Row 2: project health & CI -->
<p align="center">
  <a href="https://github.com/stablekernel/cascade/actions/workflows/codeql.yml"><img src="https://github.com/stablekernel/cascade/actions/workflows/codeql.yml/badge.svg?branch=main" alt="CodeQL"></a>
  <a href="https://securityscorecards.dev/viewer/?uri=github.com/stablekernel/cascade"><img src="https://api.securityscorecards.dev/projects/github.com/stablekernel/cascade/badge" alt="OpenSSF Scorecard"></a>
  <a href="https://github.com/stablekernel/cascade/releases/latest"><img src="https://img.shields.io/github/v/release/stablekernel/cascade?sort=semver&display_name=tag" alt="Release"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License: Apache 2.0"></a>
</p>

<!-- Row 3: test & validation ladder -->
<p align="center">
  <a href="https://github.com/stablekernel/cascade/actions/workflows/validate.yaml"><img src="https://github.com/stablekernel/cascade/actions/workflows/validate.yaml/badge.svg?branch=main" alt="Tests & Lint"></a>
  <a href="https://github.com/stablekernel/cascade/actions"><img src="https://img.shields.io/badge/coverage-82.6%25-brightgreen" alt="Coverage"></a>
  <a href="https://github.com/stablekernel/cascade/actions/workflows/e2e.yaml"><img src="https://github.com/stablekernel/cascade/actions/workflows/e2e.yaml/badge.svg?branch=main" alt="Integration (act + gitea)"></a>
  <a href="https://github.com/stablekernel/cascade/actions/workflows/fleet-e2e.yaml"><img src="https://github.com/stablekernel/cascade/actions/workflows/fleet-e2e.yaml/badge.svg?branch=main" alt="Fleet E2E (live GitHub)"></a>
</p>

<p align="center">
  <img src="docs/src/assets/avatar.png" alt="The cascade mascot" width="220">
</p>

<p align="center"><strong>Declarative trunk-based CI/CD for GitHub Actions.</strong></p>

<p align="center">
  Define what to build and where to deploy in one manifest.<br> cascade generates the GitHub Actions wiring, tracks deployment state, manages releases,<br> and cascades promotions through your environments.
</p>

---

cascade is a compiler, not a control plane. You describe your environments, builds, deploys, and release policy in one manifest. cascade compiles that manifest into GitHub Actions workflows and then gets out of the way: the generated workflows run on GitHub's own runners, with no external service, agent, or daemon watching your repository.

## How it works

The manifest (`.github/manifest.yaml`) holds both the pipeline configuration and the live deployment state for every environment. You run `cascade generate-workflow` once to compile it into GitHub Actions workflows, and commit those alongside your code. From then on the generated workflows own their own execution: a merge to trunk builds and deploys to the first environment, and a `workflow_dispatch` promotes the same built artifact through the rest of the chain without rebuilding it.

Read [How Cascade works](https://stablekernel.github.io/cascade/start/how-it-works/) for the full mental model, including the release boundary and the hotfix and rollback off-ramps.

---

## Is cascade for you?

cascade earns its keep when you promote a built artifact through a chain of environments. It is a strong fit when most of these hold:

- You deploy to **two or more environments** (say dev, test, prod) and want the *same* artifact promoted through them, never rebuilt per stage.
- You are on **GitHub Actions** and would rather own your deploy logic in reusable workflows than run a separate CD platform.
- You want **promotion gates, hotfix-to-any-environment, and rollback** without hand-wiring that state machine.
- You can adopt **conventional commits** (cascade derives versions, changelogs, and the breaking-change gate from them).

It is likely overkill for a single environment with a plain build-and-release on push. A repo with no deployments at all is a different case: the no-environment mode is a supported shape that still gives you conventional-commit versioning and releases.

Already running a pipeline? See the [adoption guide](https://stablekernel.github.io/cascade/guides/adopt/) for migrating without a rewrite.

---

## Quickstart

```bash
go install github.com/stablekernel/cascade/cmd/cascade@latest
```

See [Getting started](https://stablekernel.github.io/cascade/start/getting-started/) for the pinned-version install and the `setup-cli` action, both of which most teams should use instead of a bare `@latest` install.

Write a manifest, write your build and deploy callbacks, then generate. Callbacks must exist first: the generator reads their `workflow_call` outputs to wire the rest.

```yaml
# .github/manifest.yaml
ci:
  config:
    schema_version: 1
    trunk_branch: main
    cli_version: v0.9.1
    environments: [dev, staging, prod]
    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
```

```bash
# 1. Write .github/manifest.yaml (above)
# 2. Write your build/deploy callback workflows (they must exist first)
# 3. Generate the orchestration workflows
cascade generate-workflow --config .github/manifest.yaml
# 4. Commit everything and push to trunk to run the first pipeline
```

The full walkthrough, including `cascade init` scaffolding and the four topology shapes, lives in [Getting started](https://stablekernel.github.io/cascade/start/getting-started/).

---

## What cascade generates

A `generate-workflow` run emits, unconditionally:

| File | Purpose |
|---|---|
| `.github/workflows/orchestrate.yaml` | Runs on merge to trunk: validate, build, deploy to the first environment, finalize state. |
| `.github/workflows/promote.yaml` | Manually dispatched: cascades the same built artifact through the rest of the environment chain. |
| `.github/workflows/cascade-hotfix.yaml` | Patches a single environment out of band without touching the others. |
| `.github/workflows/cascade-rollback.yaml` | Rolls an environment back to its previous deployed version. |
| `.github/actions/manage-release/action.yaml` | Composite action that creates, updates, and publishes the GitHub release. |

Opt-in companions (drift-check, PR-preview, pin-reconcile) are emitted only when their manifest block is present. See [Generated workflows](https://stablekernel.github.io/cascade/reference/generated-workflows/) for the full anatomy of each file.

---

## Capabilities

| Capability | What it does |
|---|---|
| Change detection | Builds and deploys run only when their declared `triggers` match changed paths. |
| Dependency ordering | `depends_on` and `optional_depends_on` chain builds and deploys in the right order. |
| Matrix builds | Fan a single build out over a matrix of `dimensions`, with `max_parallel` and `fail_fast`. |
| Concurrency control | Configurable group and `cancel_in_progress` on orchestrate, promote, hotfix, rollback, release, and external-update workflows. |
| Extra triggers | Attach `schedule`, `repository_dispatch`, `workflow_run`, and `merge_group` events to orchestration. |
| Least-privilege permissions | Per-callback `permissions:` blocks scope each caller job, including OIDC `id-token: write`. |
| Action pinning | `pin_mode: tag` (default) or `sha`, with an embedded action-pins manifest and an opt-in reconcile companion. |
| PR plan preview | An opt-in comment on each PR shows which builds and deploys would run. |
| Breaking-change gate | `feat!:` or `BREAKING CHANGE:` commits block the prerelease-to-release boundary unless overridden. |
| Artifact passing | The `artifact_id` output from a build is stored in state and forwarded to deploys and publish. |
| GitHub Environments | The `environments` command emits per-environment config (`required_reviewers`, `wait_timer`, `branch_policy`) for you to apply. |
| Schema enforcement | Every CLI invocation checks `schema_version` and rejects incompatible manifests with a clear error. |

Full field-by-field detail lives in the [manifest reference](https://stablekernel.github.io/cascade/reference/manifest/).

---

## Documentation

| Start here | |
|---|---|
| [Why Cascade](https://stablekernel.github.io/cascade/start/why-cascade/) | What cascade is, when to use it, and how it compares to adjacent tools. |
| [How Cascade works](https://stablekernel.github.io/cascade/start/how-it-works/) | The mental model: trunk, environment chain, release boundary. |
| [Getting started](https://stablekernel.github.io/cascade/start/getting-started/) | Install, scaffold or hand-write a manifest, and run your first pipeline. |

| Task guides | |
|---|---|
| [Adopt an existing pipeline](https://stablekernel.github.io/cascade/guides/adopt/) | Migrate without a rewrite; coexist with tools you already use. |
| [Promote a release](https://stablekernel.github.io/cascade/guides/promote/) | Trigger and watch a promotion. |
| [Run a hotfix](https://stablekernel.github.io/cascade/guides/hotfix/) | Patch one environment without touching the others. |
| [Roll back an environment](https://stablekernel.github.io/cascade/guides/rollback/) | Revert an environment to its previous version. |

| Reference | |
|---|---|
| [Manifest](https://stablekernel.github.io/cascade/reference/manifest/) | Every manifest field, its emission status, and its default. |
| [CLI](https://stablekernel.github.io/cascade/reference/cli/) | Every command, flag, environment variable, and exit code. |
| [Callback contract](https://stablekernel.github.io/cascade/reference/callbacks/) | The inputs and outputs your build/deploy/publish workflows exchange with cascade. |
| [Generated workflows](https://stablekernel.github.io/cascade/reference/generated-workflows/) | The exact file set and the anatomy of each generated workflow. |

See the [full sidebar](https://stablekernel.github.io/cascade/) for the rest, including security and internals.

---

## Conventions

cascade follows these conventions in its own codebase and in the generated workflows it produces:

- **Additive manifest changes**: new fields are always optional with sensible defaults, so existing manifest files keep working across minor version bumps.
- **Conventional commits**: commit messages follow `type: subject` (for example `feat:`, `fix:`, `docs:`), and the changelog generator reads this format.
- **Callback isolation**: generated workflows call your workflows via `workflow_call`, and cascade never reaches into your callback logic.
- **Metadata courier**: cascade passes artifact identifiers and versions between stages. It never touches your container registry, package registry, or deployment target directly.

---

## Development

```bash
# Build
go build -o cascade ./cmd/cascade

# Test (all packages)
go test ./...

# E2E tests (requires Docker)
cd e2e && go test -v -timeout 20m ./...

# Lint
golangci-lint run ./...

# Regenerate cascade's own workflows (uses itself)
go run ./cmd/cascade generate-workflow --config .github/manifest.yaml -f
```

---

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](./CONTRIBUTING.md) for development setup and workflow details.

cascade uses the [Developer Certificate of Origin](https://developercertificate.org/). Sign off each commit with `git commit -s`. By participating you agree to the [Code of Conduct](./CODE_OF_CONDUCT.md).

---

## License

Apache 2.0. See [LICENSE](./LICENSE).
