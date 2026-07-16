# Contributing to cascade

Thanks for your interest in contributing. This document covers what you need to get set up and land a change.

## Developer Certificate of Origin

cascade uses the [Developer Certificate of Origin](https://developercertificate.org/). Every commit must be signed off, which certifies you wrote the change or have the right to submit it under the project's license:

```bash
git commit -s -m "your message"
```

The sign-off adds a `Signed-off-by: Your Name <you@example.com>` line using your `git config user.name` and `user.email`. Pull requests with unsigned commits will be asked to amend.

## Development setup

cascade is a Go module with a self-contained end-to-end suite under `e2e/`.

```bash
# Build the CLI
go build -o cascade ./cmd/cascade

# Unit tests
go test ./...

# End-to-end tests (requires Docker; uses testcontainers + gitea)
cd e2e && go test -v -timeout 20m ./...

# Lint
golangci-lint run ./...

# Regenerate cascade's own workflows (cascade compiles its own)
go run ./cmd/cascade generate-workflow --config .github/manifest.yaml -f
```

## Making a change

1. Open an issue first for anything non-trivial so we can agree on the approach.
2. Branch from `main`.
3. Keep changes focused. One logical change per pull request.
4. Add or update tests. New manifest fields and generator features need an `e2e/` scenario, not just a unit test on generated output.
5. If a change alters which operations are valid on which environments (for example, which environments are eligible for rollback or promotion), update the fleet example-repo suites that exercise those operations in the same change. A generator or CLI eligibility rule and the fleet suite that asserts against it are one coupled unit; letting them drift hides the mismatch until a later fleet run exercises the now invalid path.
6. Run `go test ./...` and `golangci-lint run ./...` before pushing.
7. Use [Conventional Commits](https://www.conventionalcommits.org/) for commit messages (`feat:`, `fix:`, `docs:`, `chore:`, ...). cascade derives changelogs and version bumps from them.
8. Update the `[Unreleased]` section of `CHANGELOG.md` in the same pull request as any user-facing change: every `feat`, `fix`, or `perf` commit, and any breaking change, gets an entry whose bold scope label matches the commit scope (`fix(promote)` gets a `**promote:**` bullet; a `fix(config,simulate)` commit may use one `**config/simulate:**` bullet or one bullet per scope). `TestChangelog_UnreleasedCoversUserFacingCommits` fails any full clone where a user-facing commit since the last stable release tag has no `[Unreleased]` entry carrying its scope.

## API design

Public APIs follow a functional-options style: required inputs are positional and optional or extensible behavior arrives as a variadic `...Option` tail, so new capability is additive and never a breaking signature change. Cross-cutting concerns are small interfaces with no-op defaults rather than forced dependencies.

## Project conventions

cascade holds to a few conventions in its own codebase and in the workflows it generates:

- **Additive manifest changes**: new fields are always optional with sensible defaults, so existing manifest files keep working across minor version bumps.
- **Path fields reach every path sink**: a manifest field that widens which files a component reacts to must thread through all three places a path is consumed, or it is a silent bug. The emitted `on: push` paths filter fires the workflow, per-callback change detection decides which builds and deploys run, and the version commit range decides the bump. A field that reaches only some of these triggers a run that then no-ops, or bumps a version whose builds skip as unchanged. When you add such a field, add a test that asserts the shared path reaches each sink.
- **A breaking generator or validation change moves with the fleet, in the same change**: a change that makes a previously valid manifest invalid, such as rejecting a field `lint` used to accept, is breaking even when it ships as a `fix:`. The fleet repin re-stamps every example repository onto the release-candidate binary and regenerates its workflows before any suite runs, so a manifest still carrying the now-rejected shape fails that repin, not the intended test. Before landing a validation change that can reject something that used to pass, scan the fleet example repos for that shape and migrate any that use it in the same pull request, alongside the doc's migration note. This generalizes the existing rule that a fleet suite and the eligibility logic it exercises are one coupled unit (see [Making a change](#making-a-change)); it applies to validation, not only to eligibility.
- **Every generated workflow kind carries executing coverage**: each workflow the generator emits (`orchestrate`, `promote`, `external-update`, and the `cascade-` lanes) is mapped to the e2e scenarios and fleet lanes that run it in `internal/coverage/registry.yaml`. The coverage gate derives the emitted kinds straight from the generator source and fails when an emitted kind has no registry entry, so a new generated workflow cannot ship without a scenario or lane that exercises it. When you add a generated workflow kind, add its entry pointing at the scenario or fleet lane that runs it; a referenced scenario or lane that does not exist also fails the gate.
- **Assert a runtime outcome, never the script that produces it**: an `e2e/` or fleet-suite assertion for a load-bearing behavior must compare against a runtime artifact that differs when the behavior regresses: a state leaf (`state.<env>` sha/version/ref), a job conclusion (`success`/`skipped`/`failure`), a preflight output, a tag, a release, a branch, a pull request, or a line the running job actually logged (`expect_log`). A passing assertion must be reachable only by the behavior working at run time. Never assert a behavior by grepping emitted script source for a marker that also appears in that source: a `workflow_files.contains`/`not_contains` check over a generated `.yaml` proves a string was rendered, not that the logic ran, and stays green when the runtime behavior is deleted because the marker text is still literally present in the file. As a cautionary example, the state-write retry loop was once "covered" by grepping the emitted `orchestrate.yaml` for `cascade-state-write: exhausted attempts=10`. That branch never runs on the happy path, yet the string is always present in the script, so the check was unconditionally green and a regression that replaced the whole loop with a single `git push` would have shipped green; the fix asserts the marker the running job emits (`cascade-state-write: ok attempt=1`) and proves it red-able by breaking the emission. Restrict `workflow_files` checks to behaviors whose entire effect is the generated shape (a `concurrency:` block, a `timeout-minutes:` value, a real-GitHub-only step act cannot execute) and label those scenarios generation-only in the header. When a behavior is genuinely un-runnable in act, its executing proof lives on the fleet, and a generation-only e2e cell is a ceiling that must be labeled as such, never credited as runtime coverage. The bar: a generator or behavior change adds or updates an assertion a reviewer can turn red by reverting the behavior alone, leaving the emitted string in place.
- **Never discard errors from state or version git operations**: any git or GitHub API call on the state-write path (status, add, commit, push, Contents API reads and writes) or the version-derivation path (tag lookups, commit enumeration, initial-commit resolution) must return its error, wrapped with `%w` and carrying the command's stderr or API body. These failures are never safe to degrade to a warning or an empty result, because the empty result is always indistinguishable from a legitimate state: a failed `git status` reads as "no changes" and finalize reports success without committing state; a failed tag lookup reads as "no tags" and restarts an established project at v0.1.0-rc.0; a failed `git log` reads as "no commits" and silently under-bumps the version. A read that pairs content with an optimistic-lock token (a Contents API GET) must take both from a single response, so a concurrent writer committing between two calls can never produce fresh-token-plus-stale-content that passes the lock and drops the other writer's keys. Fail-open is acceptable only where the failure direction is safe and deliberate, such as change detection assuming "changed"; say so in a comment at the site.
- **One trigger evaluator**: every CLI-side decision that consumes a `triggers` list (orchestrate change detection, promotion preflight, anything new) must delegate to `config.MatchTrigger`/`config.MatchAnyTrigger` rather than reimplementing glob or `!`-negation matching. A private matcher that drifts from GitHub Actions semantics makes the CLI silently disagree with the workflows it generated. The same rule covers derived trigger lists: a build-linked deploy's promotion gate resolves its effective trigger sets through `config.GetTriggerSetsForDeploy`, which returns one set per build dependency so the deploy runs when any set matches, not by reading the deploy's own `triggers` field directly or by concatenating the dependencies' lists into one (a `!` exclusion in one build's list must not veto a sibling build's positive match).
- **The evaluator implements GitHub's filter semantics, and emission is order-preserving**: GitHub evaluates a `paths:` filter in order (last match wins), so `config.MatchTrigger` is order-dependent and the glob grammar is GitHub's filter-pattern grammar (`*`, `**` including inline, `?`/`+` quantifiers, `[]` classes), not Go's `path.Match`. Anything that carries a trigger list toward emission must preserve declaration order and never re-sort it (sorting moves every `!` to the front, where it negates nothing). Shapes a flat list cannot express are normalized by `config.OrchestratePathsFilter`, never ad hoc: a negation-only list becomes `paths-ignore`, and a union across differing callback lists must never let one callback's `!` suppress a sibling's build. When the workflow-level filter must approximate, it over-fires (the run then skips unchanged callbacks); it never under-fires, because a push that never fires the workflow is a silently missed build.
- **--dry-run is gated twice, at the command and at the mutation boundary**: every command that mutates external state (creating, publishing, or deleting releases and tags, PUTting branch protection, writing state through the Contents API, pushing commits) checks `globals.DryRun()` before its first mutation, prints a preview of what it would do, and returns success without mutating. Independently, the lowest-level helpers that execute those mutations (the release API request builder, the branch-protection applier, `statewrite` PUTs, the shared git push helpers) call `globals.GuardMutation` and refuse with a loud error under dry-run, so a future command that forgets its explicit gate fails visibly instead of silently mutating. A new mutation boundary must call `globals.GuardMutation` before executing, and a new mutating command needs a regression test in `cmd/cascade/dryrun_mutation_test.go` proving that `--dry-run` issues no mutation.
- **Callback isolation**: generated workflows call your workflows via `workflow_call`, and cascade never reaches into your callback logic.
- **Metadata courier**: cascade passes artifact identifiers and versions between stages. It never touches your container registry, package registry, or the systems you deploy to directly.

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/), as noted under [Making a change](#making-a-change); the changelog and version bumps are derived from them.

## Governed action pins

cascade owns the third-party action pins it emits into generated workflows, and that ownership rests on a few rules that any code touching pins, manifest paths, or machine-authored commits must keep:

- Governed action pins are single-source: the manifest's `action_pins` (or, for cascade's own repo, `action_pins.yaml`) is the one place a pin value lives. A generated workflow is a rendering of that source, never a second copy to reconcile against.
- A pin value gets spliced verbatim into generated YAML, so it must be charset-validated before it is accepted and must never carry a newline. Validate at the point a pin value enters the manifest, not at render time.
- Path-shaped manifest fields, such as `action_folder` and callback workflow paths, must reject a `..` path segment during validation, so a configured path can only resolve inside the repository tree it is meant to.
- A machine-authored commit (a bot or CI job writing on the project's behalf) stages an explicit pathspec allowlist naming exactly the files it intends to change. It never uses a blanket `git add -A` or `git add .`, so an unrelated working-tree change can never ride along.
- Generated files are targets, never sources: a pin (or any other value) is read from the manifest and written into generated output, never read back out of a generated file. This keeps generation a pure, offline function of the manifest, which is what makes a regenerate reproducible and a diff meaningful.
- cascade's own self-heal companion is generated, not hand-written. `.github/workflows/pin-reconcile.yaml` is produced by the same reconcile generator that emits a downstream user's companion, in its own-repo variant, and is drift-locked byte-for-byte by a test so a hand-edit fails the suite. The own-repo variant differs from the user emission in exactly three ways: it installs the latest non-prerelease cascade release (never an rc or a draft, so cascade's own CI cannot self-install a prerelease), it scans both the workflow and composite-action trees for a moved pin, and it commits the regenerated workflows alongside the updated `action_pins.yaml`. Change the generator and regenerate the file; never edit the workflow by hand.
- `DefaultCLIVersion` in `internal/config/types.go` is the setup-cli pin baked into every generated workflow whose manifest leaves `cli_version` unset, so it should be bumped to track the latest stable release tag. CI does not enforce the const against the repository's tags (a tag necessarily exists before the commit that bumps the const, so a tag-equality gate would block its own release), so keeping it current is a maintainer responsibility. When bumping it, regenerate the byte-identical baseline goldens (`go test ./internal/generate/ -run TestByteIdenticalBaseline -update`) and refresh the pinned examples in the README and docs site; `TestDefaultCLIVersion_MatchesDocsExamples` fails when the examples lag the const.

## Tag grammar

cascade owns one canonical shape for its release tags, and that ownership rests on a few rules that any code touching version tags must keep:

- `internal/taggrammar` is the single source of truth for the shape of a release tag: the prefix, the pre-release token, the separator, and the dry-run token. No other package hand-copies a tag regex or a format string; every tag sink (version parsing, the git tag predicate, the promote-boundary strip, generated workflow templates, hotfix segment allocation) derives its behavior from a resolved `taggrammar.Spec`, never a re-implementation of it.
- A manifest's `tag_grammar` block resolves to exactly one `taggrammar.Spec` per repository (`internal/config`), and that resolved spec is threaded through, not re-read piecemeal from manifest fields at each call site.
- Read-side tolerance (recognizing a foreign pre-release shape or build metadata left over from before `tag_grammar` was adopted) lives in the shared grammar package too, so every consumer stays consistent about what counts as a version tag.
- cascade's own self-release tooling (`nightly-release.yaml`, `release.yaml`, and the fleet) stays pinned to the default grammar (`taggrammar.Default()`) regardless of what a driven repository configures; it never resolves a manifest's `tag_grammar` for cascade's own tags.

## Documentation quality

A change that alters behavior, CLI surface, flags, config or manifest fields, generated output, or the release flow updates the affected docs in the same pull request: the docs site under `docs/src/content/docs/`, the root `README.md`, and any other affected Markdown file. The docs site follows these rules:

- Every page is typed to one Diataxis mode (tutorial, how-to, reference, or explanation) and stays in that mode.
- Depth is layered: the common path a typical reader needs comes first; edge cases, the full field surface, and advanced options move into a clearly labeled later section.
- Each concept is single-sourced on exactly one page. Every other page that touches it links there instead of restating it.
- A manifest field's emission status ("emitted", "validated-only", or "reserved") is stated accurately wherever the field is documented. Get this wrong and the reference has failed its one job.
- Every page carries a prerequisite and next-step link, so a reader always knows what to read before and after.

Stale docs fail review.

## Reporting bugs

Open an issue with the manifest config, the generated workflow (if relevant), and what you expected versus what happened. A minimal reproduction helps a lot.
