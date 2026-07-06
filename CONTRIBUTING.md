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

## Reporting bugs

Open an issue with the manifest config, the generated workflow (if relevant), and what you expected versus what happened. A minimal reproduction helps a lot.
