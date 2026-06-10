# Cascade

**Declarative trunk-based CI/CD for GitHub Actions.**

Define what to build and where to deploy in one manifest. Cascade generates the
GitHub Actions wiring, tracks deployment state, manages releases, and cascades
promotions through your environments.

## How it works

The manifest (`.github/manifest.yaml`) is the single source of truth. It holds the
pipeline configuration and the live deployment state for every environment. You run
`cascade generate-workflow` once. After that, the generated workflows own their
execution.

A merge to trunk runs the orchestrate workflow: it detects what changed, computes
the next release candidate, builds and deploys to the first environment, and writes
state back to the manifest. A `workflow_dispatch` then promotes the same artifacts
forward, one environment at a time, until the release is published and the RC tags
are cleaned up.

## Where to go next

- **[Getting Started](getting-started.md)** walks through a first manifest and the
  generated workflows.
- **[Manifest Reference](configuration.md)** documents every field.
- **[Callback Contract](callback-contract.md)** covers the inputs and outputs your
  build, deploy, and publish workflows exchange with cascade.
- **[Workflows](workflows.md)** explains orchestrate, promote, and release.
- **[CLI Reference](cli-reference.md)** lists the commands and flags.
- **[Architecture](architecture.md)** describes the design and what cascade does and
  does not own.
- **[Versioning & Schema](versioning.md)** sets out the schema compatibility policy.

## Project

Cascade is open source under the Apache 2.0 license. The source, issue tracker, and
releases live at [github.com/stablekernel/cascade](https://github.com/stablekernel/cascade).
