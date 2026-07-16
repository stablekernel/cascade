# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Entries from 0.2.1 onward are derived from the conventional-commit history, the
same source the release automation uses to build the notes on each
[GitHub Release](https://github.com/stablekernel/cascade/releases), so this
file and the release notes always tell the same story. The manifest schema is
versioned with a single integer `schema_version`; the schema-version
compatibility policy is documented in
[versioning and schema compatibility](https://stablekernel.github.io/cascade/reference/versioning/).
A `Migration` section is added to any release that bumps `schema_version`.

## [Unreleased]

### Fixed

- **rollback:** Drive a component-scoped rollback from the component's resolved
  configuration. The runtime narrowed only the environment ladder onto the root
  config, so a component overriding `deploys` had its rollback planned against
  the repo-global deploy set: finalize gated the state write on root deploy
  names the component's workflow never runs, and `--deployable` accepted a root
  deployable while rejecting the component's own. The resolved component config
  is now swapped in whole, so every value the generator emitted the component's
  rollback workflow from is the value the runtime plans and gates against

- **rollback:** Refuse an environment-scoped rollback of a hotfix-diverged
  environment. The rollback overwrote the divergence record (`ref: env/<env>`,
  `base_sha`, `patches`) that authorizes the rejoin teardown, so the hotfix's
  integration branch, tags, and release objects leaked forever and the stale
  patches misled the re-promotion containment gate. Rollback now fails fast at
  preflight and directs the operator to rejoin the environment first; a
  rollback-diverged environment can still be rolled back again, and stale
  patches never survive onto a rolled-back version

- **promote:** The promote runtime (preflight, promotion, and finalize) now
  operates on a component's fully resolved config instead of copying only its
  `environments` subset from the root config. Previously a component that
  overrode `deploys:` had its generated deploy jobs gated on deploy names the
  runtime never emitted, so every deploy skipped deterministically while the
  promotion still recorded success; the downgrade gate failed open on
  prefix-tagged component versions (the root grammar cannot parse them); a
  component `prerelease_token` was never stripped at publish; and a
  component-scoped finalize looked up root deploy names in the
  `DEPLOY_RESULT_*` variables the workflow sets from the component's own
  deploys, advancing environment state with no deploy gate at all. As a
  fail-safe, the generated finalize step now forwards preflight's planned
  deploy set (`DEPLOYS_TO_RUN`) and `promote finalize` refuses the state write
  when planned deploys all report skipped or unreported, so a promotion whose
  deploys silently did not run can never record success

- **version:** `next-version --component` now derives its commit scope through
  the same helper as `orchestrate`, so the preview includes the component's
  `extra_paths`/`shared_paths`. Previously a commit touching only a declared
  shared path was invisible to the preview, which reported a lower version
  than the pipeline actually mints. Component tag-lookup failures now surface
  as errors instead of silently previewing from an empty tag namespace

- **config:** Bump `DefaultCLIVersion` to v0.16.2 and remove the test that
  required the const to equal the repository's latest stable tag. The tag is
  cut before the commit that bumps the const can land, so the guard failed on
  main, on every PR, and inside the release's own test job, blocking the very
  release that would have satisfied it. The shape guard (stable `vX.Y.Z` only)
  and the docs-examples sync check remain
- **config:** Shape-validate every manifest value that is spliced into
  emitted GitHub Actions contexts, closing the class of one-off holes found
  field by field in earlier audits. New checks at `cascade lint` and
  generation time: schedule cron expressions (five-field GHA grammar),
  `extra_triggers` repository_dispatch and workflow_run event types,
  tag_grammar prefixes may not begin with a hyphen (top-level AND
  per-component; a leading hyphen reached `git tag -l` argv and failed every
  tag lookup with exit 129), `git.user_name`/`user_email` (double-quoted
  shell splice), `gpg_key_id`/`gpg_key_secret` (GitHub secret-name grammar),
  `environment_url` (http(s), no quotes or whitespace), `notify.*`,
  `trunk_branch` and `external[].ref` (git-ref charset), `manifest_file` and
  `manifest_key`, token expressions, `concurrency.group`, callback workflow
  references (`changelog`, `publish`, `release_build`, external deploys and
  `on_update`), per-callback `secrets:` map names, operator input keys and
  values, `artifact_upload` globs and download names, trigger globs and
  `shared_paths`/`extra_paths`, `cli_version`, `release_build.tag` output
  names, and multiline dispatch-input defaults (previously folded silently or
  broke the document). Shared shape helpers live in
  `internal/config/validate_shapes.go`.
- **config:** Validate every component's resolved configuration with the same
  rules as the top level. Previously a per-component override (a build name,
  an environment, a notify block) bypassed validation entirely and reached
  the generator unchecked; inherited values are still reported once.
- **generate:** Escape emitted free-form scalars instead of splicing them
  raw: `workflow_run` workflow names and paths-filter globs go through YAML
  single-quote escaping (an apostrophe in a workflow display name previously
  emitted an unparseable document), operator input values in orchestrate
  `with:` blocks are single-quoted (a colon or JSON snippet previously
  restructured the mapping), and `environment_url` is emitted in shell single
  quotes so a `$` in a URL query string is no longer silently expanded to an
  empty string on every deploy.
- **git:** Pass `--` before the tag pattern in prefix-scoped `git tag -l`
  lookups as defense in depth beneath the new prefix validation.

### Added

- **test:** A durable emitted-field guard: a reflection walk over the
  manifest schema forces every string-carrying field to be classified as
  either emitted (with its validated shape, exercised by an adversarial
  hostile-value battery plus a generate-and-reparse round trip) or explicitly
  not emitted with the reason. A new manifest field fails CI until it is
  classified, so an unvalidated splice can no longer ship unnoticed. A new
  e2e scenario (68-emitted-value-shapes) exercises apostrophe workflow names,
  cron and dispatch types, a custom git identity with GPG signing, and a
  `$`-carrying environment_url end to end.

- **hotfix:** Report the patch bump (`v1.3.0` -> `v1.3.1`) as the plan's version
  candidate when the target environment holds a published base version, matching
  what finalize actually allocates. Previously `hotfix plan` echoed the current
  published version back in its `Version:` line and in the
  `hotfix_version_candidate` JSON and GHA outputs, because the hotfix segment was
  silently dropped when rendered without a pre-release
- **generate:** Treat a release artifact that omits `required:` as required,
  matching the documented default. Previously the unset value silently made
  the artifact optional, so a release with a missing asset warned and
  published instead of failing. Release-artifact names and paths are now also
  validated against the character set the emitted upload shell can carry
  safely, since the path is spliced unquoted so its glob can expand
- **generate:** Route the promote matrix input JSON (`DEFAULT_INPUTS` /
  `ENV_INPUTS`) through the Build Deploy Matrices step `env:` map instead of
  embedding it in a shell single-quote literal, and emit dispatch-input
  defaults as properly escaped YAML single-quoted scalars. An apostrophe in an
  ordinary input value (for example `message: "it's live"`) previously
  generated a promote script that failed shell parsing on every promotion, and
  a crafted value could have executed in the promote job; an apostrophe in a
  `dispatch_inputs` default produced an invalid workflow document

- **promote:** Record only the publishing component's own release marker
  (`version`, `sha`, `released_on`) under `latest_release.components.<name>`.
  Previously a component publish wrote the entire shared release record under
  its own leaf, nesting a stale snapshot of every component's markers there,
  and a component promotion save could synthesize a phantom marker for a
  component that had never released

- **config:** Validate matrix dimension keys and axes at validation time: each
  `matrix.dimensions` key must be a valid GitHub Actions matrix identifier
  (start with a letter or underscore; letters, digits, hyphens, and
  underscores only) and each axis must list at least one value, instead of an
  invalid key or empty axis passing validation and failing at the first
  workflow run; the manifest JSON Schema enforces the same constraints
- **config:** Validate `run_policy`, `on_failure`, and `retries` on the
  `validate` block the same way as builds and deploys: `run_policy` and
  `on_failure` must be one of their documented values and `retries` must stay
  between 0 and 3, instead of out-of-range settings being silently accepted
- **config:** Bump the default `cli_version` pin for generated workflows to
  v0.16.1, and guard the default against lagging the latest stable release
- **hotfix:** Converge the missing tag and release when the hotfix finalize
  job reruns after a partial failure, instead of skipping release creation
- **promote:** Carry recorded component state (previous ring, sibling
  deploys, ref, base SHA, patches, divergence) through component finalize
  instead of overwriting it, and defer rejoin cleanup until the state write
  is durable, so a component promotion cannot drop a sibling component's
  recorded state
- **orchestrate:** Overlay recorded component state into the working state
  map so the base-SHA ladder and finalize read each component's recorded
  rungs instead of rebuilding them from empty for component manifests
- **orchestrate:** Anchor a component-scoped run's changelog base on the
  component's own dimension: the release marker recorded under
  `latest_release.components.<name>`, the component's `environments` override
  when it narrows the shared ladder, and the component's own release tags.
  Previously only the top-level `latest_release` was read, which is always
  empty for component-scoped manifests, so after a component's first publish
  its release notes spanned the repository's full history
- **config:** Reject build, deploy, and environment names whose emitted
  output keys collide because they differ only by hyphen versus underscore,
  at validation time instead of at first workflow run
- **simulate:** Apply the deploy-result gate to the simulated rollback
  preview so it matches the eligibility rules a real rollback enforces

## [0.16.1] - 2026-07-15

### Security

- **setup-cli:** Verify the release archive checksum and the cosign signature
  on checksums.txt before installing the CLI, failing closed on any mismatch
- **setup-cli:** Resolve version `latest` to stable releases only, excluding
  pre-releases and drafts
- **generate:** Randomize heredoc delimiters in generated output writes and
  bind the PR preview comment body through the step environment

### Fixed

- **statewrite:** Read manifest content and the optimistic-lock sha as one
  snapshot so concurrent finalize writes cannot lose an update, and route the
  rollback finalize through the same optimistic-lock write path instead of its
  own unguarded PUT
- **statewrite:** Fail fast with the real cause when the manifest lookup
  returns 404 (file absent or the token lacks repo access) instead of retrying
  into a misleading empty-manifest error
- **statewrite:** Retry transient state-write API failures (rate limits, 5xx,
  transport errors) and fail fast on permanent authorization and not-found
  errors instead of treating every failure the same way
- **git:** Apply the same split to push: retry transient transport and
  rate-limit failures, fail fast on authorization, not-found, and 4xx RPC
  failures
- **git:** Pin repository discovery to the caller-supplied directory with a
  `GIT_CEILING_DIRECTORIES` boundary, so state commits, tag reads, and the
  promote preflight change-detection diff can never resolve into an enclosing
  repository
- **generate:** Emit a state-write shell that refuses unguarded PUTs, fails
  fast on permanent API errors, and retries rate-limited writes
- **generate:** Rebase an absolute manifest path to repo-relative in all
  emitted output so generated workflows are reproducible across checkouts
- **generate:** Correct emitted output keys, branch-case resolution, manifest
  routing, and unresolved state-ref handling
- **generate:** Emit `always()` alone when a forced job has no other conditions
- **generate:** Emit a least-privilege `permissions` block on the
  single-environment release workflow: read-only at the top level with
  `contents: write` scoped to the release and finalize jobs, instead of
  inheriting the repository default
- **triggers:** Evaluate `!` exclusion patterns through one canonical matcher
  in orchestrate change detection and promote preflight; exclusions were
  previously matched as inclusions
- **triggers:** Match the order-dependent semantics of the emitted GitHub
  Actions `paths` filter in CLI change detection, where the last matching
  pattern wins and a later pattern can re-include an excluded path; matching
  was previously order-independent and could mispredict which files trigger a
  build or deploy
- **promote:** Resolve build-linked deploys through their build's triggers in
  promotion change detection instead of reading the deploy's own empty trigger
  list
- **promote:** Gate a deploy with multiple `depends_on` builds on the union of
  all its dependencies' triggers instead of only the first, so a change to any
  linked build promotes the deploy
- **config:** Reject build dependencies on deploy jobs in both resolution legs
- **github:** Paginate workflow-job queries and match deploy job names exactly
- **fleetreconcile:** Fail closed when a run-enumeration page slices short at
  the cursor
- **output:** Surface flush close errors and render outputs in sorted order
- **visualize:** Escape graph labels, keep repo slugs injective, and encode
  theme values
- **ci:** Wire the build-cli workflow_call result to a real job output
- **cli:** Honor `--dry-run`, `--json`, and `--trace` on the orchestrate,
  external, promote, simulate, and reset command trees; the flags were parsed
  but silently ignored, so commands such as `orchestrate finalize --dry-run`
  and `external update --dry-run` could write real state
- **cli:** Honor `--dry-run` on `manage-release` delete and publish,
  `branch-protection --apply`, and `promote finalize`, which previously
  performed real deletions, publishes, protection PUTs, and state writes under
  dry-run; a fail-loud guard at the mutation layer now refuses every external
  mutation under `--dry-run`, so no command can mutate silently
- **cli:** Align the root help text with the compiler positioning
- Surface git and API errors on state and version paths instead of swallowing
  them

### Maintenance

- **ci:** Lint workflows with actionlint and shellcheck, and cover the
  setup-cli install script with hermetic fixture tests
- **ci:** Run the e2e harness tests under the race detector
- **e2e:** Repin act runner image to current upstream digest (#572)
- **docs:** Publish release verification on the security page, add the code of
  conduct, refresh stale install and pin examples, add package documentation,
  and correct the attestation verification flags and manifest anchor links
- **test:** Cover reset deletion, change detection, git write paths, simulate
  what-ifs, and visualize theming

## [0.16.0] - 2026-07-13

### Added

- **simulate:** Preview the multi-env hotfix cherry-pick chain (#571)
- **simulate:** Reformat the human-readable what-if output (#570)

## [0.15.0] - 2026-07-12

### Added

- **config:** Fold environment config into the environments list with explicit roles (#568)
- **config:** Uniform deep-merge for component inheritance (#565)
- **config:** Lint errors on reserved-schema usage (#564)
- **simulate:** Support monorepo component-scoped state (#562)
- **docs:** Make dark the only theme and remove the theme switcher (#560)

### Changed

- **config:** Rename the release block to release_build (#569)
- **config:** Canonicalize tag prefix on tag_grammar.prefix (#567)
- **config:** Rename artifact upload field to artifact_upload (#566)
- **config:** Rename parse-config to lint and reject unknown keys (#563)

### Maintenance

- **docs:** Upgrade to Astro 7 and Starlight 0.41 (#559)

## [0.14.2] - 2026-07-11

### Fixed

- **merge-queue:** Pass --environment to the speculative orchestrate setup (#555)

### Maintenance

- **deps:** Bump github/codeql-action to 4.36.3 (#556)
- **deps:** Bump github/codeql-action/upload-sarif (#512)
- **deps:** Bump dorny/paths-filter from 4.0.1 to 4.0.2 (#513)

## [0.14.1] - 2026-07-11

### Fixed

- **schema:** Allow the user-facing reconcile block in the manifest schema (#554)

### Maintenance

- **e2e:** Assert runtime outcomes instead of emitted script text (#552)

## [0.14.0] - 2026-07-11

### Added

- **coverage:** Gate generated workflow kinds on executing coverage (#546)

### Fixed

- **release:** Cut the git tag on update()'s existing-release branch (#551)
- **generate:** Bind state-write CAS token to the rendered base blob (#550)
- **orchestrate:** Re-derive component leaf onto fresh trunk before first state commit (#549)
- **generate:** Scope hotfix finalize trigger to its own component (#548)
- **hotfix:** Match per-component env branches in finalize trigger (#547)

### Maintenance

- **fleet:** Serialize the remainder lane to lower peak hosted-runner demand (#544)

## [0.13.0] - 2026-07-09

### Added

- **config:** Shared-path change semantics via extra_paths and shared_paths (#540)

### Fixed

- **statewrite:** Retry an empty or errored manifest re-fetch instead of parsing it (#542)
- **config:** Reject merge_group on extra_triggers, pointing at merge_queue.enabled (#539)
- **statewrite:** Raise the state-write retry ceiling with jittered backoff and convergence markers (#538)
- **orchestrate:** Re-apply the component state leaf on a rejected finalize push (#537)
- **statewrite:** Classify the branch-ref-CAS 409 as a conflict so concurrent writes retry (#535)

### Maintenance

- **fleet:** Retry transient lane failures and fix the self-repin version SIGPIPE (#543)

## [0.12.0] - 2026-07-08

### Added

- **promote:** Honor a component's environment-ladder subset at runtime (#530)
- **rollback:** Scope the git-history fallback to the component (#529)
- **hotfix,rollback:** Execute the per-component lifecycle in the harness and prove isolation (#528)
- **hotfix:** Thread the component through the hotfix plan and generated apply lane (#527)
- **generate:** Fan out per-component hotfix and rollback workflows (#526)
- **rollback:** Record per-component rollback state and namespace the rollback ref (#525)
- **promote:** Scope hotfix rejoin cleanup to the component (#524)
- **hotfix:** Record per-component finalize state and scope hotfix tags (#523)
- **hotfix:** Namespace env branches per component and scope orphan detection (#522)
- **release:** Reap rc tags per component using the tag grammar (#521)
- **orchestrate:** Record per-component seed state and read it on promote (#520)
- **e2e:** Execute per-component promote workflows and assert component-scoped state (#519)
- **generate:** Fan out per-component promote workflows with isolated concurrency (#518)
- **promote:** Record per-component finalize state via scoped writes (#517)
- **version:** Derive per-component versions from path-scoped commits and strict tag namespaces (#516)
- **generate:** Fan out per-component orchestrate workflows (#514)
- **state:** Scoped state serializer preserving sibling components on concurrent writes (#508)
- **config:** Parse, validate, and resolve the components model under schema_version 1 (#507)

### Maintenance

- **security:** Bump the Go toolchain to 1.26.5 for GO-2026-5856 (#533)
- **fleet:** Add the monorepo example repo to the fleet roster (#532)
- **generate:** Add absolute byte-identical baseline gate for single-component output (#515)

## [0.11.0] - 2026-07-07

### Added

- Add allow_breaking_changes manifest field to disable the breaking-change gate (#502)

### Fixed

- **fleet:** Only fan out the fleet for rc and dryrun tags (#501)

## [0.10.0] - 2026-07-07

### Added

- Configurable tag_grammar manifest block, wiring, and release-path sinks (#500)
- **taggrammar:** Canonical dependency-free tag grammar spec (#498)

### Changed

- **version,git:** Derive the tag grammar from the canonical spec (#499)

### Maintenance

- **e2e:** Repin act runner image to current upstream digest (#480)

## [0.9.3] - 2026-07-07

### Fixed

- **release:** Run own-repo finalize from the source-built cascade binary (#497)
- **release:** One Release run per tag and no orphan draft on rc cut (#496)

## [0.9.2] - 2026-07-06

### Fixed

- **ci:** Make Release single-flight and idempotent to end double-trigger 422s (#491)

### Maintenance

- Block PR merge on a broken docs-site build

## [0.9.1] - 2026-07-06

### Fixed

- **reconcile:** Generate own self-heal companion with a stable-release install (#489)
- **fleet:** Repin example repos to the rc before fan-out (#488)
- **ci:** Pass the action-pins path to the self-heal own-repo reconcile (#486)
- **ci:** Correct the self-heal companion release-asset pattern (#484)
- **ci:** Pass changed files to the self-heal reconcile check and companion (#482)
- **ci:** Harden the release-cut path (bootstrap pin, dry-run sweep, dispatch target) (#479)

### Maintenance

- **generate:** Assert emit-filter and pin shape, not frozen versions (#487)

## [0.9.0] - 2026-07-06

### Added

- **generate:** Emit the opt-in reconcile companion (#478)
- **cli:** Add the reconcile command (#476)
- **release:** Group the changelog by conventional-commit type (#472)
- **ci:** Bump example-suite bootstrap pins on final release (#468)
- **fleet:** Add shared fleet-repin composite action (#467)
- **fleet:** Add optional cascade_version inputs to dispatch-suite action (#466)

### Changed

- **generate:** Promote the shared pin-reconcile core (#474)
- **fleet:** Dispatch suites with rc inputs and drop parent repin and floor gate (#470)
- **git:** Unify state-push rebase-retry into one dir-aware helper (#463)

### Fixed

- **config:** Harden manifest input validation (#475)
- **release:** Span the final-release changelog since the previous stable tag (#471)
- **fleet:** Install the rc from the cascade repo, not the example repo (#469)
- **release:** Dispatch the release workflow for a candidate tag (#465)
- **fleet:** Fan out on a dispatched Release and fail closed on a no-op (#464)

### Maintenance

- Self-heal action-pin drift on cascade's own repo (#477)
- **configuration:** Document cascade's ownership of generated action pins (#473)
- **contributing:** Require fleet suites to move with generator eligibility changes (#462)

## [0.8.0] - 2026-07-05

### Added

- **ci:** Surface failing-job root cause in the PR failure comment
- **release:** Sign release artifacts and add build provenance (#395)

### Fixed

- **release:** Assert required release assets by presence in auto-promote (#460)
- **ci:** Label the failure-report log region as excerpt not tail
- **release:** Migrate cosign signing to the Sigstore bundle format (#459)
- **ci:** Surface action-pins remediation in the PR failure report
- Batch low-risk hardening (rebase abort, nil guards, injectable clock, rc parsing) (#455)
- **config:** Charset-validate dispatch_input names and choice options (#454)
- **orchestrate:** Retry state-write push on a non-fast-forward trunk (#451)
- **config:** Match trigger globs with slash-native path.Match (#450)
- **git:** Surface git log failures instead of swallowing them as empty range (#448)
- **orchestrate:** Correct trigger glob matching for recursive and single-star patterns (#446)
- **security:** Build with go1.26.4 to clear stdlib advisories (#445)
- **git:** Scope GetLatest* tag lookups to the repo dir (#423)
- **version:** Tolerate prerelease dryrun tags in next-env calc (#420)
- **rollback:** Guard the first environment against rollback (#418)

### Maintenance

- **release:** Drop redundant E2E from the orchestrate build path (#461)
- Bump actions/cache to the node24 v6.1.0 release
- **validate:** Pin govulncheck to an immutable commit (#449)
- **validate:** Extend the vulnerability scan to the e2e module (#447)
- **generate:** Make actionlint normalization test hermetic (#444)
- **deps:** Bump actions/setup-go from 6.4.0 to 6.5.0 (#404)
- **deps:** Bump golangci/golangci-lint-action from 8.0.0 to 9.3.0 (#428)
- **deps:** Bump goreleaser/goreleaser-action from 7.2.2 to 7.2.3 (#429)
- **fleet:** Guard example-suite tooling pins against floor drift (#427)
- **e2e:** Add scheduled act runner image repin (#426)
- **e2e:** Reuse config.TrunkConfig in the scenario harness (#422)
- **hotfix:** Document orphan self-heal, correct stale reserved-shape comments, add callbacks matrix row (#425)
- **configuration:** Add rows for action_pins, pin_mode, release_trigger, validate_check, merge_queue (#424)
- **cli-reference:** Document status, rollback, and graph command families (#421)
- **release:** Sweep accumulated dry-run prerelease tags (#419)

## [0.7.0] - 2026-06-30

### Added

- **branch-protection:** Add --apply flag for direct branch-protection PUT (#416)

### Maintenance

- **branch-protection:** Document --apply mode and admin-token caveat (#417)
- **release:** Cover the auto-promote resolve decision logic (#415)
- **e2e:** Add scenarios 42-44 covering consistency-fix, rollout strategy, and components reserve (#414)

## [0.6.0] - 2026-06-28

### Added

- **status:** Add consistency --fix to delete orphan env branches (#409)
- **release:** Nightly-gated rc-cutting and promotion with a dispatch test vector (#380)
- **fleet:** Add cascade-example-rollback-dispatch to the staged remainder (#379)

### Changed

- Converge artifact action versions and add pin consistency lint (#401)
- **generate:** Source action pins from an embedded manifest (#398)

### Fixed

- **hotfix:** Self-heal orphan env branches during plan (#408)
- **e2e:** Pin act runner image to a Node 24 digest (#399)
- **fleet:** Raise dispatch-suite watch cap 75->180 min (#394)
- **fleet:** Preserve cli_version_sha across state writes and restore the sha-pin repin (#393)
- **fleet:** Repin rewrites stale dryrun refs, not just rc refs (#392)
- **statewrite:** Route orchestrate and external state writes through WriteManifestState (#389)
- **fleet:** Tag-pin the example repos in the repin instead of SHA-pinning (#391)
- **security:** SHA-pin the setup-cli action in generated workflows (#384)
- **version:** Ignore non-version tags in latest-tag discovery (#382)
- **fleet:** Stage the fan-out and add a selective repos input (#378)

### Maintenance

- **fleet:** Retry heavy lane on suite failure (#407)
- **e2e:** Shard scenarios across a retrying matrix (#405)
- **deps:** Scope dependabot to action-pins manifest (#403)
- **deps:** Bump actions/checkout to v7.0.0 and github-script to v9.0.0 (#402)
- **deps:** Bump github.com/moby/moby/api from 1.54.2 to 1.55.0 in /e2e (#271)
- **deps:** Bump github.com/testcontainers/testcontainers-go in /e2e (#269)
- **deps:** Bump actions/download-artifact from 4.3.0 to 8.0.1 (#270)
- **deps:** Bump dorny/paths-filter from 3.0.2 to 4.0.1 (#273)

## [0.5.1] - 2026-06-26

### Fixed

- **statewrite:** Preserve unmodeled manifest config across state writes (#371)
- **fleet:** Raise per-repo suite watch cap to clear the longer 4env run (#368)

## [0.5.0] - 2026-06-26

### Added

- **visualize:** Render the cross-repo flow as per-repo lanes (#362)
- **simulate:** Record build and deploy callbacks as stubbed effects with gating (#360)
- **visualize:** Add theme layer with cascade and bland built-in themes (#359)
- **simulate:** Add rollback, release, and hotfix what-if actions (#355)
- **graph:** Add env and stages render granularities (#356)
- **graph:** Add cascade graph command rendering the pipeline as mermaid (#354)
- **simulate:** Add what-if engine for state diff and effect sequence (#353)
- **visualize:** Pipeline view model, pluggable emitter, and Mermaid emitter (#352)
- **fleet:** Fan out to cascade-example-no-env and cascade-example-callbacks (#345)

### Fixed

- **hotfix:** Fail loudly when a remote env tip diverges from recorded state (#367)
- **statewrite:** Stamp the bot on orchestrate, release, and rollback state writes (#366)
- **statewrite:** Attribute state commits to the bot, not the token owner (#364)
- **reset:** Retry reset push on non-fast-forward, re-applying the baseline (#361)
- **fleet:** Retry repin verify read-back to absorb contents-API lag (#358)
- **orchestrate:** Skip unchanged builds via build-state base ladder (#351)
- **release:** Require --sha only for tag-creating manage-release actions (#346)
- **e2e:** Make act container start and CLI tar-copy deterministic (#342)
- **e2e:** Make hotfix-conflict scenario anchor deterministic (#309)

### Maintenance

- **e2e:** Add emission coverage for triggers, action-pins, breaking-gate (#344)
- **rollback:** Isolate resolution source in e2e + surface it in the rollback workflow (#347)
- **cli:** Cover init, status, and generate-flag CLI gaps (#343)

## [0.4.1] - 2026-06-24

### Fixed

- **generate:** Disable pyflakes in actionlint test for hermeticity (#341)
- **fleet:** Tolerate transient watch API errors with a bounded retry (#340)

## [0.4.0] - 2026-06-24

### Added

- Add notify deploy_name and environment overrides (#267)
- Add fleet run-ledger reconcile gate (#266)

### Fixed

- **generate:** Pass github.token to setup-cli so cold-cache CLI download authenticates (#277)
- **fleet:** Download ledger artifacts per-subdir so multi-job ledgers do not clobber (#276)
- **fleet:** Correct the register-run upload-artifact pin to the real v4.6.2 sha (#275)

## [0.3.0] - 2026-06-24

### Added

- Default release_token to state_token to arm the rc-to-release chain (#258)
- Emit least-privilege top-level workflow permissions (#251)
- Elevate a hotfix commit set across the env chain in the generated workflow (#248)
- Plan multi-commit hotfix elevation across the env chain (#246)
- Add cascade plan command to preview workflow diffs (#243)
- Add repository_dispatch trigger to the rollback workflow (#242)
- Emit native GitHub Deployment objects for promotions (#241)
- Support GitHub App token sources for state and release tokens (#240)
- Emit environments.json from the manifest (#239)
- Emit branch-protection.json from the manifest (#238)
- Complete cascade init starter scaffold (#237)
- Generate an opt-in PR drift-check workflow (#236)
- Detect orphaned generated workflows in cascade verify (#235)
- Reserve release version-override location pointer (#234)
- Reserve telemetry webhook and job-summary fields (#233)
- Reserve gitops deploy target branch and sha-tracking fields (#232)
- Reserve canary and blue-green deploy fields on rollout (#231)
- Add cascade verify command to detect workflow drift (#225)
- Reserve components schema shape for per-component versioning (#222)

### Fixed

- Reap superseded rc tags at publish time (#265)
- Complete hotfix-rejoin cleanup for published releases (#264)
- Retry manifest state writes on contents API 409 conflicts (#261)
- Record every commit of a multi-commit hotfix set per environment (#260)
- Eliminate eventual-consistency race in multi-env hotfix finalize (#259)
- Wire release_token to the trigger-capable state token (#254)

### Maintenance

- **e2e:** Raise TestActRunner_Start deadline to 5m for cold image pulls (#262)
- **docs:** Patch astro, dompurify, and esbuild advisories (#253)
- **deps:** Bump github/codeql-action from 3.36.2 to 4.36.2
- **deps:** Bump github.com/moby/moby/api from 1.54.1 to 1.54.2 in /e2e
- Comment on PR workflow drift via fork-safe workflow_run (#228)
- Use cascade verify for the dogfood workflow drift check (#227)

## [0.2.1] - 2026-06-18

### Fixed

- Hand resolved version-under-test to auto-promote via artifact (#221)
- Repin fleet with normal push and propagate failures (#220)

### Maintenance

- Promote final release when the rc fleet gate is green (#219)
- Repin example fleet to the rc under test before fan-out (#217)
- Gate merges on Integration e2e via always-run gate (#216)

## [0.2.0] - 2026-06-18

This release restructures the orchestrate gate chain so build, deploy, and
promote callbacks run in a deterministic dependency order, adds deploy-on-update
so external-update events can drive a deploy directly, and hardens workflow
generation (deterministic output, cross-repo reusable-workflow resolution, and
stricter callback validation).

### Added

- `cascade rollback` command: re-promotes a prior recorded version for a given
  deployable, reading version history from the manifest state store. (#69)
- GHA artifact passing between build jobs in a single orchestrate run: build and
  deploy callbacks may declare an `artifact:` block with `upload` and `downloads`
  sub-keys; the generator emits `actions/upload-artifact` and
  `actions/download-artifact` steps automatically. (#68)
- Per-deployable version recorded on promote finalize: the manifest state now
  tracks which version of each deployable is live per environment, making
  `cascade status` and rollback reliable. (#67)
- Per-callback `runs_on`, `permissions` (including OIDC `id-token`), and
  `concurrency` on inline-run jobs. (#66)
- `cascade status` subcommands for read-only state inspection: per-environment
  summary, per-deployable detail, and raw JSON output. (#61)
- `strategy.matrix` on build jobs in the orchestrate workflow: build callbacks
  that declare `matrix:` get a GHA `strategy:` block; `max-parallel` and
  `fail-fast` are emitted when non-zero/non-default. (#60)
- `extra_triggers` on the orchestrate workflow: `schedule`, `repository_dispatch`,
  `workflow_run`, and `merge_group` triggers are wired from the manifest. (#57)
- `concurrency` blocks on promote, release, and external-update workflows. (#55)
- `schema_version` field on the manifest (`ci.config.schema_version`). The CLI
  reads it on load and enforces a compatibility contract: a manifest written for
  a newer schema than the CLI understands is rejected (upgrade the CLI), a
  manifest below the supported minimum is rejected (migrate), and a manifest
  that omits the field is accepted with a warning. `parse-config` surfaces
  non-fatal advisories in a new `warnings` field on its JSON output. (#43, #63)
- Versioning documentation describing the schema-version compatibility policy,
  the schema-version to CLI-version matrix, and the deprecation window (now
  published as
  [versioning and schema compatibility](https://stablekernel.github.io/cascade/reference/versioning/)).
- Inline run callbacks: cascade-owned callback jobs may use `run:` / `shell:`
  in place of `uses:` (reusable workflow). The `workflow` XOR `run` constraint
  is enforced at parse time. (#62)
- Frozen v1 manifest schema field shapes: all field names, types, and `omitempty`
  behaviour are frozen for the v1 schema generation. (#45)

### Fixed

- Self-host CI trunk branch corrected from `master` to `main`; orchestrate
  workflow now triggers on `main` pushes. (#58)
- `e2e`: parallel runs no longer silently skip when `orchestrate.yaml` is absent;
  the suite now fails fast with a clear error. (#47)

### Maintenance

- OpenSSF Scorecard hardening: all generated and self-hosting workflows pin
  third-party action refs by SHA and declare least-privilege `permissions`. (#46)
- Merge-queue (`merge_group`) and nightly schedule triggers added to the e2e
  workflow. (#59)
- Dependency bumps: `actions/checkout` v6, `actions/setup-go` v6,
  `actions/upload-artifact` v7, `goreleaser/goreleaser-action` v7,
  `github.com/spf13/cobra` v1.10.2. (#48, #49, #52, #53, #54)

## [0.1.0] - 2026-06-10

Initial release of cascade: a trunk-based CI/CD orchestrator for GitHub Actions.

### Added

- `cascade generate` / `cascade parse-config` CLI: reads a YAML manifest and
  emits a complete set of GitHub Actions workflow files (orchestrate, promote,
  release, hotfix, external-update).
- Manifest-driven configuration: trunk branch, environments, deployables,
  build/deploy/promote/release/rollout/rollback callback wiring, change-detection
  paths, and trigger rules all declared in a single `manifest.yaml`.
- `cascade promote`, `cascade release`, and `cascade finalize` state-machine
  commands for managing the deployment lifecycle.
- `docs/` suite: architecture overview, callback contract, CLI reference,
  configuration reference, and getting-started guide.
- Self-hosted CI: cascade manages its own build, release, e2e, and promote
  workflows using the same manifest.

[Unreleased]: https://github.com/stablekernel/cascade/compare/v0.16.1...HEAD
[0.16.1]: https://github.com/stablekernel/cascade/compare/v0.16.0...v0.16.1
[0.16.0]: https://github.com/stablekernel/cascade/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/stablekernel/cascade/compare/v0.14.2...v0.15.0
[0.14.2]: https://github.com/stablekernel/cascade/compare/v0.14.1...v0.14.2
[0.14.1]: https://github.com/stablekernel/cascade/compare/v0.14.0...v0.14.1
[0.14.0]: https://github.com/stablekernel/cascade/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/stablekernel/cascade/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/stablekernel/cascade/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/stablekernel/cascade/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/stablekernel/cascade/compare/v0.9.3...v0.10.0
[0.9.3]: https://github.com/stablekernel/cascade/compare/v0.9.2...v0.9.3
[0.9.2]: https://github.com/stablekernel/cascade/compare/v0.9.1...v0.9.2
[0.9.1]: https://github.com/stablekernel/cascade/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/stablekernel/cascade/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/stablekernel/cascade/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/stablekernel/cascade/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/stablekernel/cascade/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/stablekernel/cascade/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/stablekernel/cascade/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/stablekernel/cascade/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/stablekernel/cascade/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/stablekernel/cascade/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/stablekernel/cascade/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/stablekernel/cascade/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/stablekernel/cascade/releases/tag/v0.1.0
