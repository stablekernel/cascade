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
```

## Making a change

1. Open an issue first for anything non-trivial so we can agree on the approach.
2. Branch from `main`.
3. Keep changes focused. One logical change per pull request.
4. Add or update tests. New manifest fields and generator features need an `e2e/` scenario, not just a unit test on generated output.
5. If a change alters which operations are valid on which environments (for example, which environments are eligible for rollback or promotion), update the fleet example-repo suites that exercise those operations in the same change. A generator or CLI eligibility rule and the fleet suite that asserts against it are one coupled unit; letting them drift hides the mismatch until a later fleet run exercises the now invalid path.
6. Run `go test ./...` and `golangci-lint run ./...` before pushing.
7. Use [Conventional Commits](https://www.conventionalcommits.org/) for commit messages (`feat:`, `fix:`, `docs:`, `chore:`, ...). cascade derives changelogs and version bumps from them.

## API design

Public APIs follow a functional-options style: required inputs are positional and optional or extensible behavior arrives as a variadic `...Option` tail, so new capability is additive and never a breaking signature change. Cross-cutting concerns are small interfaces with no-op defaults rather than forced dependencies.

## Governed action pins

cascade owns the third-party action pins it emits into generated workflows, and that ownership rests on a few rules that any code touching pins, manifest paths, or machine-authored commits must keep:

- Governed action pins are single-source: the manifest's `action_pins` (or, for cascade's own repo, `action_pins.yaml`) is the one place a pin value lives. A generated workflow is a rendering of that source, never a second copy to reconcile against.
- A pin value gets spliced verbatim into generated YAML, so it must be charset-validated before it is accepted and must never carry a newline. Validate at the point a pin value enters the manifest, not at render time.
- Path-shaped manifest fields, such as `action_folder` and callback workflow paths, must reject a `..` path segment during validation, so a configured path can only resolve inside the repository tree it is meant to.
- A machine-authored commit (a bot or CI job writing on the project's behalf) stages an explicit pathspec allowlist naming exactly the files it intends to change. It never uses a blanket `git add -A` or `git add .`, so an unrelated working-tree change can never ride along.
- Generated files are targets, never sources: a pin (or any other value) is read from the manifest and written into generated output, never read back out of a generated file. This keeps generation a pure, offline function of the manifest, which is what makes a regenerate reproducible and a diff meaningful.
- cascade's own self-heal companion is generated, not hand-written. `.github/workflows/pin-reconcile.yaml` is produced by the same reconcile generator that emits a downstream user's companion, in its own-repo variant, and is drift-locked byte-for-byte by a test so a hand-edit fails the suite. The own-repo variant differs from the user emission in exactly three ways: it installs the latest non-prerelease cascade release (never an rc or a draft, so cascade's own CI cannot self-install a prerelease), it scans both the workflow and composite-action trees for a moved pin, and it commits the regenerated workflows alongside the updated `action_pins.yaml`. Change the generator and regenerate the file; never edit the workflow by hand.

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
