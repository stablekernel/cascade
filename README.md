<h1 align="center">cascade</h1>

<!-- Row 1: identity & quality -->
<p align="center">
  <a href="https://pkg.go.dev/github.com/stablekernel/cascade"><img src="https://pkg.go.dev/badge/github.com/stablekernel/cascade.svg" alt="Go Reference"></a>
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

cascade is a compiler, not a control plane. You describe your environments, builds, deploys, and release policy in one manifest. cascade compiles that manifest into GitHub Actions workflows and then gets out of the way: everything runs as native GitHub Actions, with no external service, agent, or daemon watching your repository. Your build and deploy callbacks run on whatever runners you configure, GitHub-hosted or self-hosted.

## How it works

The manifest (`.github/manifest.yaml`) holds both the pipeline configuration and the live deployment state for every environment. You run `cascade generate-workflow` once to compile it into GitHub Actions workflows, and commit those alongside your code. From then on the generated workflows own their own execution: a merge to trunk builds and deploys to the first environment, and a `workflow_dispatch` promotes the same built artifact through the rest of the chain without rebuilding it.

Read [How Cascade works](https://stablekernel.github.io/cascade/start/how-it-works/) for the full mental model, including the release boundary and the hotfix and rollback off-ramps.

---

## Quickstart

```bash
go install github.com/stablekernel/cascade/cmd/cascade@latest
```

See [Getting started](https://stablekernel.github.io/cascade/start/getting-started/) for the pinned-version install and the `setup-cli` action, both of which most teams should use instead of a bare `@latest` install. If your organization restricts Actions to an allowlist, generate with `--cli-install=binary` so the generated workflows install the CLI inline with no third-party action (same signed-release verification); see the [`--cli-install` flag](https://stablekernel.github.io/cascade/reference/cli/#installing-the-cli-in-generated-workflows).

Write a manifest, write your build and deploy callbacks, then generate. Callbacks must exist first: the generator reads their `workflow_call` outputs to wire the rest.

```yaml
# .github/manifest.yaml
ci:
  config:
    schema_version: 1
    trunk_branch: main
    cli_version: v0.16.2
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

A single `generate-workflow` run compiles the manifest into the orchestrate, promote, hotfix, and rollback workflows plus the release composite action, with opt-in companions emitted only when their manifest block is present. See [Generated workflows](https://stablekernel.github.io/cascade/reference/generated-workflows/) for the full anatomy of each file.

---

## Highlights

- **Compiler model.** One manifest compiles into a full multi-environment pipeline of native GitHub Actions workflows.
- **Single or multi-component.** A single-component repo is the default; declare more to version, promote, hotfix, and roll back each independently from one manifest, each in its own tag and state namespace. Monorepos are native, not bolted on. See [Components](https://stablekernel.github.io/cascade/guides/components/).
- **SHA-keyed promotion ladder.** Promote the exact bytes that passed the previous environment, never a per-stage rebuild. See [Promote a release](https://stablekernel.github.io/cascade/guides/promote/).
- **Security by construction.** Every caller job carries a per-callback least-privilege `permissions:` block, including OIDC `id-token: write`. See [Callback contract](https://stablekernel.github.io/cascade/reference/callbacks/).
- **Self-healing supply chain.** Third-party action pins live in one source of truth, and a reconcile companion adopts external pin bumps back into the manifest. See [Action pins](https://stablekernel.github.io/cascade/guides/action-pins/).
- **Hotfix and rollback, race-safe.** Patch or revert a single environment with correct, race-safe concurrency. Hotfix currently cherry-picks, builds, tags, and releases the fix; its generated deploy step is a placeholder, so running the deploy workflow is a manual follow-up. See [Run a hotfix](https://stablekernel.github.io/cascade/guides/hotfix/) and [Roll back an environment](https://stablekernel.github.io/cascade/guides/rollback/).

Preview a pipeline before you merge: [`simulate`](https://stablekernel.github.io/cascade/guides/simulate-and-verify/) traces what a change would build and deploy, and [`graph`](https://stablekernel.github.io/cascade/guides/visualize/) renders the environment chain.

A fuller manifest puts a few fields to work: `web` builds only after `api`, and each build and deploy runs only when its `triggers` match the changed paths.

```yaml
# .github/manifest.yaml
ci:
  config:
    schema_version: 1
    trunk_branch: main
    cli_version: v0.16.2
    environments: [dev, staging, prod]
    builds:
      - name: api
        workflow: .github/workflows/build-api.yaml
        triggers: ["api/**"]
      - name: web
        workflow: .github/workflows/build-web.yaml
        triggers: ["web/**"]
        depends_on: [api]
    deploys:
      - name: services
        workflow: .github/workflows/deploy.yaml
        triggers: ["api/**", "web/**"]
```

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

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](./CONTRIBUTING.md) for development setup and workflow details.

cascade uses the [Developer Certificate of Origin](https://developercertificate.org/). Sign off each commit with `git commit -s`. By participating you agree to the [Code of Conduct](./CODE_OF_CONDUCT.md).

---

## License

Apache 2.0. See [LICENSE](./LICENSE).
