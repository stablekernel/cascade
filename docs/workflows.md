# Workflows

The framework generates two reusable workflows from your manifest: **Orchestrate** and **Promote**. Both are written by `cascade generate-workflow`.

## Orchestrate

Triggered on every merge to trunk. Handles the full CI/CD pipeline for the first environment in the promotion chain.

### Flow

```
Merge to Trunk
      │
      ▼
┌──────────┐
│  Setup   │ ← Parse config, detect changes, compute version
└──────────┘
      │
      ▼
┌──────────┐
│ Validate │ ← Optional pre-build validation
└──────────┘
      │
      ▼
┌──────────┐
│  Build   │ ← Matrix: triggered builds only
└──────────┘
      │
      ▼
┌──────────┐
│  Deploy  │ ← Matrix: triggered deploys with dependency ordering
└──────────┘
      │
      ▼
┌──────────┐
│ Finalize │ ← Update state, generate changelog, draft pre-release
└──────────┘
```

### Triggering

The orchestrate workflow is generated to fire on `push` to the trunk branch. There is no need to wrap it — the generator emits the trigger directly:

```yaml
# .github/workflows/orchestrate.yaml (generated)
on:
  push:
    branches: [master]   # taken from config.trunk_branch
```

### Standard Inputs

The orchestrate workflow has no manual inputs by default — it runs automatically on push.

### Outputs

| Output | Description |
|--------|-------------|
| `deployed_sha` | Deployed commit SHA |
| `triggered_builds` | JSON array of triggered builds |
| `triggered_deploys` | JSON array of triggered deploys |
| `version` | Calculated RC version (e.g., `v1.2.0-rc.0`) |
| `changelog` | Generated changelog markdown |
| `release_url` | URL to the GitHub release |
| `execution_plan` | JSON execution plan with waves |

### Change Detection

The setup job determines what to build/deploy:

1. Reads the manifest to get the last deployed SHA (base)
2. Compares base to the current SHA (head)
3. Matches changed files against triggers
4. Builds an execution plan respecting `depends_on`

Example output:
```json
{
  "triggered_builds": ["app"],
  "triggered_deploys": ["cdk", "services"],
  "has_changes": true,
  "execution_plan": {
    "waves": [
      {"name": "wave-1", "callbacks": ["app", "cdk"]},
      {"name": "wave-2", "callbacks": ["services"]}
    ]
  }
}
```

### Version Calculation

The version is computed from conventional commits between the previous release and the current SHA:

| Commits since last release | Bump |
|---------------------------|------|
| `feat!:` or `BREAKING CHANGE:` | major |
| `feat:` | minor |
| `fix:` / `perf:` | patch |

The first environment receives an RC suffix: e.g., `v1.2.0-rc.0`. Each subsequent orchestrate run increments the RC counter.

## Promote

Manual workflow to promote between environments.

### Flow

```
Default mode (one step at a time)
      │
      ▼
┌──────────┐
│ Preflight│ ← Validate source/target, check ancestry, gate breaking changes
└──────────┘
      │
      ▼
┌──────────┐
│  Deploy  │ ← Matrix: per-deploy with change detection
└──────────┘
      │
      ▼
┌──────────┐
│  Publish │ ← (only at prerelease → release boundary, if publish: configured)
└──────────┘
      │
      ▼
┌──────────┐
│ Finalize │ ← Update state, publish release, dispatch Release workflow
└──────────┘
```

A cascade mode (e.g., `dev-to-prod`) walks the chain step by step, running deploy/finalize for each intermediate environment, with the breaking-change gate enforced at the prerelease→release boundary.

### Triggering (Generated)

```yaml
# .github/workflows/promote.yaml (generated excerpt)
on:
  workflow_dispatch:
    inputs:
      mode:
        description: 'Promotion mode - default (sequential) or select a cascade target'
        type: choice
        required: true
        options:
          - default
          - dev-to-test
          - test-to-prod
          - dev-to-prod
          # ... all valid direct cascade targets
        default: default
      force:
        description: 'Continue on failure (default mode only)'
        type: boolean
        default: false
      allow_breaking_changes:
        description: 'Required if promoting breaking changes past pre-release → release'
        type: boolean
        default: false
      dry_run:
        description: 'Dry run mode'
        type: boolean
        default: false
      deploys:
        description: 'Deploys to promote (comma-separated names or "all")'
        type: string
        default: 'all'
      rollback_on_failure:
        description: 'Revert successful deploys if any fails (atomic promotion)'
        type: boolean
        default: true
```

### Inputs

| Input | Type | Default | Description |
|-------|------|---------|-------------|
| `mode` | choice | `default` | `default` or a cascade target (e.g., `dev-to-prod`) |
| `force` | boolean | false | Continue on failure (default mode only) |
| `allow_breaking_changes` | boolean | false | Required to cross the prerelease→release boundary with breaking changes |
| `dry_run` | boolean | false | Preview without deploying |
| `deploys` | string | `all` | Comma-separated deploy names or `all` |
| `rollback_on_failure` | boolean | true | Atomic semantics: revert on failure |

### Outputs

| Output | Description |
|--------|-------------|
| `source_sha` | SHA being promoted |
| `target_env` | Destination environment |
| `rollback_sha` | SHA to revert to on failure |
| `deploys_to_run` | JSON array of deploys to run |
| `external_deploys_to_run` | JSON array of external deploys to run |
| `version` | Version applied to the target |
| `changelog` | Changelog since the previous release |
| `release_url` | URL to the GitHub release |

### Atomic Promotions with Rollback

The promote workflow supports atomic promotions where successful deploys are automatically rolled back if any deploy fails:

```yaml
# Enabled by default
rollback_on_failure: true
```

When enabled:
1. Preflight captures the target environment's current SHA as `rollback_sha`
2. If any deploy job fails, rollback jobs trigger for successful deploys
3. Rollback jobs redeploy using the `rollback_sha`
4. This ensures all-or-nothing promotion semantics

Disable for non-atomic promotions:
```yaml
rollback_on_failure: false
```

### Selective Deployments

Use the `deploys` input to promote specific deploys:

```yaml
deploys: "app,infra"   # Only promote app and infra
deploys: "all"         # Promote all (default)
```

### Per-Deployable Change Detection

The promote workflow uses diff-based detection:

1. For each deployable, compare the target's last deployed SHA with the source SHA
2. Check whether trigger paths have changes
3. Only run deploys with actual changes

This prevents unnecessary deploys (e.g., don't redeploy CDK if only services changed).

### Promotion Modes

The mode dropdown is generated from the configured `environments` list.

**Default mode** advances the chain by one logical step (next env, or release/prod at the boundary).

**Cascade modes** are explicit `from-to-to` walks generated for every valid forward pair:

| Mode (example) | Behavior |
|----------------|----------|
| `dev-to-test` | Promote dev → test |
| `dev-to-uat` | Cascade dev → test → uat (each step deployed and finalized) |
| `dev-to-prod` | Full cascade through all environments + release |
| `uat-to-prod` | Partial cascade from uat onward |
| `test-to-prod` | Standard release |

Cascade promotions are atomic per environment. The breaking-change gate runs at the prerelease→release boundary; pass `allow_breaking_changes: true` to proceed past it.

### Publish Step

When the manifest contains a `publish:` callback, the promote workflow includes a publish step that runs once per configured build at the prerelease→release boundary. The framework reads `artifact_id` from the source environment's build state and dispatches the publish workflow with:

```
build_name=<build name>
old_version=<RC version, e.g. v1.0.0-rc.2>
new_version=<final semver, e.g. v1.0.0>
sha=<source SHA>
artifact_id=<digest from build state>
```

The publish callback is responsible for the registry operation (retag, copy, sign).

### Version Determination

For prod promotions:
1. Get the latest semver tag (e.g., `v1.2.3`)
2. Auto-increment based on conventional commits since that tag (major / minor / patch)
3. Or use the `version_override` input for an explicit bump

The framework drops the RC suffix when crossing the prerelease→release boundary.

## Hotfix

Hotfix is currently handled via the standard promote workflow with `dry_run: false` and a deploy-list filter. A first-class hotfix workflow is on the roadmap (issue #94 — direct promotion to prod with branch ancestry checks).

## Workflow Permissions

Generated workflows include the necessary permissions:

```yaml
permissions:
  contents: write    # Push state, create tags
  actions: write     # Dispatch the Release workflow from finalize
  packages: write    # Optional: only if your callbacks publish to GHCR
```

For environment protection on deploys, set the environment in your callback:

```yaml
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: ${{ inputs.environment }}   # GitHub enforces approvals
```

## Concurrency Control

Each workflow uses concurrency groups to prevent conflicts:

```yaml
# Orchestrate - per branch
concurrency:
  group: orchestrate-${{ github.ref }}
  cancel-in-progress: false

# Promote - per source environment
concurrency:
  group: promote-${{ inputs.mode }}
  cancel-in-progress: false
```

## Dry Run Mode

Both workflows support `dry_run: true`:

- Detects changes normally
- Generates the execution plan
- Skips actual deployments (callbacks check `inputs.dry_run`)
- Does not update state
- Does not create or publish releases

Use dry run to preview what would happen.

## Debugging

Enable trace-level logging by setting `TRACE=true` in the environment, or invoke the CLI with `--trace`:

```bash
cascade --trace orchestrate setup --environment dev
```

Trace logs include:
- Full change detection results
- Dependency resolution steps
- Callback input/output details
- State operations
