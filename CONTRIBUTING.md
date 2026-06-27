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
5. Run `go test ./...` and `golangci-lint run ./...` before pushing.
6. Use [Conventional Commits](https://www.conventionalcommits.org/) for commit messages (`feat:`, `fix:`, `docs:`, `chore:`, ...). cascade derives changelogs and version bumps from them.

## Test policy

cascade requires tests for changes that add or change behavior. This is a
condition of acceptance, not a suggestion.

- **Major new functionality must ship with tests.** Any change that adds a
  feature, a CLI command or flag, a manifest field, or generator behavior must
  include automated tests that cover the new behavior. A pull request that adds
  functionality without tests will be asked to add them before it can merge.
- **Generator features need an end-to-end scenario.** New manifest fields and
  generator features require an `e2e/` scenario that exercises the generated
  workflow, not only a unit test asserting on generated output. A generator
  change with no `e2e/` scenario does not meet the bar.
- **Bug fixes should include a regression test.** When you fix a bug, add a test
  that fails before your fix and passes after it, so the bug cannot return
  unnoticed.
- **Tests run in CI on every pull request.** The test suite and the linter run
  automatically on each pull request and must pass before merge.

Run the suites locally before pushing:

```bash
# Unit tests
go test ./...

# End-to-end tests (requires Docker; uses testcontainers + gitea)
cd e2e && go test -v -timeout 20m ./...
```

The normal inner loop is `go build ./...`, `go test ./...`, and the linter; the
full Docker-backed end-to-end suite is run when your change affects generated
workflow behavior.

## Coding standard

cascade follows standard Go style, enforced automatically. Contributions are
expected to meet it.

- **Formatting.** Code must be `gofmt`-clean. Use `gofmt` (or `goimports`) before
  committing.
- **Linting.** cascade uses `golangci-lint` as its coding standard. Your change
  must pass it with no new findings:

  ```bash
  golangci-lint run ./...
  ```

  The linter runs in CI and is part of the merge bar. Treat its warnings as
  errors: do not merge changes that introduce new lint findings, and do not
  silence findings with blanket suppressions. Narrowly scoped, justified
  suppressions are acceptable only when a finding is a genuine false positive.
- **Idiomatic Go.** Follow effective, idiomatic Go: clear naming, small focused
  functions, errors wrapped with context, no unused exports. The build must be
  warning-free.
- **API design.** Public APIs use a functional-options style: required inputs are
  positional and optional or extensible behavior arrives as a variadic
  `...Option` tail, so new capability is additive and never a breaking signature
  change. Cross-cutting concerns are small interfaces with no-op defaults rather
  than forced dependencies.

## Contribution requirements summary

To be accepted, a contribution must:

1. Be a single logical change in its own pull request, branched from `main`.
2. Sign off every commit under the
   [Developer Certificate of Origin](https://developercertificate.org/)
   (`git commit -s`).
3. Use [Conventional Commits](https://www.conventionalcommits.org/) for commit
   messages.
4. Include tests for new or changed behavior, with an `e2e/` scenario for
   generator and manifest changes.
5. Pass `go test ./...` and `golangci-lint run ./...`.
6. Keep the manifest schema additive: new fields are optional with sensible
   defaults; existing fields are not removed or renamed within a major version.

By participating you agree to the [Code of Conduct](./CODE_OF_CONDUCT.md).

## Reporting bugs

Open an issue with the manifest config, the generated workflow (if relevant), and what you expected versus what happened. A minimal reproduction helps a lot.
