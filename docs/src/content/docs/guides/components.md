---
title: Split a repo into components
description: Run a monorepo from one manifest. Version, promote, hotfix, and roll back several independent components, each in its own isolated tag and state namespace.
---

A single repository often holds more than one deliverable: an API and a web
frontend, a service and its infrastructure module, a family of libraries. Cascade
lets each of these be a **component** with its own version line, its own promotion
cadence, and its own hotfix and rollback history, all described in one manifest.

Components are the native shape of `schema_version: 1`. A manifest with no
`components:` block is one implicit component that spans the whole repository, and
it generates byte-identical output to a single-component setup. Nothing about the
single-component path changes, and there is nothing to migrate. You reach for
`components:` only when you want more than one independently versioned unit in the
same repository.

:::note[Components are not multiple repos]
This guide is about several components inside **one** repository. To coordinate
artifacts that live in **separate** repositories, each with its own history and
release cadence, see [Coordinate multiple repos](/cascade/guides/multi-repo/). The
two models solve different problems and can be combined.
:::

## What a component is

A component is a named subtree of the repository that versions and ships on its
own. Each component declares two required fields:

- `path`, the subtree the component owns (for example `api/` or `web/`).
- `tag_grammar.prefix`, the prefix for that component's version tags, set inside the
  component's `tag_grammar` block. Each component needs a distinct prefix so their tag
  namespaces never overlap.

Everything else a component needs, such as its builds, deploys, environment
ladder, or breaking-change policy, is inherited from the shared top-level config
and overridden per component only where it differs.

## Declare components with shared defaults

When a `components:` block is present, the top-level config becomes the set of
**shared defaults** every component inherits. Each entry under `components:`
overrides those defaults where it sets a value and inherits them everywhere else.
Overrides are a deep merge: a component that sets only part of a block, such as
one `tag_grammar` field, keeps the inherited siblings rather than dropping them.
The `environments` list is the exception, whole-replacing the shared ladder
(inline per-environment settings and all) when a component narrows it. See the [override
matrix](/cascade/reference/manifest/#inheritable-overrides) for the precise
per-field rules.

This manifest ships an `api` service and a `web` frontend from one repository. The
shared defaults set the CLI pin, the trunk branch, and the default environment
ladder. Each component owns its subtree, its tag namespace, and its own build and
deploy:

```yaml
# .github/manifest.yaml
ci:
  config:
    schema_version: 1
    trunk_branch: main
    cli_version: v0.16.2
    environments: [dev, staging, prod]

    components:
      api:
        path: api/
        tag_grammar:
          prefix: api-
        builds:
          - name: api
            workflow: .github/workflows/build-api.yaml
            triggers: ["api/**"]
        deploys:
          - name: api
            workflow: .github/workflows/deploy-api.yaml
            triggers: ["api/**"]

      web:
        path: web/
        tag_grammar:
          prefix: web-
        # web ships to a shorter ladder than the shared default.
        environments: [dev, prod]
        builds:
          - name: web
            workflow: .github/workflows/build-web.yaml
            triggers: ["web/**"]
        deploys:
          - name: web
            workflow: .github/workflows/deploy-web.yaml
            triggers: ["web/**"]
```

Here `api` inherits the shared `[dev, staging, prod]` ladder, while `web`
overrides it with a shorter `[dev, prod]` ladder. Both inherit `trunk_branch` and
`cli_version` because those are shared, repository-wide settings that a component
cannot override. For the exact list of which fields a component may override and
which stay repository-wide, see the [`components` field
reference](/cascade/reference/manifest/#components).

## How each component versions independently

Every component computes its own version. Cascade scopes the commit walk to the
component's `path`, so only changes under `api/` bump the `api` version and only
changes under `web/` bump the `web` version. Each component reads and writes tags
under its own `tag_grammar.prefix`, and that namespace is strict: `api-1.2.3` and
`web-1.2.3` never cross-match, and a prefix that is a substring of another (such as
`api-` against `api-beta-`) cannot collide either.

The result is two independent version lines from one trunk. A commit that touches
only `web/` advances `web-` tags and leaves the `api-` line untouched.

The implicit single-component default keeps the permissive historical tag parsing,
so a repository without a `components:` block reads its tags exactly as before. See
[Per-component versioning](/cascade/reference/versioning/#per-component-versioning)
for the tag-namespace rules in full.

## Share code across components

Scoping the commit walk to each component's `path` is the isolation invariant, but
a monorepo also has code every component shares: a common library, a proto package,
a root build file. A change there sits outside every component's `path`, so on its
own it fires nothing and bumps nothing. Two additive fields let a component opt into
the shared code it depends on:

```yaml
ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    shared_paths:
      - libs/common/**   # every component depends on this
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-
        extra_paths:
          - libs/proto/**  # only api depends on the proto package
      web:
        path: services/web
        tag_grammar:
          prefix: web-
```

`extra_paths` widens one component's scope; top-level `shared_paths` widens every
component's scope and is sugar for adding the same glob to each component's
`extra_paths`. A component's effective scope is its `path` plus both. That scope
reaches every place a path matters: the workflow's `push` filter fires on a shared
change, change detection runs the affected builds and deploys, and the version walk
counts the shared commit. A breaking (`feat!:`) commit under `libs/common/` bumps
both `api` and `web`; a breaking commit under `libs/proto/` bumps only `api`. When
you declare neither field, each component's scope is just its `path`, exactly as
before.

## How each component promotes independently

Each component gets its own promote workflow and its own concurrency lane. Cascade
derives a per-component concurrency group so two components never serialize against
each other: promoting `api` from `dev` to `staging` does not queue behind or cancel
a `web` promotion. You promote each component on its own schedule, and the release
boundary (the prerelease and release markers near the top of each ladder) is
evaluated per component against that component's own environment list.

Because `web` above declares a two-environment ladder, its promotion chain has a
single step, while `api` promotes across three environments. Each component's
ladder is its own.

## How hotfix and rollback stay isolated

Hotfix and rollback operate per component, in separate namespaces, so an operation
on one component never reaches into another:

- **State** is recorded per component at `state.components.<name>.<env>`, carrying
  the full per-environment record (version, SHA, deploy history, and the rollback
  ring) for each component independently.
- **Hotfix branches** are namespaced as `env/<component>/<env>`, so a hotfix in
  flight on `api` in `staging` lives on its own branch and its single-flight guard
  only counts that component's open hotfix pull requests. If you protect these
  branches with a ruleset, match the nested `env/<component>/<env>` pattern.
- **Rollback** reads the deploy history from that component's own state ring and
  enumerates that component's own environment ladder, so the rollback dropdown for
  `web` offers only `web` environments.
- **Release-candidate cleanup** runs within the component's tag namespace.
  Publishing `api-1.0.1` reaps only `api-` prerelease tags and never touches
  `web-` tags.

The operator walkthroughs in [Run a hotfix](/cascade/guides/hotfix/) and [Roll
back an environment](/cascade/guides/rollback/) apply per component unchanged; the
only difference is that the branch, state, and tag names carry the component name.

## Target a subset of the ladder per component

A component does not have to ride the full shared ladder. Override `environments:`
on the component to give it a shorter chain, as `web` does above with `[dev,
prod]`. The shared default stays whatever the top-level `environments` list
declares, and each component narrows it where that makes sense. A component's
release and prerelease markers are the last and second-to-last entries of its own
resolved ladder.

## The single-component default is unchanged

If you never write a `components:` block, none of the above applies. The whole
repository is one unit, the top-level `tag_grammar.prefix` is the one tag namespace, the
top-level `environments` is the one ladder, and the generated workflows, state
serialization, version lines, and tags are byte-identical to a pre-component
setup. Components are strictly additive: adopt them when you have a second thing to
version, and ignore them entirely until then.

## What components deliberately do not do

Components are independent by design. Cascade does not sequence one component after
another, build a cross-component dependency graph, or add a fan-in barrier across
components. When you need one unit to react to another, use the `external` and
`notify` coordination path described in [Coordinate multiple
repos](/cascade/guides/multi-repo/), which works across components and repositories
alike.

## Wayfinding

**Prerequisite:** [How Cascade works](/cascade/start/how-it-works/) for the trunk,
environment chain, and release boundary each component follows.

**Reference:** the [`components` field
reference](/cascade/reference/manifest/#components) for the override matrix and
[Per-component versioning](/cascade/reference/versioning/#per-component-versioning)
for the tag-namespace and state rules.
