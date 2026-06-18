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

## API design

Public APIs follow a functional-options style: required inputs are positional and optional or extensible behavior arrives as a variadic `...Option` tail, so new capability is additive and never a breaking signature change. Cross-cutting concerns are small interfaces with no-op defaults rather than forced dependencies.

## Reporting bugs

Open an issue with the manifest config, the generated workflow (if relevant), and what you expected versus what happened. A minimal reproduction helps a lot.
