package generate

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// hotfixLabel is the GitHub label applied to clean hotfix resolution PRs.
//
// The literal must stay in sync with hotfixPRLabel in internal/hotfix/plan.go:
// the planner's protection suggestions seed the same label, and the two live in
// separate packages without a shared constant.
const hotfixLabel = "cascade-hotfix"

// hotfixConflictLabel is the GitHub label applied to hotfix resolution PRs
// that require manual conflict resolution.
//
// The literal must stay in sync with hotfixConflictPRLabel in
// internal/hotfix/plan.go.
const hotfixConflictLabel = "cascade-hotfix-conflict"

// HotfixGenerator emits the cascade-hotfix workflow. It cherry-picks a trunk fix
// onto a diverged intermediate environment by replaying the commit on an
// env/<env> integration branch, opening a resolution pull request, and then
// building, deploying, and finalizing the hotfix once that pull request merges.
//
// The workflow carries two triggers in one file: a workflow_dispatch entry that
// plans and applies the cherry-pick, and a pull_request (closed) entry that runs
// the build, deploy, and finalize stages when the resolution pull request merges.
// The clean-path merge runs as the configured state token so the merge is
// trigger capable and the pull_request (closed) chain actually fires; a merge
// authored by the default GITHUB_TOKEN would not emit that event.
//
// This generator is gated on the configured environment count: it emits only
// when two or more environments are declared, because a single-environment
// pipeline has no intermediate target to hotfix onto.
type HotfixGenerator struct {
	config  *config.TrunkConfig
	baseDir string

	// componentName, when non-empty, names the component this hotfix workflow is
	// scoped to. It suffixes the workflow name, composes the component identity
	// into the concurrency group, threads --component through the hotfix CLI steps
	// so plan and finalize record state under this component's subtree, and points
	// the context job's raw N-1 state read at state.components.<name>. It is set
	// only via WithHotfixComponentName by the per-component fan-out.
	componentName string
}

// HotfixGeneratorOption configures a HotfixGenerator. Options are additive so new
// per-component capability never breaks the positional constructor signature.
type HotfixGeneratorOption func(*HotfixGenerator)

// WithHotfixComponentName scopes the generated hotfix workflow to a declared
// component so a multi-component manifest emits one distinct cascade-hotfix-<name>.yaml
// per component. It sets the emitted workflow name, threads --component through the
// hotfix CLI steps, composes the component into the concurrency group, and points
// the context job's rollback-SHA read at the component's state subtree.
func WithHotfixComponentName(name string) HotfixGeneratorOption {
	return func(g *HotfixGenerator) { g.componentName = name }
}

// NewHotfixGenerator creates a hotfix-workflow generator bound to the given
// trunk config and repository base directory.
func NewHotfixGenerator(cfg *config.TrunkConfig, baseDir string, opts ...HotfixGeneratorOption) *HotfixGenerator {
	g := &HotfixGenerator{
		config:  cfg,
		baseDir: baseDir,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// writeComponentFlag emits a "--component <name> \" continuation line at the given
// indent when this hotfix workflow is scoped to a component, so the hotfix CLI
// records and reads state under that component's subtree. The single-component
// workflow emits nothing, keeping its CLI invocations byte-identical.
func (g *HotfixGenerator) writeComponentFlag(sb *strings.Builder, indent string) {
	if g.componentName != "" {
		fmt.Fprintf(sb, "%s--component %s \\\n", indent, g.componentName)
	}
}

// envBranchPrefix returns the env-integration-branch name prefix this hotfix
// workflow operates under, mirroring hotfix.EnvBranchName(component, ""):
// single-component yields "env/" (byte-identical to the historical flat form),
// a component yields "env/<component>/" so each component's integration branches
// occupy a disjoint namespace that agrees with the component-aware plan and
// finalize CLI paths. The apply lane appends the ${env} loop variable to form
// the branch, and the context job recovers TARGET_ENV by stripping this prefix
// from the merged resolution PR's base ref.
//
// The literal must stay in sync with hotfix.EnvBranchName in
// internal/hotfix/lifecycle.go, the same cross-package convention the hotfix
// label constants above follow.
func (g *HotfixGenerator) envBranchPrefix() string {
	if g.componentName != "" {
		return "env/" + g.componentName + "/"
	}
	return "env/"
}

// envBranchRef returns the shell expression naming the env integration branch
// for the apply lane's ${env} loop variable: env/${env} single-component
// (byte-identical), env/<component>/${env} for a component.
func (g *HotfixGenerator) envBranchRef() string {
	return g.envBranchPrefix() + "${env}"
}

// getStateTokenRef returns the token expression used to merge the clean-path
// resolution PR. It mirrors the release and promote generators: users configure
// the full expression via the state_token config option, and it defaults to the
// GITHUB_TOKEN expression when unset. The clean-path merge must run as this
// actor so the merge emits a pull_request(closed) event and reaches the build,
// deploy, and finalize chain; the default GITHUB_TOKEN does not emit that event,
// so a hotfix into a repo with no configured state token records no state until
// the operator supplies a trigger-capable token here.
func (g *HotfixGenerator) getStateTokenRef() string {
	return resolveStateTokenRef(g.config)
}

// Enabled reports whether the hotfix workflow should be emitted. The workflow is
// emitted only when the manifest declares two or more environments, since the
// first environment is the build target and at least one further environment is
// required as a hotfix target.
func (g *HotfixGenerator) Enabled() bool {
	return g.config != nil && len(g.config.Environments) >= 2
}

// targetEnvs returns the hotfix target environments: every configured
// environment except the first, which is the build target. Callers must gate on
// Enabled() so the slice is non-empty.
func (g *HotfixGenerator) targetEnvs() []string {
	return g.config.Environments[1:]
}

// getCLIRef mirrors the ref-resolution used by the other generators so the
// emitted setup-cli ref tracks config.cli_version. The default (cli_version
// unset or "latest") resolves to config.DefaultCLIVersion, an immutable release
// tag; "beta" is the explicit opt-in escape hatch to the "master" branch.
func (g *HotfixGenerator) getCLIRef() string {
	return cliSetupRef(g.config)
}

// getManifestFilePath returns the repo-relative manifest path for use in the
// generated workflow, matching the release generator's resolution.
func (g *HotfixGenerator) getManifestFilePath() string {
	manifestPath := g.config.GetManifestFile()
	if !filepath.IsAbs(manifestPath) {
		return manifestPath
	}
	if g.baseDir != "" {
		if rel, err := filepath.Rel(g.baseDir, manifestPath); err == nil {
			return rel
		}
	}
	return ".github/manifest.yaml"
}

// Generate renders the cascade-hotfix workflow.
func (g *HotfixGenerator) Generate() (string, error) {
	var sb strings.Builder

	g.writeHeader(&sb)
	g.writeTriggers(&sb)
	g.writePermissions(&sb)
	g.writeConcurrency(&sb)
	g.writeJobs(&sb)

	return sb.String(), nil
}

func (g *HotfixGenerator) writeHeader(sb *strings.Builder) {
	sb.WriteString(GeneratedFileMarker + "\n")
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n", g.getManifestFilePath())
	sb.WriteString("#\n")
	sb.WriteString("# Cascade hotfix workflow.\n")
	sb.WriteString("#\n")
	sb.WriteString("# Cherry-picks a trunk fix onto a diverged intermediate environment. On\n")
	sb.WriteString("# manual dispatch it plans the cherry-pick, replays the commit onto the\n")
	sb.WriteString("# env/<env> integration branch via a hotfix/<env>/<sha> branch, and opens a\n")
	sb.WriteString("# resolution pull request. When that pull request merges it builds, deploys,\n")
	sb.WriteString("# and finalizes the hotfix for the target environment. Clean cherry-picks\n")
	sb.WriteString("# auto-merge; conflicting ones open a labeled pull request for a human to\n")
	sb.WriteString("# resolve locally before the build/deploy stages run.\n")
	sb.WriteString("\n")
}

func (g *HotfixGenerator) writeTriggers(sb *strings.Builder) {
	if g.componentName != "" {
		fmt.Fprintf(sb, "name: Cascade Hotfix (%s)\n\n", g.componentName)
	} else {
		sb.WriteString("name: Cascade Hotfix\n\n")
	}
	sb.WriteString("on:\n")
	sb.WriteString("  workflow_dispatch:\n")
	sb.WriteString("    inputs:\n")
	sb.WriteString("      commit:\n")
	sb.WriteString("        description: 'Trunk commit SHA(s) to hotfix, comma-delimited (must be on trunk)'\n")
	sb.WriteString("        required: true\n")
	sb.WriteString("        type: string\n")
	sb.WriteString("      target_env:\n")
	sb.WriteString("        description: 'Target environment'\n")
	sb.WriteString("        required: true\n")
	sb.WriteString("        type: choice\n")
	sb.WriteString("        options:\n")
	for _, env := range g.targetEnvs() {
		fmt.Fprintf(sb, "          - %s\n", env)
	}
	sb.WriteString("      pr_number:\n")
	sb.WriteString("        description: 'Existing hotfix PR number to replay (optional)'\n")
	sb.WriteString("        required: false\n")
	sb.WriteString("        type: string\n")
	sb.WriteString("      dry_run:\n")
	sb.WriteString("        description: 'Dry run (validate only, mutate nothing)'\n")
	sb.WriteString("        required: false\n")
	sb.WriteString("        type: boolean\n")
	sb.WriteString("        default: false\n")
	sb.WriteString("  pull_request:\n")
	sb.WriteString("    types: [closed]\n")
	sb.WriteString("    branches:\n")
	sb.WriteString("      - 'env/*'\n")
	sb.WriteString("\n")
}

// writePermissions grants the scopes the hotfix workflow needs: contents:write
// to push the cherry-pick branch, issues:write to seed labels via gh label create
// (required before gh pr create --label), pull-requests:write to open the
// resolution PR, and actions:read for workflow introspection.
func (g *HotfixGenerator) writePermissions(sb *strings.Builder) {
	// Default to a least-privilege top-level block: reads only. The apply job
	// carries contents/issues/pull-requests: write to push the cherry-pick branch,
	// seed labels, and open the resolution PR; the finalize job carries contents:
	// write to commit state. A callback's own scopes (e.g. id-token: write for
	// OIDC) are scoped to its caller job via writeCallbackPermissions.
	base := [][2]string{
		{"contents", "read"},
		{"actions", "read"},
	}
	writeTopLevelPermissions(sb, base)
}

// writeConcurrency keys the apply (dispatch) path per target environment, but
// keys the finalize (pull_request close) path on a per-repository constant so
// concurrent per-environment finalize runs QUEUE rather than race.
//
// Each env's finalize commits the manifest to trunk through the Contents API.
// Keyed per base ref, two envs whose resolution PRs close together fall into
// different concurrency groups and PUT in parallel; the second writer's blob SHA
// is stale and GitHub returns 409, dropping that env's state. A per-repo finalize
// group with cancel-in-progress: false serializes those writes instead. This is
// defense-in-depth: the durable fix is the Contents API 409 read-modify-write
// retry in internal/statewrite, which still protects against any other writer
// (orchestrate, promote, rollback) that a manifest-global group cannot serialize.
func (g *HotfixGenerator) writeConcurrency(sb *strings.Builder) {
	sb.WriteString("concurrency:\n")
	if g.componentName != "" {
		// Bake the component identity into both the finalize (per-repo) and apply
		// (per-target-env) lanes so two components' hotfixes into the same env do
		// not collide on one repo-global group, mirroring the promote fan-out's
		// per-component isolation.
		fmt.Fprintf(sb, "  group: ${{ github.event_name == 'pull_request' && format('hotfix-finalize-%s-{0}', github.repository) || format('hotfix-%s-{0}', github.event.inputs.target_env) }}\n", g.componentName, g.componentName)
	} else {
		sb.WriteString("  group: ${{ github.event_name == 'pull_request' && format('hotfix-finalize-{0}', github.repository) || format('hotfix-{0}', github.event.inputs.target_env) }}\n")
	}
	sb.WriteString("  cancel-in-progress: false\n")
	sb.WriteString("\n")
}

func (g *HotfixGenerator) writeJobs(sb *strings.Builder) {
	sb.WriteString("jobs:\n")
	g.writePlanJob(sb)
	g.writeApplyJob(sb)
	g.writeCheckJob(sb)
	g.writeContextJob(sb)
	g.writeBuildJobs(sb)
	g.writeDeployJobs(sb)
	g.writeFinalizeJob(sb)
}

// writePlanJob emits the plan job, run only on manual dispatch. It fetches env
// branches and tags, then runs `cascade hotfix plan` and surfaces the planner's
// branch-protection suggestions as ::notice:: lines.
func (g *HotfixGenerator) writePlanJob(sb *strings.Builder) {
	sb.WriteString("  plan:\n")
	sb.WriteString("    name: Plan Hotfix\n")
	sb.WriteString("    if: github.event_name == 'workflow_dispatch'\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	// The plan job runs the single-flight gate via --repo and self-heals an
	// abandoned env branch by force-pushing it back to its recorded base, so it
	// needs contents: write. The single-flight lookup lists hotfix PRs, which
	// requires pull-requests: read on a private repo. actions: read covers
	// workflow introspection. These writes live on the plan job, not at the
	// least-privilege top level.
	writeJobPermissions(sb, "    ", [][2]string{
		{"contents", "write"},
		{"pull-requests", "read"},
		{"actions", "read"},
	})
	sb.WriteString("    outputs:\n")
	sb.WriteString("      branch: ${{ steps.plan.outputs.branch }}\n")
	sb.WriteString("      fix_sha: ${{ steps.plan.outputs.fix_sha }}\n")
	sb.WriteString("      base_sha: ${{ steps.plan.outputs.base_sha }}\n")
	sb.WriteString("      hotfix_version_candidate: ${{ steps.plan.outputs.hotfix_version_candidate }}\n")
	sb.WriteString("      conflict_expected: ${{ steps.plan.outputs.conflict_expected }}\n")
	sb.WriteString("      no_op: ${{ steps.plan.outputs.no_op }}\n")
	sb.WriteString("      env_sequence: ${{ steps.plan.outputs.env_sequence }}\n")
	for _, env := range g.targetEnvs() {
		fmt.Fprintf(sb, "      commits_%s: ${{ steps.plan.outputs.commits_%s }}\n", env, env)
		fmt.Fprintf(sb, "      no_op_%s: ${{ steps.plan.outputs.no_op_%s }}\n", env, env)
		fmt.Fprintf(sb, "      base_%s: ${{ steps.plan.outputs.base_%s }}\n", env, env)
	}
	sb.WriteString("    steps:\n")
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")

	g.writeSetupCLI(sb)
	g.writeFetchEnvBranches(sb)

	sb.WriteString("      - name: Plan hotfix\n")
	sb.WriteString("        id: plan\n")
	sb.WriteString("        env:\n")
	// GH_TOKEN authenticates the single-flight REST API call the planner makes when
	// --repo is set. Without it the lookup may fail on private repos and the plan aborts.
	sb.WriteString("          GH_TOKEN: ${{ github.token }}\n")
	sb.WriteString("          HOTFIX_COMMIT: ${{ github.event.inputs.commit }}\n")
	sb.WriteString("          HOTFIX_TARGET_ENV: ${{ github.event.inputs.target_env }}\n")
	sb.WriteString("          HOTFIX_DRY_RUN: ${{ github.event.inputs.dry_run }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          cascade hotfix plan \\\n")
	fmt.Fprintf(sb, "            --config %s \\\n", g.getManifestFilePath())
	g.writeComponentFlag(sb, "            ")
	sb.WriteString("            --commits \"$HOTFIX_COMMIT\" \\\n")
	sb.WriteString("            --target-env \"$HOTFIX_TARGET_ENV\" \\\n")
	// --repo wires the single-flight PR lookup to a real REST-backed checker.
	// Without it the gate is inert (the no-op checker), which both skips the
	// single-flight protection and, by design, disables orphan self-heal.
	sb.WriteString("            --repo \"${{ github.repository }}\" \\\n")
	sb.WriteString("            --dry-run=\"$HOTFIX_DRY_RUN\" \\\n")
	sb.WriteString("            --gha-output\n")

	// Q6: surface the planner's ready-to-run branch-protection commands.
	// On the --commits (chain) path, protection_suggestions is not emitted by
	// chainGHAOutputs, so this step's if: guard skips it silently. That is the
	// correct behavior: protection suggestions are single-env-plan output only.
	sb.WriteString("      - name: Surface protection suggestions\n")
	sb.WriteString("        if: steps.plan.outputs.protection_suggestions != ''\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          SUGGESTIONS: ${{ steps.plan.outputs.protection_suggestions }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          while IFS= read -r line; do\n")
	sb.WriteString("            [ -z \"$line\" ] && continue\n")
	sb.WriteString("            echo \"::notice::$line\"\n")
	sb.WriteString("          done <<< \"$SUGGESTIONS\"\n")
}

// writeApplyJob emits the apply job, run on dispatch when not a dry-run. It
// walks the bottom-up env_sequence the planner produced, cherry-picking each
// env's still-to-apply commits onto a hotfix/<env>/<sha> branch, opening a
// resolution PR, and merging it before moving to the next env. Each env's commit
// list and base SHA are resolved from statically baked per-env outputs the plan
// job exposes. The job-level GH_TOKEN is the configured state token so every PR
// is authored by a trigger-capable actor: this fires on: pull_request, which lets
// a protected env branch's required check post on PR open rather than only after
// this run finishes. A clean cherry-pick is merged inline (polling until the PR
// is mergeable so the required check still gates the merge) and the loop proceeds
// to the next env. A conflicting cherry-pick opens a labeled PR for local
// resolution and halts the chain: later envs are left untouched until the
// operator resolves the conflict and re-engages the workflow.
func (g *HotfixGenerator) writeApplyJob(sb *strings.Builder) {
	sb.WriteString("  apply:\n")
	sb.WriteString("    name: Apply Hotfix Cherry-Pick\n")
	sb.WriteString("    needs: plan\n")
	// Gate on env_sequence being non-empty: if the planner emitted no envs to
	// process the apply job is a no-op. Per-env idempotency (all commits already
	// present for a given env) is handled inside the loop where COMMITS is empty.
	sb.WriteString("    if: github.event_name == 'workflow_dispatch' && github.event.inputs.dry_run != 'true' && needs.plan.outputs.env_sequence != ''\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	// The apply job pushes the cherry-pick branch (contents: write), seeds labels
	// via gh label create (issues: write), and opens the resolution PR
	// (pull-requests: write). These writes live here, not at the top level.
	writeJobPermissions(sb, "    ", [][2]string{
		{"contents", "write"},
		{"issues", "write"},
		{"pull-requests", "write"},
	})
	sb.WriteString("    env:\n")
	// Author every resolution PR with the configured state token so gh pr create
	// runs as a trigger-capable actor. A PR opened under the default GITHUB_TOKEN
	// is authored by github-actions[bot], and a bot-authored PR does not fire
	// on: pull_request workflows; the env-branch required check would then post
	// only via on: workflow_run after this run finishes, deadlocking against the
	// inline merge poll that waits for that check. A PAT-authored PR fires on:
	// pull_request so the check posts on PR open, independent of this job. When no
	// state token is configured this degrades to GITHUB_TOKEN, in which case
	// post-hotfix automation (early check + finalize) requires the operator to
	// supply a trigger-capable state_token.
	fmt.Fprintf(sb, "      GH_TOKEN: %s\n", g.config.GetStateToken())
	// HOTFIX_COMMIT and HOTFIX_TARGET_ENV carry the operator's original dispatch
	// inputs for human-facing messaging (the conflict PR body's re-engage hint).
	// ENV_SEQUENCE is the planner's bottom-up env chain the loop walks.
	sb.WriteString("      HOTFIX_COMMIT: ${{ github.event.inputs.commit }}\n")
	sb.WriteString("      HOTFIX_TARGET_ENV: ${{ github.event.inputs.target_env }}\n")
	sb.WriteString("      ENV_SEQUENCE: ${{ needs.plan.outputs.env_sequence }}\n")
	sb.WriteString("    steps:\n")
	writeMintSteps(sb, g.config, "      ", seamState)
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")

	g.writeSetupCLI(sb)
	g.writeFetchEnvBranches(sb)

	sb.WriteString("      - name: Configure git identity\n")
	sb.WriteString("        run: |\n")
	writeGitConfigSteps(sb, g.config, "          ")

	// Q2: warn (do not fail) when an env branch lacks required-status-check
	// protection, and print the exact command to configure it. The check loops
	// over the whole chain so every env the apply step may touch is reported.
	sb.WriteString("      - name: Check branch protection on env branch\n")
	sb.WriteString("        continue-on-error: true\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          for env in $(echo \"$ENV_SEQUENCE\" | tr ',' '\\n'); do\n")
	fmt.Fprintf(sb, "            PROT_PATH=\"repos/${{ github.repository }}/branches/%s${env}/protection\"\n", strings.ReplaceAll(g.envBranchPrefix(), "/", "%2F"))
	sb.WriteString("            PROT=$(gh api \"$PROT_PATH\" 2>/dev/null || echo '')\n")
	sb.WriteString("            CHECKS=$(echo \"$PROT\" | jq -r '.required_status_checks.contexts[]? // empty' 2>/dev/null || echo '')\n")
	sb.WriteString("            if [ -z \"$PROT\" ] || [ -z \"$CHECKS\" ]; then\n")
	fmt.Fprintf(sb, "              echo \"::warning::Branch %s has no required status checks; hotfix auto-merge will NOT be gated by required checks.\"\n", g.envBranchRef())
	sb.WriteString("              echo \"::warning::Configure protection: gh api \\\"$PROT_PATH\\\" -X PUT -f required_status_checks.strict=true -F required_status_checks.contexts[]=hotfix-check\"\n")
	sb.WriteString("            fi\n")
	sb.WriteString("          done\n")

	// Seed both resolution-PR labels before any PR is opened. gh pr create
	// --label fails hard on a missing label; seeding here guarantees both the
	// clean and conflict PR paths can open. The `|| true` keeps the step green on
	// a repeated run where the label already exists, matching the seed command
	// the planner surfaces (protectionSuggestions in internal/hotfix/plan.go).
	sb.WriteString("      - name: Ensure hotfix labels exist\n")
	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          gh label create %s --color B60205 --description \"Cascade hotfix resolution PR\" || true\n", hotfixLabel)
	fmt.Fprintf(sb, "          gh label create %s --color D93F0B --description \"Cascade hotfix resolution PR with cherry-pick conflicts\" || true\n", hotfixConflictLabel)

	// Cherry-pick step: loop bottom-up over env_sequence, resolve per-env commit
	// lists and base SHAs from statically baked env vars, cherry-pick all commits
	// for each env, merge the clean PR, then proceed to the next env. On conflict,
	// open a resolution PR and break out of the loop - later envs are NOT touched.
	sb.WriteString("      - name: Cherry-pick and open resolution PRs\n")
	sb.WriteString("        env:\n")
	for _, env := range g.targetEnvs() {
		upper := strings.ToUpper(env)
		fmt.Fprintf(sb, "          COMMITS_%s: ${{ needs.plan.outputs.commits_%s }}\n", upper, env)
		fmt.Fprintf(sb, "          BASE_%s: ${{ needs.plan.outputs.base_%s }}\n", upper, env)
	}
	sb.WriteString("        run: |\n")
	// REMAINING tracks the envs still to process AFTER the current one. The loop
	// strips the current env (always at the front of REMAINING) before the body
	// runs, so on a conflict REMAINING names exactly the envs left untouched.
	sb.WriteString("          REMAINING=\"$ENV_SEQUENCE\"\n")
	sb.WriteString("          for env in $(echo \"$ENV_SEQUENCE\" | tr ',' '\\n'); do\n")
	sb.WriteString("            REMAINING=\"${REMAINING#\"$env\"}\"\n")
	sb.WriteString("            REMAINING=\"${REMAINING#,}\"\n")
	sb.WriteString("            case \"$env\" in\n")
	for _, env := range g.targetEnvs() {
		upper := strings.ToUpper(env)
		fmt.Fprintf(sb, "              %s) COMMITS=\"$COMMITS_%s\"; BASE=\"$BASE_%s\" ;;\n", env, upper, upper)
	}
	sb.WriteString("            esac\n")
	// A no-op env (all requested commits already present) has an empty commit
	// list; skip it and continue the chain to the next env.
	sb.WriteString("            if [ -z \"$COMMITS\" ]; then\n")
	fmt.Fprintf(sb, "              echo \"::notice::%s: all commits already present, skipping\"\n", g.envBranchRef())
	sb.WriteString("              continue\n")
	sb.WriteString("            fi\n")
	sb.WriteString("            FIRST_COMMIT=$(echo \"$COMMITS\" | cut -d',' -f1)\n")
	sb.WriteString("            SHORT_SHA=$(echo \"$FIRST_COMMIT\" | cut -c1-8)\n")
	sb.WriteString("            BRANCH=\"hotfix/${env}/${SHORT_SHA}\"\n")
	// Materialize env/<env> at the planner's validated base if origin lacks it,
	// so the resolution PR has a base branch; the plan enforces tip == BASE when
	// the branch already exists, so this is a no-op create in that case.
	fmt.Fprintf(sb, "            if ! git rev-parse --verify --quiet \"refs/remotes/origin/%s\" >/dev/null; then\n", g.envBranchRef())
	fmt.Fprintf(sb, "              git push origin \"${BASE}:refs/heads/%s\"\n", g.envBranchRef())
	fmt.Fprintf(sb, "              git fetch origin \"+refs/heads/%s:refs/remotes/origin/%s\"\n", g.envBranchRef(), g.envBranchRef())
	sb.WriteString("            fi\n")
	sb.WriteString("            git switch -c \"$BRANCH\" \"$BASE\"\n")
	// The PR-body trailers carry the full comma-joined set of applied trunk
	// commits and the base anchor so the post-merge context job can recover every
	// fix SHA (not just the first) and the base SHA. The Source trailer mirrors the
	// per-env $COMMITS list the cherry-pick loop below applies.
	sb.WriteString("            BODY=$(printf 'Cascade-Hotfix-Target: %s\\nCascade-Hotfix-Source: %s\\nCascade-Hotfix-Base: %s\\n' \"$env\" \"$COMMITS\" \"$BASE\")\n")
	sb.WriteString("            CLEAN=true\n")
	sb.WriteString("            CONFLICT_COMMIT=\"\"\n")
	sb.WriteString("            CONFLICTS=\"\"\n")
	sb.WriteString("            for commit in $(echo \"$COMMITS\" | tr ',' '\\n'); do\n")
	sb.WriteString("              if ! git cherry-pick -x \"$commit\"; then\n")
	sb.WriteString("                CLEAN=false\n")
	sb.WriteString("                CONFLICT_COMMIT=\"$commit\"\n")
	sb.WriteString("                CONFLICTS=$(git diff --name-only --diff-filter=U)\n")
	sb.WriteString("                git add -A\n")
	sb.WriteString("                git -c core.editor=true cherry-pick --continue || git commit -m \"hotfix: cherry-pick $(echo \"$commit\" | cut -c1-8) with conflicts\"\n")
	sb.WriteString("                break\n")
	sb.WriteString("              fi\n")
	sb.WriteString("            done\n")
	sb.WriteString("            if $CLEAN; then\n")
	sb.WriteString("              git push origin \"$BRANCH\"\n")
	sb.WriteString("              gh pr create \\\n")
	fmt.Fprintf(sb, "                --base \"%s\" \\\n", g.envBranchRef())
	sb.WriteString("                --head \"$BRANCH\" \\\n")
	fmt.Fprintf(sb, "                --label %s \\\n", hotfixLabel)
	sb.WriteString("                --title \"hotfix(${env}): cherry-pick ${SHORT_SHA}\" \\\n")
	sb.WriteString("                --body \"$BODY\"\n")
	// Poll mergeability for up to ~5 minutes (20 attempts, 15s apart) so a
	// required status check on a protected env branch has time to report, then
	// merge as the state token so the merge is trigger capable and reaches
	// finalize. The merge must complete before the loop advances so each env's
	// state lands before the next env cherry-picks onto it.
	sb.WriteString("              ATTEMPTS=20\n")
	sb.WriteString("              SLEEP=15\n")
	sb.WriteString("              MERGED=false\n")
	sb.WriteString("              for i in $(seq 1 \"$ATTEMPTS\"); do\n")
	sb.WriteString("                STATE=$(gh pr view \"$BRANCH\" --json mergeable,mergeStateStatus -q '.mergeable + \" \" + .mergeStateStatus' 2>/dev/null || echo \"UNKNOWN UNKNOWN\")\n")
	sb.WriteString("                MERGEABLE=$(echo \"$STATE\" | cut -d' ' -f1)\n")
	sb.WriteString("                STATUS=$(echo \"$STATE\" | cut -d' ' -f2)\n")
	sb.WriteString("                echo \"::notice::resolution PR mergeable=$MERGEABLE state=$STATUS (attempt $i/$ATTEMPTS)\"\n")
	sb.WriteString("                if [ \"$MERGEABLE\" = \"MERGEABLE\" ] && [ \"$STATUS\" != \"BLOCKED\" ]; then\n")
	sb.WriteString("                  if gh pr merge --squash --delete-branch \"$BRANCH\"; then\n")
	sb.WriteString("                    MERGED=true\n")
	sb.WriteString("                    break\n")
	sb.WriteString("                  fi\n")
	sb.WriteString("                fi\n")
	sb.WriteString("                sleep \"$SLEEP\"\n")
	sb.WriteString("              done\n")
	sb.WriteString("              if [ \"$MERGED\" != \"true\" ]; then\n")
	sb.WriteString("                echo \"::error::Resolution PR for $BRANCH did not become mergeable within the timeout; merge it manually to run the hotfix finalize chain\"\n")
	sb.WriteString("                exit 1\n")
	sb.WriteString("              fi\n")
	// Re-fetch the env branches so the next env in the chain cherry-picks onto the
	// just-merged tip.
	sb.WriteString("              git fetch origin '+refs/heads/env/*:refs/remotes/origin/env/*' --tags\n")
	sb.WriteString("            else\n")
	fmt.Fprintf(sb, "              echo \"::warning::Cherry-pick conflicted on %s; opening resolution PR and halting chain\"\n", g.envBranchRef())
	sb.WriteString("              git push origin \"$BRANCH\"\n")
	sb.WriteString("              CONFLICT_BODY=$(printf '%s\\n\\nConflicting files:\\n%s\\n\\nThis resolves %s.\\n\\nEnvironments still pending: %s.\\n\\nAfter merge, re-engage the hotfix workflow targeting %s.\\n\\nResolve locally:\\n  git fetch && git switch %s\\n  # resolve conflicts, then\\n  git push --force-with-lease\\n' \"$BODY\" \"$CONFLICTS\" \"$env\" \"$REMAINING\" \"$HOTFIX_TARGET_ENV\" \"$BRANCH\")\n")
	sb.WriteString("              gh pr create \\\n")
	fmt.Fprintf(sb, "                --base \"%s\" \\\n", g.envBranchRef())
	sb.WriteString("                --head \"$BRANCH\" \\\n")
	fmt.Fprintf(sb, "                --label %s \\\n", hotfixConflictLabel)
	sb.WriteString("                --title \"hotfix(${env}): cherry-pick $(echo \"$CONFLICT_COMMIT\" | cut -c1-8) (conflicts)\" \\\n")
	sb.WriteString("                --body \"$CONFLICT_BODY\"\n")
	sb.WriteString("              break\n")
	sb.WriteString("            fi\n")
	sb.WriteString("          done\n")
}

// writeCheckJob emits the parse-config validity gate that runs while a hotfix PR
// against an env/* branch is open or closing. Full required-status-check
// designation against the open PR is the operator's branch-protection
// responsibility, surfaced by the apply job's protection-warning step.
func (g *HotfixGenerator) writeCheckJob(sb *strings.Builder) {
	sb.WriteString("  check:\n")
	sb.WriteString("    name: Validate Hotfix PR\n")
	sb.WriteString("    if: github.event_name == 'pull_request' && github.event.pull_request.merged != true\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    steps:\n")
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")

	g.writeSetupCLI(sb)

	sb.WriteString("      - name: Validate manifest\n")
	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          MANIFEST_FILE=\"%s\"\n", g.getManifestFilePath())
	sb.WriteString("          RESULT=$(cascade parse-config --config \"$MANIFEST_FILE\")\n")
	sb.WriteString("          echo \"$RESULT\"\n")
	sb.WriteString("          VALID=$(echo \"$RESULT\" | jq -r '.valid // false')\n")
	sb.WriteString("          if [[ \"$VALID\" != \"true\" ]]; then\n")
	sb.WriteString("            echo \"$RESULT\" | jq -r '.errors[]? | \"::error::\" + .'\n")
	sb.WriteString("            echo \"::error::Manifest validation failed\"\n")
	sb.WriteString("            exit 1\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          echo \"::notice::Manifest is valid\"\n")
}

// writeContextJob derives the merged-hotfix target environment from the PR base
// ref and recovers the fix and base SHAs from the resolution PR body trailers
// (Cascade-Hotfix-Source / Cascade-Hotfix-Base, stamped by the apply job). It
// exposes these plus a rollback sha as outputs for the build, deploy, and
// finalize stages. The plan job does not run on the pull_request (merged) path,
// so its job outputs are unavailable here; the PR-body trailers are the carrier.
func (g *HotfixGenerator) writeContextJob(sb *strings.Builder) {
	sb.WriteString("  context:\n")
	sb.WriteString("    name: Hotfix Context\n")
	// The 'cascade-hotfix' literal must match hotfixLabel.
	sb.WriteString("    if: github.event_name == 'pull_request' && github.event.pull_request.merged == true && contains(github.event.pull_request.labels.*.name, 'cascade-hotfix')\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    outputs:\n")
	sb.WriteString("      target_env: ${{ steps.ctx.outputs.target_env }}\n")
	sb.WriteString("      fix_sha: ${{ steps.ctx.outputs.fix_sha }}\n")
	sb.WriteString("      base_sha: ${{ steps.ctx.outputs.base_sha }}\n")
	sb.WriteString("      rollback_sha: ${{ steps.ctx.outputs.rollback_sha }}\n")
	sb.WriteString("    steps:\n")
	// The context job reads the target env's pre-hotfix state SHA from the
	// committed manifest, so the repo must be checked out first. fetch-depth: 0
	// matches the other hotfix jobs that need full history available.
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")
	sb.WriteString("      - name: Derive target environment and hotfix SHAs\n")
	sb.WriteString("        id: ctx\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          BASE_REF: ${{ github.event.pull_request.base.ref }}\n")
	sb.WriteString("          PR_BODY: ${{ github.event.pull_request.body }}\n")
	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          TARGET_ENV=\"${BASE_REF#%s}\"\n", g.envBranchPrefix())
	// Recover the full comma-joined set of trunk fix commits and the trunk base
	// anchor from the trailers the apply job stamped into the resolution PR body.
	// The Source trailer carries every applied commit, so keeping the whole value
	// (rather than the first field) threads the complete set to finalize. grep
	// tolerates absent trailers (the || true) so the step never hard-fails here;
	// the finalize command enforces that the required SHAs are present.
	sb.WriteString("          FIX_SHA=$(printf '%s\\n' \"$PR_BODY\" | grep -m1 '^Cascade-Hotfix-Source:' | sed 's/^Cascade-Hotfix-Source:[[:space:]]*//' || true)\n")
	sb.WriteString("          BASE_SHA=$(printf '%s\\n' \"$PR_BODY\" | grep -m1 '^Cascade-Hotfix-Base:' | sed 's/^Cascade-Hotfix-Base:[[:space:]]*//' || true)\n")
	// Resolve the auto-rollback target: the target env's state SHA as recorded in
	// the manifest before this hotfix deploys (the N-1 deployment). yq emits "" for
	// an absent env/state so the downstream rollback gate (rollback_sha != '')
	// stays closed until a prior deployment exists. Mirrors the release generator's
	// ".$MANIFEST_KEY.state.<env>.sha" read.
	fmt.Fprintf(sb, "          MANIFEST_FILE=\"%s\"\n", g.getManifestFilePath())
	fmt.Fprintf(sb, "          MANIFEST_KEY=\"%s\"\n", g.config.GetManifestKey())
	// Component-scoped state nests under state.components.<name>.<env>; the
	// single-component form is the flat state.<env>. The read must match the scope
	// the finalize CLI wrote at so auto-rollback resolves this component's own N-1
	// SHA (and cannot read a sibling's).
	if g.componentName != "" {
		fmt.Fprintf(sb, "          ROLLBACK_SHA=$(yq eval \".$MANIFEST_KEY.state.components.%s.${TARGET_ENV}.sha // \\\"\\\"\" \"$MANIFEST_FILE\")\n", g.componentName)
	} else {
		sb.WriteString("          ROLLBACK_SHA=$(yq eval \".$MANIFEST_KEY.state.${TARGET_ENV}.sha // \\\"\\\"\" \"$MANIFEST_FILE\")\n")
	}
	sb.WriteString("          if [ \"$ROLLBACK_SHA\" = \"null\" ]; then ROLLBACK_SHA=\"\"; fi\n")
	sb.WriteString("          {\n")
	sb.WriteString("            echo \"target_env=${TARGET_ENV}\"\n")
	sb.WriteString("            echo \"fix_sha=${FIX_SHA}\"\n")
	sb.WriteString("            echo \"base_sha=${BASE_SHA}\"\n")
	sb.WriteString("            echo \"rollback_sha=${ROLLBACK_SHA}\"\n")
	sb.WriteString("          } >> \"$GITHUB_OUTPUT\"\n")
}

// mergedHotfixGuard is the if-condition gating the post-merge stages: the PR
// merged and carried the cascade-hotfix label.
func mergedHotfixGuard() string {
	// The 'cascade-hotfix' literal must match hotfixLabel.
	return "github.event_name == 'pull_request' && github.event.pull_request.merged == true && contains(github.event.pull_request.labels.*.name, 'cascade-hotfix')"
}

// writeBuildJobs emits one build job per configured build, run on the merged
// hotfix commit. With no builds configured a single no-op build job is emitted so
// downstream needs: references resolve.
func (g *HotfixGenerator) writeBuildJobs(sb *strings.Builder) {
	if len(g.config.Builds) == 0 {
		sb.WriteString("  build:\n")
		sb.WriteString("    name: Build Hotfix (no-op)\n")
		sb.WriteString("    needs: context\n")
		fmt.Fprintf(sb, "    if: %s\n", mergedHotfixGuard())
		sb.WriteString("    runs-on: ubuntu-latest\n")
		sb.WriteString("    steps:\n")
		sb.WriteString("      - name: No builds configured\n")
		sb.WriteString("        run: echo \"No builds configured; skipping build stage\"\n")
		return
	}

	for _, b := range g.config.Builds {
		fmt.Fprintf(sb, "  build-%s:\n", b.Name)
		fmt.Fprintf(sb, "    name: Build %s\n", b.Name)
		sb.WriteString("    needs: context\n")
		fmt.Fprintf(sb, "    if: %s\n", mergedHotfixGuard())
		if b.Workflow != "" {
			writeCallbackPermissions(sb, "    ", b.Permissions)
			fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(b.Workflow))
			sb.WriteString("    with:\n")
			sb.WriteString("      sha: ${{ github.event.pull_request.merge_commit_sha }}\n")
			sb.WriteString("      target_env: ${{ needs.context.outputs.target_env }}\n")
			// Honor the same opt-in / least-privilege model as the orchestrate and
			// promote callbacks: emit no secrets block unless the build opted in via
			// secrets: inherit or an explicit per-secret map.
			writeSecretsBlock(sb, b.Secrets)
			continue
		}
		// Hotfix build placeholder step: a steps-based job that echoes the build
		// name. Operators replace this scaffold with the real build commands.
		sb.WriteString("    runs-on: ubuntu-latest\n")
		sb.WriteString("    steps:\n")
		writeActionStep(sb, g.config, "      ", actionCheckout)
		sb.WriteString("        with:\n")
		sb.WriteString("          ref: ${{ github.event.pull_request.merge_commit_sha }}\n")
		fmt.Fprintf(sb, "      - name: Run build %s\n", b.Name)
		sb.WriteString("        run: |\n")
		fmt.Fprintf(sb, "          echo \"build %s\"\n", b.Name)
	}
}

// buildJobNames returns the build job identifiers emitted by writeBuildJobs so
// deploy jobs can declare correct needs: references.
func (g *HotfixGenerator) buildJobNames() []string {
	if len(g.config.Builds) == 0 {
		return []string{"build"}
	}
	names := make([]string, 0, len(g.config.Builds))
	for _, b := range g.config.Builds {
		names = append(names, "build-"+b.Name)
	}
	return names
}

// writeDeployJobs emits one deploy job per configured deploy, each gated on the
// merged-hotfix guard and bound to the target GitHub Environment for org
// protection gating. Each deploy is paired with a rollback job mirroring the
// promote workflow's rollback mechanics.
func (g *HotfixGenerator) writeDeployJobs(sb *strings.Builder) {
	buildNeeds := g.buildJobNames()
	needsList := append([]string{"context"}, buildNeeds...)
	needsStr := "[" + strings.Join(needsList, ", ") + "]"

	for _, d := range g.config.Deploys {
		fmt.Fprintf(sb, "  deploy-%s:\n", d.Name)
		fmt.Fprintf(sb, "    name: Deploy %s\n", d.Name)
		fmt.Fprintf(sb, "    needs: %s\n", needsStr)
		fmt.Fprintf(sb, "    if: %s\n", mergedHotfixGuard())
		// Decision 7: bind to the target GitHub Environment so org protection
		// rules (manual approval on prod, etc.) apply to the hotfix deploy. The
		// environment: key is invalid on a reusable-workflow (uses:) job, so the
		// hotfix deploy is a steps-based job that carries the gate and invokes the
		// deploy via the CLI; the configured deploy workflow path is recorded for
		// the operator in the step.
		sb.WriteString("    environment: ${{ needs.context.outputs.target_env }}\n")
		sb.WriteString("    runs-on: ubuntu-latest\n")
		sb.WriteString("    steps:\n")
		fmt.Fprintf(sb, "      - name: Run deploy %s\n", d.Name)
		sb.WriteString("        env:\n")
		sb.WriteString("          DEPLOY_ENV: ${{ needs.context.outputs.target_env }}\n")
		sb.WriteString("          DEPLOY_SHA: ${{ github.event.pull_request.merge_commit_sha }}\n")
		sb.WriteString("        run: |\n")
		if d.Workflow != "" {
			fmt.Fprintf(sb, "          echo \"deploy %s via %s to $DEPLOY_ENV at $DEPLOY_SHA\"\n", d.Name, normalizeWorkflowPath(d.Workflow))
		} else {
			fmt.Fprintf(sb, "          echo \"deploy %s to $DEPLOY_ENV at $DEPLOY_SHA\"\n", d.Name)
		}

		// Rollback job: gated on a rollback sha being available and the deploy
		// failing, mirroring the promote workflow's rollback shape.
		//
		// Hotfix auto-rollback is always-on when an N-1 SHA exists (rollback_sha
		// non-empty). Unlike promote, hotfix has no preflight job to carry an
		// explicit rollback_on_failure opt-in signal. Adding a new manifest knob is
		// out of scope; always-on matches the inherent N-1 model of hotfix
		// deployments.
		fmt.Fprintf(sb, "  rollback-%s:\n", d.Name)
		fmt.Fprintf(sb, "    name: Rollback %s\n", d.Name)
		fmt.Fprintf(sb, "    needs: [context, deploy-%s]\n", d.Name)
		fmt.Fprintf(sb, "    if: always() && needs.context.outputs.rollback_sha != '' && needs.deploy-%s.result == 'failure'\n", d.Name)
		sb.WriteString("    environment: ${{ needs.context.outputs.target_env }}\n")
		sb.WriteString("    runs-on: ubuntu-latest\n")
		sb.WriteString("    steps:\n")
		fmt.Fprintf(sb, "      - name: Rollback deploy %s\n", d.Name)
		sb.WriteString("        env:\n")
		sb.WriteString("          ROLLBACK_ENV: ${{ needs.context.outputs.target_env }}\n")
		sb.WriteString("          ROLLBACK_SHA: ${{ needs.context.outputs.rollback_sha }}\n")
		sb.WriteString("        run: |\n")
		fmt.Fprintf(sb, "          echo \"rollback %s in $ROLLBACK_ENV to $ROLLBACK_SHA\"\n", d.Name)
	}
}

// deployJobNames returns the deploy job identifiers so the finalize job can
// declare correct needs: references.
func (g *HotfixGenerator) deployJobNames() []string {
	names := make([]string, 0, len(g.config.Deploys))
	for _, d := range g.config.Deploys {
		names = append(names, "deploy-"+d.Name)
	}
	return names
}

// writeFinalizeJob emits the finalize job, run only after all deploys succeed on
// the merged-hotfix path. It runs `cascade hotfix finalize`.
func (g *HotfixGenerator) writeFinalizeJob(sb *strings.Builder) {
	deployNeeds := g.deployJobNames()
	needsList := append([]string{"context"}, deployNeeds...)
	needsStr := "[" + strings.Join(needsList, ", ") + "]"

	sb.WriteString("  finalize:\n")
	sb.WriteString("    name: Finalize Hotfix\n")
	fmt.Fprintf(sb, "    needs: %s\n", needsStr)
	fmt.Fprintf(sb, "    if: success() && %s\n", mergedHotfixGuard())
	sb.WriteString("    runs-on: ubuntu-latest\n")
	// The finalize job commits the post-hotfix state, so it needs contents: write.
	writeJobPermissions(sb, "    ", [][2]string{{"contents", "write"}})
	sb.WriteString("    env:\n")
	sb.WriteString("      TARGET_ENV: ${{ needs.context.outputs.target_env }}\n")
	// merge-sha is the tip of env/<target> after the resolution PR merged.
	sb.WriteString("      MERGE_SHA: ${{ github.event.pull_request.merge_commit_sha }}\n")
	// fix-sha and base-sha are recovered by the context job from the PR-body
	// trailers the apply job stamped (the plan job does not run on this event).
	sb.WriteString("      FIX_SHA: ${{ needs.context.outputs.fix_sha }}\n")
	sb.WriteString("      BASE_SHA: ${{ needs.context.outputs.base_sha }}\n")
	sb.WriteString("    steps:\n")
	writeMintSteps(sb, g.config, "      ", seamState)
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")

	g.writeSetupCLI(sb)
	// Finalize cross-checks the merge SHA against the env-branch tip, so the env
	// branches must be fetched into the checkout before the verb runs.
	g.writeFetchEnvBranches(sb)

	sb.WriteString("      - name: Finalize hotfix\n")
	sb.WriteString("        env:\n")
	// GH_TOKEN authenticates the Contents REST API write that finalize performs
	// against the protected trunk manifest. It must carry the configured state
	// token so the write bypasses trunk's require-pull-request rule
	// (enforce_admins=false), mirroring the promote and orchestrate finalize state
	// writes; the default GITHUB_TOKEN is github-actions[bot], which trunk
	// protection rejects with a 409. It defaults to GITHUB_TOKEN when no state
	// token is configured, so a repo with a protected trunk must supply a
	// bypass-capable state_token for finalize to record state there. GITHUB_TOKEN
	// authenticates the release/tag API calls, which are not gated by branch
	// protection. GITHUB_REPOSITORY names the target repo for both.
	fmt.Fprintf(sb, "          GH_TOKEN: %s\n", g.getStateTokenRef())
	sb.WriteString("          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n")
	sb.WriteString("          GITHUB_REPOSITORY: ${{ github.repository }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          cascade hotfix finalize \\\n")
	fmt.Fprintf(sb, "            --config %s \\\n", g.getManifestFilePath())
	g.writeComponentFlag(sb, "            ")
	sb.WriteString("            --target-env \"$TARGET_ENV\" \\\n")
	sb.WriteString("            --merge-sha \"$MERGE_SHA\" \\\n")
	sb.WriteString("            --fix-sha \"$FIX_SHA\" \\\n")
	sb.WriteString("            --base-sha \"$BASE_SHA\"\n")
}

// writeSetupCLI emits the setup-cli step, mirroring the merge-queue generator.
func (g *HotfixGenerator) writeSetupCLI(sb *strings.Builder) {
	sb.WriteString("      - name: Setup CLI\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())
	// github.token is the built-in Actions token, sufficient to authenticate
	// gh release download against the public stablekernel/cascade repository.
	sb.WriteString("          token: ${{ github.token }}\n")
}

// writeFetchEnvBranches emits a step that fetches the env/* branches and tags so
// the cherry-pick base and the planner have the integration history available.
func (g *HotfixGenerator) writeFetchEnvBranches(sb *strings.Builder) {
	sb.WriteString("      - name: Fetch env branches and tags\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          git fetch origin '+refs/heads/env/*:refs/remotes/origin/env/*' --tags\n")
}
