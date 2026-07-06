---
title: Add or change environments
description: Reshape your environment chain, wire per-environment GitHub Environment settings, and apply branch protection.
---

This guide covers adding an environment to an existing pipeline, configuring each environment (required reviewers, wait timers, branch policy), and applying those settings to GitHub.

## Add an environment

`environments` is an ordered list; position, not name, carries meaning. The last environment is the release stage, the second-to-last is the prerelease stage. Add a name at the position you want, then regenerate:

```yaml
environments: [dev, staging, prod]
```

```bash
cascade generate-workflow -f
```

cascade adds the new environment's `state.<env>` entry automatically the next time orchestrate or promote finalizes; you never hand-author `state:`. Appending to the end of the list shifts which environment is the release stage, since that role is always the last position, so reorder deliberately rather than just appending if you want to keep an existing environment as production.

## Per-environment config

`environment_config.<env>` carries settings for one environment, keyed by its cascade name. All fields are optional and additive.

```yaml
environment_config:
  prod:
    gha_environment: production
    required_reviewers: ["octocat", "team/ops"]
    wait_timer: 10
    branch_policy: protected
```

| Field | Purpose |
|-------|---------|
| `gha_environment` | Maps this cascade environment to a GitHub Environment: native deployment records, `environment_url`, required reviewers, wait timers, env-scoped secrets. |
| `required_reviewers` | User or team slugs (for example `octocat`, `team/ops`) that may approve a deployment. |
| `wait_timer` | Delay in minutes before a job targeting this environment runs. GitHub accepts 0 to 43200 (30 days). |
| `branch_policy` | `protected` (protected branches only), `custom` (only branches/tags matching patterns), or `all` (no restriction). Empty is treated as `all`. |

### Full field reference

A few more fields round out the block, mostly for the `custom` branch policy or operator record-keeping:

| Field | Purpose |
|-------|---------|
| `branch_patterns` | Branch name patterns allowed to deploy when `branch_policy: custom`. |
| `tag_patterns` | Tag name patterns allowed to deploy when `branch_policy: custom`. |
| `secrets` | Expected env-scoped secret **names** only. cascade never stores or emits values; create them out of band. |
| `variables` | Expected env-scoped variable **names** only, same rule as secrets. |
| `environment_url` | URL reported to the GitHub Deployments API for this environment's deployment status. |

GitHub Environment support is shipped: `gha_environment` drives native GitHub deployments and `environment_url`, and the fields above feed the `environments` command below. It lands in generated output today, not a "modeled but not emitted" state.

## Apply GitHub Environment settings with the `environments` command

`cascade environments` reads your manifest and emits a per-environment configuration file for an operator to apply. cascade never calls the GitHub API itself:

```bash
cascade environments
```

The output has one entry per manifest environment, in manifest order:

| Key | Contents |
|-----|----------|
| `environments[].name` | The cascade environment name. |
| `environments[].gha_environment` | The GitHub Environment to configure. |
| `environments[].environment` | The body to `PUT` to the Environments API (`wait_timer` and the branch-policy fields cascade can fully form). |
| `environments[].operator_todo` | Guidance that is not part of the API body: reviewer slugs to resolve to numeric IDs, and secret/variable names to create. |

Apply each entry by sending only its `.environment` object:

```bash
cascade environments | jq -c '.environments[] | {gha_environment, environment}' | \
  while read -r row; do
    env=$(jq -r .gha_environment <<<"$row")
    jq .environment <<<"$row" | \
      gh api -X PUT "repos/OWNER/REPO/environments/$env" --input -
  done
```

Required reviewers need a manual step: the Environments API takes numeric reviewer IDs, not slugs, so resolve each slug under `operator_todo.required_reviewers` and add it to the body's `reviewers` array yourself. Secrets and variables are names only under `operator_todo`; create them through the environment-secrets and environment-variables APIs and set values yourself.

Flags: `-c/--config` (auto-detects `.github/manifest.yaml`), `--manifest-key`, `-o/--output` (write to a file instead of stdout).

## Branch protection

`cascade branch-protection` emits the JSON body for GitHub's branch-protection API, built from the required Setup and Finalize jobs that run on every pipeline run:

```bash
cascade branch-protection --branch main
```

The output has two keys: `protection` (the exact PUT body) and `operator_todo`. Apply it the same way:

```bash
cascade branch-protection | jq .protection | \
  gh api -X PUT repos/OWNER/REPO/branches/main/protection --input -
```

The required-status-checks list only ever contains the cascade-controlled Setup and Finalize jobs, since those are the only contexts cascade knows the exact name of; applying `.protection` as-is never blocks a pull request. Your validate, build, and deploy callback jobs are not required automatically, because cascade knows their display-name prefix but not the inner job name GitHub appends to form the real check-run context. Complete those from `operator_todo.complete_these_contexts`, which lists `"<DisplayName> / <inner-job>"` placeholders for you to fill in.

Pass `--apply` to PUT the body directly instead of emitting JSON; it requires a scoped, repo-admin token via `--token` (or `GITHUB_TOKEN`), plus `--repo` (default `GITHUB_REPOSITORY`) and `--api-url` (default `GITHUB_API_URL`, then `https://api.github.com`).

## The runner-override caveat

Per-environment (and per-callback) `runs_on` is validated-only: cascade parses and schema-checks it, but never emits it into generated YAML. GitHub Actions forbids a `runs-on:` key on a job that calls a reusable workflow with `uses:`, and every cascade-generated caller job is exactly that shape. So cascade-owned jobs are hardcoded to `ubuntu-latest`, and there is no way to point one environment's orchestration job at a different runner.

Your own callback workflows are unaffected: set `runs-on:` inside your `workflow_call` workflow's job as you would in any other GitHub Actions workflow. The restriction only applies to the caller job cascade generates.

---

**Prerequisite:** [Getting Started](/cascade/start/getting-started/) for a running pipeline to reshape.
**Next:** [Promote a release](/cascade/guides/promote/).
