---
title: Feature coverage matrix
description: A feature-by-feature map of how cascade is verified. Every capability is traced to its hermetic act plus gitea scenario, its live-fleet probe, and its unit coverage, with one line on what each layer proves and why both layers exist.
---

The [How cascade is tested](/cascade/testing/) page explains the two validation
layers and the reconcile gate that makes the live layer trustworthy. This page is
the detailed companion: a feature-by-feature table that traces each cascade
capability to the exact place it is exercised.

For every feature the table records four things: the hermetic act plus gitea
scenario that exercises it (under `e2e/scenarios/**` in this repository), the
live-fleet probe that exercises it (the example repository and the probe in its
`scenario-suite.yaml`), the unit coverage when that is the deliberate layer, and
one line on what the chosen layer proves. A blank cell means the feature is not
covered at that layer by design, not by omission. The "why both layers" section
below explains those choices.

:::tip[Last validated]
This matrix was last validated against the fully green live-fleet run behind `v0.5.1`:
all eleven example repos (primary, artifact-a, artifact-b, single-env, 2env, 3env, 4env,
release-only, no-env, callbacks, rollback-dispatch) passed every probe, and the shared
fail-closed reconcile gate accounted for every run in each scenario window.
:::

## Why two layers, restated for this matrix

The two layers are not redundant. Each reaches conditions the other cannot, so a
feature is placed in the layer where it can actually be exercised.

- **The act plus gitea harness is hermetic, fast, and deterministic.** It owns the
  whole environment, so it can assert emitted structure byte for byte and can
  synthesize conditions on demand: a cherry-pick guaranteed to conflict, a
  state-write race, a merge-queue lane with no configured queue. It catches logic
  and generation regressions on every pull request without touching github.com.
- **The live fleet proves real GitHub behavior the harness cannot synthesize.** A
  real release object moving from draft to prerelease to published, real
  release-candidate tag reaping, the Contents API state-write path, real cross-repo
  dispatch, real concurrency cancellation, and a hotfix whose cherry-pick base is
  read back from recorded manifest state on a real ref.

### A concrete case where the live fleet caught what the harness masked

Two behaviors looked green in the hermetic harness yet were wrong on real GitHub:

- **Hotfix base read from recorded state.** A hotfix to a pinned environment creates
  an `env/<name>` branch at the environment's recorded `base_sha` and cherry-picks
  the fix onto it. The harness localizes and commits manifest state itself, so the
  base-from-recorded-state path was satisfied artificially and the divergence between
  the recorded base and the real ref never surfaced. It surfaced only when the
  multi-environment hotfix flow ran against a real repository, where the recorded
  base and the live branch tip are independent facts.
- **No-change skip.** A re-dispatch with no watched-path change must take the
  no-change skip path. The harness could report success while the live skip behaved
  differently. The 4env `probe_concurrency` step exercises the real skip on real
  GitHub so the path is asserted where it actually runs.

A generated token-source regression in the setup step is a third example: it cleared
only under real installation tokens on the fleet, never in the token-free harness.

## Lifecycle workflows

| Feature | act plus gitea scenario | Live-fleet probe (repo) | Unit | What the layer proves |
|---|---|---|---|---|
| Orchestrate trunk build to release candidate | `01`, `02`, `03`, `04`, `34-extra-orchestrate-triggers` | every repo, orchestrate-on-merge (all 11) | `internal/orchestrate` | A trunk merge mints an RC draft and writes state, across every topology, on real Actions |
| Default promotion (env to next env) | `04`, `promote/cascade-deploy-enabled` | `promote-staging` (2env, 3env, primary) | `internal/promote` | One promotion step copies source state into the target on a real release object |
| Cascade-mode promotion (atomic multi-step) | `04-cascade-promotion` | `lifecycle` dev to prod (4env) | `internal/promote` | The full ladder advances through intermediates and publishes at the top |
| Standalone release lane (draft, prerelease, publish) | `05-publish-callback`, `37`, `38` | dispatch prerelease then release (single-env); `release-only` | `internal/release` | A real release transitions draft to prerelease to published with RC reaping |
| Hotfix clean apply | `hotfix/hotfix-clean-apply`, `hotfix-multi-commit-clean`, `hotfix-multi-env-clean`, `hotfix-rejoin` | hotfix plan, apply, PR merge, finalize (3env) | `internal/hotfix` | A pinned-env fix lands, diverges state, and rejoins on real branches and PRs |
| Hotfix cherry-pick conflict and halt | `hotfix/hotfix-conflict-resolution`, `hotfix-multi-env-conflict-halt` | `probe_hotfix_conflict` (4env) | `internal/hotfix` | A guaranteed conflict raises the conflict label and halts the downstream lane |
| Rollback to prior version or SHA | `rollback/*` (8 scenarios) | `probe_rollback` (4env), `rollback-check` (2env) | `internal/rollback` | An env rewinds, is marked diverged, and the ring snapshot advances |
| External rollback via `repository_dispatch` | | repository_dispatch rollback, state revert asserted (rollback-dispatch) | `internal/rollback` | A real `repository_dispatch` payload drives the automated rollback entry point and the target env's state is read back reverted |
| Drift check and comment | `22-verify-drift`, `27-verify-orphan`, `28-drift-check` | `probe_drift` (4env) | `internal/verify`, `internal/generate` | Generated-vs-committed drift is detected and surfaced on a real run |
| Reconcile: adopt a governed pin bump | `pin_reconcile_test.go` (`TestReconcileAdoptsBumpAndSurvivesRegen`) | | `internal/pinreconcile`, `internal/generate` | An external governed-pin bump lands in the manifest's `action_pins` and survives a regenerate |
| Reconcile companion (emitted detector plus workflow_run companion) | `pin_reconcile_test.go` (`TestReconcileCompanionAppendsOnSameRepoPR`) | | `internal/generate` | The emitted companion resolves the pull request from trusted `workflow_run` metadata and pushes the adoption commit on a real run |
| Validate gate | `14-validate-check`, `17-validate-callback` | `probe_validate` (4env); pre-build validate gate (3env) | `internal/generate` | A validate callback gates the build before it proceeds |
| Merge queue | `15-merge-queue` | `probe_merge_queue` (4env) | `internal/generate` | The merge-queue lane is emitted and runs (harness covers the no-configured-queue case) |
| Pull-request preview | `16-pr-preview` | `pr-preview-check` (2env) | `internal/generate` | The preview run fires on a PR and posts its comment |
| External update and notify (cross-repo) | `21-cross-repo-callback`, `multi-repo/*` | external-update from artifact-a and artifact-b, concurrent no-loss (primary) | `internal/external` | A satellite deploy writes the primary's shared manifest with no lost update |
| Manage-release verbs (create, update, lock, prerelease, publish, delete) | `05-publish-callback` | `manage-release-verbs` lock to prerelease, orphan delete (primary); single-env stages | `internal/release` | Each release verb mutates the real release object as specified |
| State write (Contents API) and retry-on-conflict | `08-state-push-retry` | state read-back after every step (all multi-step repos) | `internal/statewrite`, `internal/promote` | State commits land and survive a concurrent-writer race |

## Guards and registered negatives

| Feature | act plus gitea scenario | Live-fleet probe (repo) | Unit | What the layer proves |
|---|---|---|---|---|
| No-change skip | `06-no-change-skip` | `probe_concurrency` step 12 (4env) | `internal/changes` | A re-dispatch with no watched change skips, on real Actions |
| Concurrency cancellation | `07-orchestrate-concurrency` | `probe_concurrency` cancel (4env) | `internal/generate` | The older run in a per-component group is cancelled and the survivor concludes |
| Breaking-change gate | `37-release-breaking-gate`, `38-promote-breaking-gate-release-build` | `release-gates` (primary) | `internal/promote`, `internal/release` | A breaking transition is refused without the explicit allow flag |
| Promote from diverged env blocked | `rollback/rollback-marks-diverged-blocks-promote` | `rollback-check` diverged-blocks-promote (2env) | `internal/promote` | Promotion from a diverged source is refused (registered as an expected failure) |
| Allow-downgrade and prod guard | `20-promote-allow-downgrade` | `release-gates` (primary) | `internal/promote/downgrade.go` | A downgrade needs the flag, and prod needs it even when a lower env does not |
| Promote with missing source | `promote/promote-fails-missing-source` | | `internal/promote` | A promotion with no built source fails fast |
| Build failure stops promotion | `errors/build-failure-stops-promotion` | | `internal/orchestrate` | A failed build halts the chain rather than promoting a broken artifact |
| Withheld-secret callback refusal | `orchestrate/secrets-default-none` | `withheld-negative` stage 2 (callbacks) | `internal/generate` | A callee denied its secret fails, registered as an expected failure |

## Callbacks, permissions, and OIDC

| Feature | act plus gitea scenario | Live-fleet probe (repo) | Unit | What the layer proves |
|---|---|---|---|---|
| Reusable-workflow callbacks (cross-repo @ref) | `21-cross-repo-callback` | cross-repo build consolidated (primary), build callback (artifact-a, artifact-b) | `internal/generate` | A real `@ref` callee runs and its artifact consolidates into state |
| Inline callbacks (run and shell) | `orchestrate/secrets-opt-in` | callback postures (callbacks, 3env) | `internal/generate` | Inline build and deploy callbacks run with their declared posture |
| Per-callback secrets opt-in | `orchestrate/secrets-opt-in`, `secrets-default-none` | secret opt-in posture at callee (callbacks) | `internal/generate` | A secret reaches only the callee that opted in |
| Least-privilege permissions | `orchestrate/least-privilege-permissions` | least-priv posture (callbacks); gen-time wiring (3env) | `internal/generate` | Permissions are scoped to the job that needs them, not the top level |
| OIDC id-token propagation | `orchestrate/callback-permissions-oidc` | OIDC posture at callee (callbacks); gen-time `id-token: write` scoped (3env) | `internal/generate` | `id-token: write` propagates to the caller job without leaking workflow-wide |
| Callback dependency ordering (`depends_on`) | `11-job-timeouts-and-optional-deps` | needs ordering (callbacks); base to app order (3env gen-time) | `internal/generate` | A dependent callback starts only after its prerequisite concludes |
| Callback retry wrapper | | retry-wrapper jobs present (callbacks); retry shim jobs (3env gen-time) | `internal/generate` | The retry jobs are emitted and wired for `retries: N` |
| Signed auto-commit identity (`auto_commits`) | `03-three-env-repo` | auto_commits author and message (3env) | `internal/promote/auto_commit_sha*.go` | The state commit carries the configured author and message |

## Manifest schema and emitted shape

These are about what the generator emits. Several are asserted at the emission
layer on purpose, because the bytes are the contract and a live run would add no
signal beyond what the structural assertion already proves.

| Feature | act plus gitea scenario | Live-fleet probe (repo) | Unit | What the layer proves |
|---|---|---|---|---|
| Action pins (sha mode, default tag, per-action override) | `35`, `36`, `39` | | `internal/generate` | The pin mode lands the correct immutable SHA or mutable tag in the emitted bytes |
| Reserved shapes (canary and blue-green, gitops target, telemetry sink, version overrides) | `23`, `24`, `25`, `26` | | `internal/config`, `internal/generate` | A parsed-but-not-yet-generated block round-trips cleanly through verify |
| Dispatch inputs | `13-dispatch-inputs`, `20-promote-allow-downgrade` | `dispatch-inputs-check` reason threading (2env) | `internal/generate` | A dispatch input reaches consolidated state |
| Environment config emit | `29-environment-config-emit` | | `internal/environments` | The per-environment config file is emitted for the operator to apply |
| Custom changelog and contributors | `19-custom-changelog` | changelog assembly (release-only) | `internal/changelog` | The changelog and contributor section assemble from the commit range |
| Owned-job timeouts and optional dependencies | `11-job-timeouts-and-optional-deps` | | `internal/generate` | Timeouts and optional `needs` land on the right jobs |
| Notify overrides | | notify overrides reach dispatch (artifact-a, artifact-b) | `internal/generate` | Configured `notify.deploy_name` and `notify.environment` override the defaults |
| Matrix builds | `02-two-env-repo` | per-leg matrix artifacts (2env) | `internal/generate` | Each matrix leg produces its artifact |
| Native deployments API calls | `31-native-deployments` | | `internal/generate` | The deployment create, in-progress, and status calls are emitted (objects are GitHub-side) |
| App token source and mint steps | `30-app-token-source`, `orchestrate/22-release-token-bare-secret-name`, `orchestrate/23-release-token-defaults-to-state-token` | | `internal/generate` | The token-source wiring is emitted and defaults correctly (minting is GitHub-side) |

## Topologies

Each example repository is validated as its own live pipeline rather than inferred
from a general case.

| Topology | Live-fleet repository | act plus gitea scenario |
|---|---|---|
| Single environment plus standalone release lane | `cascade-example-single-env` | `09-single-env-repo` |
| Two environments, matrix, promotion, PR preview | `cascade-example-2env` | `02-two-env-repo` |
| Three environments, validate gate, env-branch handling, signed identity | `cascade-example-3env` | `03-three-env-repo` |
| Four environments, cascade mode, breaking gate, hotfix, rollback | `cascade-example-4env` | `04-cascade-promotion` |
| Release-only (no deploy environments) | `cascade-example-release-only` | none specific (release lane via `37`, `38`) |
| No-environment library shape | covered in harness only by design | `01-no-env-repo` |
| Primary plus artifact satellites (cross-repo graph) | `cascade-example-primary`, `cascade-example-artifact-a`, `cascade-example-artifact-b` | `multi-repo/*`, `21-cross-repo-callback` |
| Per-callback secrets, permissions, and OIDC posture across reusable and inline callbacks | `cascade-example-callbacks` | `orchestrate/secrets-opt-in`, `orchestrate/callback-permissions-oidc` |
| External rollback entry point (`repository_dispatch`) | `cascade-example-rollback-dispatch` | `rollback/*` |

The no-environment library shape is covered in the act plus gitea harness; a live
`cascade-example-no-env` suite also asserts that orchestrate goes straight from a
trunk merge to a minted RC with no deploy job in the run.

## Read-only commands, unit and CLI by design

These commands make no GitHub or container calls and are fully deterministic.
Placing them on the live fleet would add runtime with no behavior to assert, so
they are covered at the unit and CLI tier on purpose. This is a deliberate choice,
not a coverage gap.

| Feature | Layer | Coverage | What it proves |
|---|---|---|---|
| `cascade simulate` (what-if engine and four subcommands) | unit plus CLI | `internal/simulate/*_test.go`, `cmd/cascade/main_test.go` | The orchestration state machine replays in record-only mode and renders a deterministic diff and effect sequence |
| `cascade graph` (Mermaid render, three granularities) | unit plus CLI | `internal/graph/graph_test.go`, `cmd/cascade/main_test.go` | The manifest projects to a valid Mermaid diagram for jobs, stages, and env views |
| `internal/visualize` (view model, themes, emitter) | unit | `internal/visualize/*_test.go` | The pure render turns a view model and theme into Mermaid source deterministically |
| `cascade init` and scaffold | unit plus CLI | `internal/initcmd`, `internal/scaffold` | The scaffold self-checks through the real generator before any file is written |
| `cascade verify` (drift) | unit plus every harness scenario | `internal/verify` | Committed workflows match manifest-regenerated bytes; drift exits non-zero |
| `cascade plan` (diff preview) | unit plus CLI | `33-plan-diff`, `internal/plan` | The per-file unified diff is produced without writing files |
| `parse-config`, `schema`, `next-version`, `detect-changes`, `generate-changelog` | unit | per-package tests | Pure logic: parsing, version calculation, change detection, changelog assembly |
| `cascade status` and `status consistency` | unit plus harness | `27-verify-orphan`, `internal/status` | State is reported and orphan env branches are flagged, and deleted on the remote with `--fix` |
| `branch-protection` and `environments` emit | unit plus CLI | `internal/branchprotection`, `internal/environments` | The JSON body and env config are emitted for the operator (applying them is GitHub-side) |

## The real-GitHub platform ceiling

Some behavior depends on GitHub platform features and real cloud outcomes that
neither layer fakes. These are validated by design and inspection, asserting the
part cascade owns (the emitted structure and the orchestration around the call)
and treating the platform-enforced outcome as a contract validated by review.

- **GitHub Environment protection** (required reviewers, wait timers): cascade emits
  the `environment:` reference; the approval gate is GitHub enforcing your rule.
- **Native Deployment objects**: the create, in-progress, and status calls are
  asserted at emission; the resulting objects are GitHub-side.
- **The Verified signed-commit badge**: cascade drives a signed state commit; the
  badge is awarded by GitHub against your key.
- **GitHub App token minting**: the mint steps are asserted at emission; an
  installation token can only be minted against github.com.
- **Real cloud deploy outcomes**: cascade orchestrates your build and deploy
  callbacks; what they do against your cloud is yours to test.

## Coverage stance

Cascade does not claim full live coverage of every feature. It claims full coverage
across the layers, with a documented ceiling. Every lifecycle and guard behavior
that can run live runs live in the fleet; every emission and synthesized-condition
behavior is asserted in the act plus gitea harness; every piece of pure logic is
covered by unit tests; and the platform ceiling is validated by design.
