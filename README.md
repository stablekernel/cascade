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

## How it works

The **manifest** (`.github/manifest.yaml`) is the single source of truth:

- It holds both the pipeline configuration and the live deployment state for every environment.
- You run `cascade generate-workflow` once.
- After that, the generated workflows own their own execution.

```mermaid
%%{init: {'theme':'base','themeVariables':{'fontFamily':'ui-sans-serif, system-ui, sans-serif','primaryColor':'#0E8B82','primaryBorderColor':'#36D0C4','primaryTextColor':'#F4FBFA','lineColor':'#1F9B92','clusterBkg':'transparent','clusterBorder':'#36D0C4','tertiaryColor':'#B87333'}}}%%
flowchart TD
    M["<b>.github/manifest.yaml</b><br/>config + live state"] --> G["<b>cascade generate-workflow</b>"]
    G --> WF["Generated GitHub Actions<br/>orchestrate.yaml + promote.yaml"]

    WF -- "merge to trunk" --> O

    subgraph O["Orchestrate (on merge)"]
        direction LR
        O1["Setup"] --> O2["Validate"] --> O3["Build"] --> O4["Deploy first env"] --> O5["Finalize"]
    end

    O -- "workflow_dispatch · same artifacts, never rebuilt" --> P

    subgraph P["Promote (cascade through environments)"]
        direction LR
        dev["dev"] --> test["test"] --> staging["staging"] --> prod["prod"]
    end

    P --> R

    subgraph R["Release lifecycle"]
        direction LR
        draft["draft"] --> pre["prerelease"] --> pub["published<br/>RC tags cleaned"]
    end

    classDef accent fill:#B87333,stroke:#E8702A,color:#FFF7F0;
    class M accent;
```

---

## Cross-repo artifact tracking

A primary repo can own the environment chain for artifacts that are built and versioned in other repos:

- Each external repo dispatches the primary's generated `external-update.yaml`.
- That workflow writes `{sha, version}` into `state.<env>.external.<name>` of the one shared manifest.
- Concurrent updates serialize on that manifest, then the primary cascades every source through its own environments.
- A callback can also pull in an external repo's workflow synchronously via `uses:` during the primary's run.

```mermaid
%%{init: {'theme':'base','themeVariables':{'fontFamily':'ui-sans-serif, system-ui, sans-serif','primaryColor':'#0E8B82','primaryBorderColor':'#36D0C4','primaryTextColor':'#F4FBFA','lineColor':'#1F9B92','clusterBkg':'transparent','clusterBorder':'#36D0C4','tertiaryColor':'#B87333'}}}%%
flowchart TD
    subgraph EXT["External artifact repos"]
        direction LR
        A["<b>artifact-a</b><br/>builds its own artifact"]
        B["<b>artifact-b</b><br/>builds its own artifact"]
    end

    A -- "workflow_dispatch<br/>source_repo · deploy_name · environment<br/>sha · version · artifacts" --> EU
    B -- "workflow_dispatch<br/>source_repo · deploy_name · environment<br/>sha · version · artifacts" --> EU

    subgraph PRIMARY["Primary repo"]
        direction TB
        EU["<b>external-update.yaml</b><br/>cascade external update"]
        EU -- "writes {sha, version}" --> ST["<b>.github/manifest.yaml</b><br/>state.&lt;env&gt;.external.&lt;name&gt;<br/>concurrent updates serialize"]
        ST --> PR
        subgraph PR["Promote (cascade through environments)"]
            direction LR
            dev["dev"] --> test["test"] --> staging["staging"] --> prod["prod"]
        end
    end

    CB["Primary build / deploy callback"] -. "sync uses:<br/>org/artifact-repo/.github/workflows/&lt;name&gt;.yaml@ref" .-> SYNC["External workflow<br/>invoked inline"]

    classDef accent fill:#B87333,stroke:#E8702A,color:#FFF7F0;
    class ST accent;
```

---

## Hotfix any environment

Most pipelines can only hotfix the tip, which in practice means production. cascade hotfixes **any** environment:

- It stages the fix or set of fixes on a per-environment integration branch.
- A hotfix can carry a set of commits and elevate them across the chain up to the target environment.
- It deploys the diverged environments with a clean `-rc.N.hotfix.M` version.
- It rejoins trunk the next time a trunk SHA that already contains the fixes is promoted.

The example below lands a fix on **staging** while dev, test, and prod stay exactly where they are.

```mermaid
%%{init: {'theme':'base','themeVariables':{'fontFamily':'ui-sans-serif, system-ui, sans-serif','primaryColor':'#0E8B82','primaryBorderColor':'#36D0C4','primaryTextColor':'#F4FBFA','lineColor':'#1F9B92','clusterBkg':'transparent','clusterBorder':'#36D0C4','tertiaryColor':'#B87333'}}}%%
flowchart TD
    T["<b>trunk tip</b><br/>fix already merged (roll forward first)"]

    subgraph LADDER["Environments"]
        direction LR
        dev["dev"] --> test["test"] --> staging["staging"] --> prod["prod"]
    end

    T -- "cherry-pick fix onto env/staging<br/>at staging's recorded base_sha" --> CP["<b>hotfix/staging/&lt;short-sha&gt;</b><br/>base + fix, nothing else"]
    CP --> RPR["<b>resolution PR</b> (base env/staging)<br/>cascade-hotfix · auto-merge on env checks"]
    RPR -- "on merge: build -> deploy staging only -> finalize" --> DS

    subgraph DS["staging diverged"]
        direction TB
        SV["<b>v1.4.0-rc.2.hotfix.1</b><br/>ref: env/staging<br/>base_sha: trunk SHA · patches: [fix]"]
    end

    DS -. "targets staging" .-> staging

    DS == "later promotion of a trunk SHA containing the fix<br/>clears divergence (patch-containment guard)" ==> staging

    classDef accent fill:#B87333,stroke:#E8702A,color:#FFF7F0;
    class SV accent;
```

---

## Is cascade right for your repo?

cascade earns its keep when you promote a built artifact through a chain of environments. It is a strong fit when most of these hold:

- You deploy to **two or more environments** (say dev, test, prod) and want the *same* artifact promoted through them, never rebuilt per stage.
- You are on **GitHub Actions** and would rather own your deploy logic in reusable workflows than run a separate CD platform.
- You want **promotion gates, hotfix-to-any-environment, and rollback** without hand-wiring that state machine.
- You can adopt **conventional commits** (cascade derives versions, changelogs, and the breaking-change gate from them).

It is likely overkill for a single environment with a plain build-and-release on push. A repo with no deployments at all is a different case: the no-environment mode is a supported shape that still gives you conventional-commit versioning and releases.

**Not trunk-based yet?** cascade promotes *from trunk*:

- You merge to one trunk branch and cascade promotes that line through your environments.
- If you run release branches or a GitFlow model today, adopting cascade means moving promotion onto a trunk-based flow.
- That is a deliberate shift, and cascade is a practical vehicle for it: your existing build and deploy steps become reusable-workflow callbacks, and cascade takes over the promotion, state, and release wiring on top of them.

### What adopting looks like

Adoption involves a few steps:

- Keep the build and deploy logic you already have, and wrap each as a `workflow_call` reusable workflow.
- Describe your environments and callbacks in the manifest.
- Let cascade generate the orchestration.
- Keep the tooling you rely on: point cascade's changelog or release step at your own workflow, or switch it off, while cascade owns the promotion cascade.

See the **[Adoption guide](https://stablekernel.github.io/cascade/adoption/)** for the full walkthrough on migrating an existing pipeline and wiring in tooling you already use. For reference: the [Getting Started guide](https://stablekernel.github.io/cascade/getting-started/), the [Callback Contract](https://stablekernel.github.io/cascade/callback-contract/) for the inputs cascade passes your workflows, and the [hardening guide](https://stablekernel.github.io/cascade/security/hardening/) for the GitHub setup (branch protection, environments, scoped tokens).

---

## Quick start

### 1. Install the CLI

```bash
go install github.com/stablekernel/cascade/cmd/cascade@latest
# or pin a specific version:
go install github.com/stablekernel/cascade/cmd/cascade@v0.1.0
```

> **Fastest path:** run `cascade init` to scaffold a working starter (manifest with a pinned `cli_version`, build and deploy callback stubs, a `CODEOWNERS`, and an AWS OIDC trust-policy example), then edit two values and push. Pick a shape with `--topology` (`no-env` for a CLI/library, or `two-env`/`three-env`/`four-env` for a promotion pipeline). The scaffold is a verifying starter: `cascade init` then `cascade generate-workflow` leaves `cascade verify` clean. The steps below build the same manifest by hand if you prefer.

### 2. Create the manifest

```yaml
# .github/manifest.yaml
ci:
  config:
    trunk_branch: main
    cli_version: v0.1.0

    environments: [dev, test, uat, prod]

    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers: [src/**, go.mod]

    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
        triggers: [cdk/**]
      - name: app
        workflow: .github/workflows/deploy-app.yaml
        depends_on: [app]   # waits for build-app to succeed

    changelog:
      contributors: true
```

### 3. Generate the workflows

```bash
cascade generate-workflow --config .github/manifest.yaml
# Creates: .github/workflows/orchestrate.yaml
#          .github/workflows/promote.yaml
```

Commit the generated files. cascade re-generates them whenever you update the manifest; the `-f` flag overwrites in place.

### 4. Write your callbacks

cascade calls your workflows via `workflow_call` and passes standard inputs. You own the build and deploy logic.

```yaml
# .github/workflows/build-app.yaml
on:
  workflow_call:
    inputs:
      environment:
        type: string
        required: true
      sha:
        type: string
        required: true
    outputs:
      artifact_id:
        description: 'Immutable artifact identifier (e.g., Docker image digest)'
        value: ${{ jobs.build.outputs.artifact_id }}
```

cascade is a metadata courier. You construct the registry and deploy operations yourself.

---

## Capabilities

cascade generates workflows that handle the orchestration layer. Your callback workflows handle the domain logic. The manifest gives you control over:

- **Change detection**: builds and deploys run only when their declared `triggers` match changed paths.
- **Dependency ordering**: `depends_on` chains builds and deploys in the right order.
- **Matrix builds**: fan out a single build over a matrix of inputs.
- **Per-job runner selection**: set `runs_on` at the config or per-build/deploy level.
- **Concurrency control**: configurable group and cancel-in-progress on orchestrate, promote, release, and external-update workflows.
- **Extra triggers**: attach `schedule`, `repository_dispatch`, `workflow_run`, and `merge_group` events to orchestration.
- **Dispatch inputs**: expose operator-facing manual-run inputs on the generated `workflow_dispatch`.
- **PR plan preview**: a comment on each PR shows which builds and deploys would run.
- **Merge queue lane**: a dedicated gate job runs before merge to protect trunk.
- **Action pinning**: `pin_mode: sha` emits pinned SHA references for all cascade-managed action calls. Override individual actions via `action_pins`.
- **Breaking-change gate**: `feat!:` or `BREAKING CHANGE:` commits block the prerelease-to-release boundary unless you override them.
- **Artifact passing**: the `artifact_id` output from build callbacks is stored in state and forwarded to deploys and the publish callback.
- **Publish callback**: once a release is published, a separate workflow call lets you retag RC artifacts in your registry.
- **Schema version enforcement**: every CLI invocation checks `schema_version` on the manifest and rejects incompatible manifests with a clear error.

For a no-environment project (library or CLI), omit `environments` entirely. Commits produce RC pre-releases; a `promote` dispatch publishes the final release.

---

## Promotion

<!-- Promote flow described textually; see docs/images/promote-flow.png for a visual -->

Promotions are triggered via `workflow_dispatch` on the generated `promote.yaml`.

| Mode | Behavior |
|---|---|
| `default` | Advance the chain one logical step |
| `dev-to-test` | Promote dev to test |
| `dev-to-uat` | Cascade: dev → test → uat (all intermediates updated atomically) |
| `dev-to-prod` | Full cascade through all environments |
| `uat-to-prod` | Partial cascade from uat onward |

These modes are generated from your configured environment names (`dev`, `test`, `uat`, `prod` shown here as an example); roles are positional, with the last environment as the release stage.

The same artifacts built on the first merge are promoted through the chain; nothing is rebuilt.

---

## State

The manifest tracks deployment state automatically. The `state:` section is managed by cascade; do not edit it by hand.

```yaml
ci:
  state:
    dev:
      sha: abc123
      version: v1.2.0-rc.3
      committed_at: "2025-01-15T10:30:00Z"
      committed_by: github-actions[bot]
      builds:
        app:
          sha: abc123
          artifact_id: sha256:def456
          built_at: "2025-01-15T10:30:00Z"
      deploys:
        infra:
          sha: abc123
          deployed_at: "2025-01-15T10:31:00Z"
    release:
      sha: abc000
      version: v1.1.0
  latest_release:
    version: v1.1.0
    sha: abc000
```

---

## CLI reference

| Command | Description |
|---|---|
| `generate-workflow` | Generate `orchestrate.yaml` and `promote.yaml` |
| `orchestrate setup` | Detect changes, compute version, plan execution |
| `orchestrate finalize` | Update state, manage release, commit manifest |
| `promote preflight` | Validate, compute promotions, check breaking changes |
| `promote finalize` | Update state after promotion deploys complete |
| `generate-changelog` | Create changelog from conventional commits |
| `manage-release` | Create, update, or publish GitHub releases |
| `next-version` | Calculate next semantic version |
| `detect-changes` | Determine which builds/deploys a file change triggers |
| `parse-config` | Validate and print the parsed manifest (with schema warnings) |
| `reset` | Wipe releases and state (for testing or a fresh start) |

Full flag reference: [CLI reference](https://stablekernel.github.io/cascade/cli-reference/).

---

## Documentation

| Document | Description |
|---|---|
| [Stage Graph](https://stablekernel.github.io/cascade/stage-graph/) | The mental model. Start here to see how the stages fit together |
| [Getting Started](https://stablekernel.github.io/cascade/getting-started/) | Step-by-step setup guide |
| [Configuration](https://stablekernel.github.io/cascade/configuration/) | Full manifest reference |
| [Workflows](https://stablekernel.github.io/cascade/workflows/) | Orchestrate and Promote explained |
| [CLI Reference](https://stablekernel.github.io/cascade/cli-reference/) | All commands and flags |
| [Callback Contract](https://stablekernel.github.io/cascade/callback-contract/) | How to write build/deploy/publish workflows |
| [Architecture](https://stablekernel.github.io/cascade/architecture/) | System design and internals |
| [Schema Versioning](https://stablekernel.github.io/cascade/versioning/) | Compatibility policy and migration guide |

---

## Roadmap to stable

cascade is functional and self-hosted; its own releases page shows the full pipeline running end to end. The remaining work before the v1.0.0 schema freeze falls into two areas:

- **Schema coverage.** A few GitHub Actions capabilities are modeled in the manifest shape but not yet emitted by the generator: environment gates, OIDC token configuration, and per-environment runner overrides. These sit on the direct path to v1.0.0.
- **Hardening.** This covers schema version enforcement (shipped), compatibility docs ([schema versioning](https://stablekernel.github.io/cascade/versioning/)), and more e2e coverage. The added tests confirm that the generated workflows behave correctly under edge cases such as empty builds, cross-repo coordination, and rollback to N-1.

Schema stability:

- The manifest schema field shapes were frozen in v0.1.0 as the v1 contract baseline.
- Minor versions between now and v1.0.0 may add new optional fields; no existing fields will be removed or renamed before v1.0.0.

Open work is tracked in [GitHub Issues](https://github.com/stablekernel/cascade/issues).

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
