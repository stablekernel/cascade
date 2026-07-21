package generate

import (
	"fmt"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// RollbackGenerator emits the cascade-rollback workflow. The workflow re-deploys
// a prior version or SHA to an environment: a read-only preflight job resolves
// the target, one deploy job per configured deployable re-runs the deploy keyed
// on the resolved SHA, and a finalize job applies the state write back to trunk
// (marking the environment diverged until a forward promotion rejoins it).
//
// The deploy stage reuses the same deploy callbacks (reusable-workflow, or
// matrix) the promote workflow drives; there is no separate rollback deploy
// path. The generator is gated on the configured environment count: it
// emits only when at least one environment is declared.
type RollbackGenerator struct {
	installModeHolder
	config  *config.TrunkConfig
	baseDir string

	// componentName, when non-empty, names the component this rollback workflow is
	// scoped to. It suffixes the workflow name, threads --component through the
	// rollback CLI steps so preflight and finalize read and record state under this
	// component's subtree, and switches the concurrency group into a
	// rollback-namespaced per-component lane. It is set only via
	// WithRollbackComponentName by the per-component fan-out.
	componentName string

	// globalConcurrencyGroup is the manifest-global concurrency.group as declared
	// on the shared top-level config, captured before per-component resolution
	// overwrites it. ResolveComponent rewrites the resolved config's group to the
	// orchestrate lane, so component mode cannot honor it bare; instead this
	// captured value is composed with the rollback-namespaced per-component group
	// so a shared global group scopes per component rather than collapsing every
	// component's rollback onto one lane. It is set only via
	// WithRollbackGlobalConcurrencyGroup.
	globalConcurrencyGroup string
}

// RollbackGeneratorOption configures a RollbackGenerator. Options are additive so
// new per-component capability never breaks the positional constructor signature.
type RollbackGeneratorOption func(*RollbackGenerator)

// WithRollbackComponentName scopes the generated rollback workflow to a declared
// component so a multi-component manifest emits one distinct
// cascade-rollback-<name>.yaml per component. It sets the emitted workflow name,
// threads --component through the rollback CLI steps, and selects the
// rollback-namespaced per-component concurrency group.
func WithRollbackComponentName(name string) RollbackGeneratorOption {
	return func(g *RollbackGenerator) { g.componentName = name }
}

// WithRollbackGlobalConcurrencyGroup records the manifest-global concurrency.group
// before per-component resolution overwrites it, so component-mode writeConcurrency
// can compose it with the component identity instead of honoring the resolved
// (orchestrate-namespaced) group or collapsing every component onto one bare lane.
func WithRollbackGlobalConcurrencyGroup(group string) RollbackGeneratorOption {
	return func(g *RollbackGenerator) { g.globalConcurrencyGroup = group }
}

// NewRollbackGenerator creates a rollback-workflow generator bound to the given
// trunk config and repository base directory.
func NewRollbackGenerator(cfg *config.TrunkConfig, baseDir string, opts ...RollbackGeneratorOption) *RollbackGenerator {
	g := &RollbackGenerator{
		config:  cfg,
		baseDir: baseDir,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// writeComponentFlag emits a "--component <name> \" continuation line at the given
// indent when this rollback workflow is scoped to a component, so the rollback CLI
// reads and records state under that component's subtree. The single-component
// workflow emits nothing, keeping its CLI invocations byte-identical.
func (g *RollbackGenerator) writeComponentFlag(sb *strings.Builder, indent string) {
	if g.componentName != "" {
		fmt.Fprintf(sb, "%s--component %s \\\n", indent, g.componentName)
	}
}

// Enabled reports whether the rollback workflow should be emitted. It requires
// at least two environments: a rollback re-points a promoted environment at a
// prior deployment, and the first environment tracks trunk (it is never promoted
// into, so it has no rollback history and reverts via a merge to trunk instead).
// A single-environment project therefore has no rollbackable environment, so the
// workflow is not emitted, mirroring the hotfix generator.
func (g *RollbackGenerator) Enabled() bool {
	return g.config != nil && len(g.config.Environments) >= 2
}

// dispatchTrigger returns the configured opt-in repository_dispatch trigger, or
// nil when the rollback workflow is in its workflow_dispatch-only baseline.
func (g *RollbackGenerator) dispatchTrigger() *config.RepositoryDispatchTrigger {
	if g.config == nil || g.config.Rollback == nil {
		return nil
	}
	return g.config.Rollback.RepositoryDispatch
}

// paramRead returns the GitHub Actions expression body that reads a single
// rollback parameter. In the baseline it reads github.event.inputs.<name>; when
// the repository_dispatch trigger is enabled it coalesces to
// github.event.client_payload.<name> so the same workflow resolves the parameter
// whether it was fired by the manual (workflow_dispatch) or external
// (repository_dispatch) path. The two paths share one parameter name: the
// client_payload key matches the workflow_dispatch input name exactly.
func (g *RollbackGenerator) paramRead(name string) string {
	if g.dispatchTrigger() != nil {
		return fmt.Sprintf("github.event.inputs.%s || github.event.client_payload.%s", name, name)
	}
	return fmt.Sprintf("github.event.inputs.%s", name)
}

// getCLIRef mirrors the ref-resolution used by the other generators so the
// emitted setup-cli ref tracks config.cli_version. "beta" is the explicit opt-in
// escape hatch to the "master" branch; everything else resolves through
// GetCLIVersion (which pins "" / "latest" to the immutable default).
func (g *RollbackGenerator) getCLIRef() string {
	return cliSetupRef(g.config)
}

// getReleaseTokenRef returns the token expression for deploy/release operations.
func (g *RollbackGenerator) getReleaseTokenRef() string {
	return resolveReleaseTokenRef(g.config)
}

// getStateTokenRef returns the token expression used to write manifest state to
// the trunk branch.
func (g *RollbackGenerator) getStateTokenRef() string {
	return resolveStateTokenRef(g.config)
}

// getManifestFilePath returns the repo-relative manifest path for use in the
// generated workflow, matching the other generators' resolution.
func (g *RollbackGenerator) getManifestFilePath() string {
	return relativeManifestPath(g.config, g.baseDir)
}

// Generate renders the cascade-rollback workflow.
func (g *RollbackGenerator) Generate() (string, error) {
	var sb strings.Builder

	g.writeHeader(&sb)
	g.writeTriggers(&sb)
	g.writeConcurrency(&sb)
	g.writeJobs(&sb)

	return sb.String(), nil
}

func (g *RollbackGenerator) writeHeader(sb *strings.Builder) {
	sb.WriteString(GeneratedFileMarker + "\n")
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n", g.getManifestFilePath())
	sb.WriteString("#\n")
	sb.WriteString("# Manual rollback: re-deploy a prior version or SHA to an environment.\n")
	sb.WriteString("#\n")
	sb.WriteString("# A read-only preflight resolves the target (from live state, the\n")
	sb.WriteString("# deploy-history ring, or manifest history), the deploy stage re-runs the\n")
	sb.WriteString("# configured deploy callbacks keyed on the resolved SHA, and finalize writes\n")
	sb.WriteString("# the rolled-back state back to trunk, marking the environment diverged until\n")
	sb.WriteString("# a forward promotion rejoins it.\n")
	sb.WriteString("\n")
}

func (g *RollbackGenerator) writeTriggers(sb *strings.Builder) {
	if g.componentName != "" {
		fmt.Fprintf(sb, "name: Rollback (%s)\n\n", g.componentName)
	} else {
		sb.WriteString("name: Rollback\n\n")
	}
	sb.WriteString("on:\n")
	sb.WriteString("  workflow_dispatch:\n")
	sb.WriteString("    inputs:\n")

	// environment: enumerate the promoted environments as a choice so the operator
	// picks from the declared set rather than free-typing. The first environment
	// is excluded: it tracks trunk and is refused by the rollback runtime guard, so
	// offering it in the dropdown would only surface a guaranteed failure. Enabled
	// gates emission on at least two environments, so Environments[1:] is non-empty.
	sb.WriteString("      environment:\n")
	sb.WriteString("        description: 'Environment to roll back'\n")
	sb.WriteString("        required: true\n")
	sb.WriteString("        type: choice\n")
	sb.WriteString("        options:\n")
	promoted := g.config.EnvironmentNames()
	if len(promoted) > 0 {
		promoted = promoted[1:]
	}
	for _, env := range promoted {
		fmt.Fprintf(sb, "          - %s\n", env)
	}

	sb.WriteString("      target:\n")
	sb.WriteString("        description: 'Prior version or SHA (optional; defaults to the previous version)'\n")
	sb.WriteString("        required: false\n")
	sb.WriteString("        type: string\n")
	sb.WriteString("        default: ''\n")

	sb.WriteString("      deployable:\n")
	sb.WriteString("        description: 'Limit rollback to one deployable (optional)'\n")
	sb.WriteString("        required: false\n")
	sb.WriteString("        type: string\n")
	sb.WriteString("        default: ''\n")

	sb.WriteString("      dry_run:\n")
	sb.WriteString("        description: 'Resolve and print without deploying'\n")
	sb.WriteString("        required: false\n")
	sb.WriteString("        type: boolean\n")
	sb.WriteString("        default: false\n")

	// Opt-in repository_dispatch (#181): an external system (an alerting or
	// incident pipeline) fires the same N-1 rollback the manual path performs by
	// calling the dispatches API. repository_dispatch carries no inputs; the
	// rollback parameters travel in client_payload (keys: environment, target,
	// deployable, dry_run), which the jobs below coalesce with the manual inputs.
	g.writeRepositoryDispatchTrigger(sb)

	sb.WriteString("\n")

	// Default to a least-privilege top-level block: reads only. The finalize job
	// carries contents: write to commit the rolled-back state. Rollback has no
	// release dispatch, so no actions: write is granted anywhere. A deploy
	// callback's own scopes (e.g. id-token: write for OIDC) are scoped to its
	// caller job via writeCallbackPermissions.
	base := [][2]string{
		{"contents", "read"},
	}
	writeTopLevelPermissions(sb, base)
}

// writeRepositoryDispatchTrigger emits the opt-in repository_dispatch entry
// under on: when the rollback config enables it. The emission mirrors the main
// orchestrate workflow's repository_dispatch block (Generator.writeExtraTriggers)
// so the two are byte-consistent. When the trigger is absent, nothing is written
// and the workflow stays workflow_dispatch-only.
func (g *RollbackGenerator) writeRepositoryDispatchTrigger(sb *strings.Builder) {
	rd := g.dispatchTrigger()
	if rd == nil {
		return
	}
	sb.WriteString("  repository_dispatch:\n")
	if len(rd.Types) > 0 {
		sb.WriteString("    types:\n")
		for _, t := range rd.Types {
			fmt.Fprintf(sb, "      - %s\n", t)
		}
	}
}

// writeConcurrency serializes rollback runs so concurrent state writes cannot
// interleave. The default group keys on the workflow; an explicit config group
// overrides it, mirroring the promote generator.
func (g *RollbackGenerator) writeConcurrency(sb *strings.Builder) {
	sb.WriteString("concurrency:\n")

	// Component mode: emit a rollback-namespaced per-component group and ignore the
	// resolved config's group entirely. ResolveComponent rewrites the resolved
	// group to the orchestrate lane, so honoring it would serialize this
	// component's rollback against its own orchestrate run (a repo-global lane
	// collision); a manifest-global concurrency.group emitted bare would collapse
	// every component's rollback onto one literal lane. The composed key always
	// carries the component identity, so two components never share a lane, and it
	// never collides with the orchestrate or promote namespaces. cancel-in-progress
	// stays false: rollback mutates durable env state, so queueing is safer than
	// cancelling a mid-flight run.
	if g.componentName != "" {
		group := config.RollbackConcurrencyGroup(g.componentName)
		if g.globalConcurrencyGroup != "" {
			group = fmt.Sprintf("%s-%s", g.globalConcurrencyGroup, group)
		}
		fmt.Fprintf(sb, "  group: %s\n", group)
		sb.WriteString("  cancel-in-progress: false\n")
		sb.WriteString("\n")
		return
	}

	// Single-component mode. An operator-supplied group is namespaced into the
	// rollback lane rather than emitted bare: bare, it would be byte-identical to
	// the group emitted on orchestrate, release, promote and external-update, and a
	// concurrency group shared across workflows cancels all-but-the-latest PENDING
	// run repo-wide. cancel-in-progress stays pinned false for the same reason it
	// is pinned in component mode: rollback mutates durable env state, so a
	// mid-flight cancel leaves state partially written.
	group := config.WorkflowConcurrencyGroup(g.config.Concurrency, "rollback", `"${{ github.workflow }}"`)
	fmt.Fprintf(sb, "  group: %s\n", group)
	sb.WriteString("  cancel-in-progress: false\n")
	sb.WriteString("\n")
}

func (g *RollbackGenerator) writeJobs(sb *strings.Builder) {
	sb.WriteString("jobs:\n")
	g.writePreflightJob(sb)
	g.writeDeployJobs(sb)
	g.writeFinalizeJob(sb)
}

// writeSetupCLI emits the checkout + setup-cli steps shared by the rollback jobs.
func (g *RollbackGenerator) writeSetupCLI(sb *strings.Builder) {
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")
	writeSetupCLIStep(sb, setupCLIStep{
		installMode:        g.installMode,
		ref:                g.getCLIRef(),
		version:            g.config.GetCLIVersion(),
		token:              g.getReleaseTokenRef(),
		tokenBeforeVersion: true,
	})
}

// writePreflightJob emits the read-only target-resolution job. It exposes the
// resolved environment, SHA, version, and can_proceed gate as outputs for the
// deploy and finalize jobs, and fails fast when resolution reports it cannot
// proceed.
func (g *RollbackGenerator) writePreflightJob(sb *strings.Builder) {
	sb.WriteString("  preflight:\n")
	sb.WriteString("    name: Pre-flight Check\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    outputs:\n")
	sb.WriteString("      target_env: ${{ steps.preflight.outputs.target_env }}\n")
	sb.WriteString("      target_sha: ${{ steps.preflight.outputs.target_sha }}\n")
	sb.WriteString("      target_version: ${{ steps.preflight.outputs.target_version }}\n")
	sb.WriteString("      target_source: ${{ steps.preflight.outputs.target_source }}\n")
	sb.WriteString("      can_proceed: ${{ steps.preflight.outputs.can_proceed }}\n")
	sb.WriteString("    steps:\n")
	writeMintSteps(sb, g.config, "      ", seamRelease)
	g.writeSetupCLI(sb)

	sb.WriteString("      - name: Resolve Target\n")
	sb.WriteString("        id: preflight\n")
	sb.WriteString("        env:\n")
	fmt.Fprintf(sb, "          ENVIRONMENT: ${{ %s }}\n", g.paramRead("environment"))
	fmt.Fprintf(sb, "          TARGET: ${{ %s }}\n", g.paramRead("target"))
	fmt.Fprintf(sb, "          DEPLOYABLE: ${{ %s }}\n", g.paramRead("deployable"))
	sb.WriteString("        run: |\n")
	sb.WriteString("          cascade rollback preflight \\\n")
	fmt.Fprintf(sb, "            --config %s \\\n", g.getManifestFilePath())
	g.writeComponentFlag(sb, "            ")
	sb.WriteString("            --env \"$ENVIRONMENT\" \\\n")
	sb.WriteString("            --to \"$TARGET\" \\\n")
	sb.WriteString("            --deployable \"$DEPLOYABLE\" \\\n")
	sb.WriteString("            --gha-output\n")

	sb.WriteString("      - name: Fail if Cannot Proceed\n")
	sb.WriteString("        if: steps.preflight.outputs.can_proceed == 'false'\n")
	sb.WriteString("        run: exit 1\n")
	sb.WriteString("      - name: Report Resolved Source\n")
	sb.WriteString("        run: echo \"rollback resolved from ${{ steps.preflight.outputs.target_source }}\"\n")
	sb.WriteString("\n")
}

// paramReadExpr returns the parameter read for use inside a larger GitHub
// Actions expression (a comparison or boolean operand). In the baseline it is
// the bare github.event.inputs.<name>; when the repository_dispatch trigger is
// enabled the coalescing "inputs || client_payload" is wrapped in parentheses so
// the surrounding operator (e.g. != 'true', == '') binds to the whole coalesced
// value rather than only the client_payload half.
func (g *RollbackGenerator) paramReadExpr(name string) string {
	if g.dispatchTrigger() != nil {
		return "(" + g.paramRead(name) + ")"
	}
	return g.paramRead(name)
}

// rollbackDeployGuard is the if-condition gating a rollback deploy job: not a
// dry run, and either no deployable filter or a filter naming this deployable.
// The dry_run and deployable reads coalesce client_payload when the
// repository_dispatch trigger is enabled, so an external signal honors the same
// dry-run and deployable scoping the manual path does.
func (g *RollbackGenerator) rollbackDeployGuard(deployName string) string {
	deployable := g.paramReadExpr("deployable")
	return fmt.Sprintf("${{ %s && (%s == '' || %s == '%s') }}", g.notDryRunExpr(), deployable, deployable, deployName)
}

// notDryRunExpr returns the expression body (no ${{ }} wrapper) that is true
// when the rollback is NOT a dry run, so a deploy or finalize job may proceed.
//
// Without the repository_dispatch trigger the dry_run signal is only ever the
// workflow_dispatch input, which is always the string 'true' or 'false', so the
// output collapses to the bare string comparison and stays byte-identical to the
// baseline. With the trigger enabled the coalesced read can also carry a JSON
// boolean true from client_payload (a natural {"dry_run": true} payload), and
// GitHub Actions compares a boolean against the string 'true' by numeric
// coercion (the string casts to NaN), so a bare "!= 'true'" reads a boolean true
// as NOT a dry run and lets a dry-run rollback perform real deploys. Matching
// both the boolean literal and the string closes that gap for either source.
func (g *RollbackGenerator) notDryRunExpr() string {
	if g.dispatchTrigger() == nil {
		return "github.event.inputs.dry_run != 'true'"
	}
	v := g.paramRead("dry_run") // github.event.inputs.dry_run || github.event.client_payload.dry_run
	return fmt.Sprintf("(%s) != true && (%s) != 'true'", v, v)
}

// writeDeployJobs emits one deploy job per configured deploy, re-running the same
// callback the promote workflow uses but sourced from the resolved rollback
// target SHA. Each deploy is a reusable (uses:) workflow call that threads the
// resolved env and SHA via the with: input. With no environments configured, no
// deploy jobs emit.
func (g *RollbackGenerator) writeDeployJobs(sb *strings.Builder) {
	if len(g.config.Environments) == 0 {
		return
	}

	for _, d := range g.config.Deploys {
		fmt.Fprintf(sb, "  deploy-%s:\n", d.Name)
		fmt.Fprintf(sb, "    name: Deploy %s\n", d.Name)
		sb.WriteString("    needs: [preflight]\n")
		fmt.Fprintf(sb, "    if: %s\n", g.rollbackDeployGuard(d.Name))

		// Reusable (uses:) deploy: thread the resolved env and SHA via with:. The
		// environment name is carried as an input; GitHub Environment protection
		// must be declared inside the reusable workflow's own job.
		writeCallbackPermissions(sb, "    ", d.Permissions)
		fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(d.Workflow))
		sb.WriteString("    with:\n")
		sb.WriteString("      environment: ${{ needs.preflight.outputs.target_env }}\n")
		sb.WriteString("      sha: ${{ needs.preflight.outputs.target_sha }}\n")
		writeSecretsBlock(sb, d.Secrets)
	}
}

// deployJobNames returns the deploy job identifiers so finalize can declare
// correct needs: references.
func (g *RollbackGenerator) deployJobNames() []string {
	names := make([]string, 0, len(g.config.Deploys))
	for _, d := range g.config.Deploys {
		names = append(names, "deploy-"+d.Name)
	}
	return names
}

// writeFinalizeJob emits the state-write job. It runs after preflight succeeds
// (and after every deploy job), skipping on a dry run, and re-resolves the target
// deterministically via the passed-through SHA before applying and committing.
//
// The job condition uses always() so finalize still runs when a deploy job
// fails or is skipped: finalize must observe every deploy result to decide
// whether the state write is safe. Each deploy's conclusion is threaded in as a
// DEPLOY_RESULT_<NAME> env var, and the CLI aborts the state write (leaving
// trunk unchanged) when an in-scope deploy did not succeed. always() guarantees
// finalize reaches that gate rather than being skipped by a failed dependency.
func (g *RollbackGenerator) writeFinalizeJob(sb *strings.Builder) {
	needsList := append([]string{"preflight"}, g.deployJobNames()...)
	needsStr := "[" + strings.Join(needsList, ", ") + "]"

	sb.WriteString("  finalize:\n")
	sb.WriteString("    name: Finalize\n")
	fmt.Fprintf(sb, "    needs: %s\n", needsStr)
	fmt.Fprintf(sb, "    if: always() && needs.preflight.result == 'success' && %s\n", g.notDryRunExpr())
	sb.WriteString("    runs-on: ubuntu-latest\n")
	// The finalize job commits the rolled-back state, so it needs contents: write.
	writeJobPermissions(sb, "    ", [][2]string{{"contents", "write"}})
	sb.WriteString("    steps:\n")
	writeMintSteps(sb, g.config, "      ", seamRelease, seamState)
	g.writeSetupCLI(sb)

	sb.WriteString("      - name: Finalize Rollback\n")
	sb.WriteString("        env:\n")
	fmt.Fprintf(sb, "          GH_TOKEN: %s\n", g.getStateTokenRef())
	fmt.Fprintf(sb, "          GITHUB_TOKEN: %s\n", g.getReleaseTokenRef())
	sb.WriteString("          GITHUB_REPOSITORY: ${{ github.repository }}\n")
	// Thread the dispatch deployable scope through so finalize applies and gates
	// at the same scope the deploy jobs ran at. An empty value is env-scope, the
	// same default the CLI flag carries, so a full-env rollback still mirrors and
	// marks the env diverged while a deployable-scoped rollback touches only that
	// deployable. Without this, a deployable-scoped dispatch would resolve and
	// deploy one deployable but finalize the whole environment.
	fmt.Fprintf(sb, "          DEPLOYABLE: ${{ %s }}\n", g.paramRead("deployable"))
	// Thread each deploy job's conclusion in as DEPLOY_RESULT_<NAME> so the CLI
	// can gate the state write on actual deploy success. Deploy jobs only exist
	// when at least one environment is configured.
	if len(g.config.Environments) > 0 {
		for _, d := range g.config.Deploys {
			envKey := "DEPLOY_RESULT_" + strings.ToUpper(strings.ReplaceAll(d.Name, "-", "_"))
			fmt.Fprintf(sb, "          %s: ${{ needs.deploy-%s.result }}\n", envKey, d.Name)
		}
	}
	sb.WriteString("        run: |\n")
	sb.WriteString("          cascade rollback finalize \\\n")
	fmt.Fprintf(sb, "            --config %s \\\n", g.getManifestFilePath())
	g.writeComponentFlag(sb, "            ")
	sb.WriteString("            --env \"${{ needs.preflight.outputs.target_env }}\" \\\n")
	sb.WriteString("            --to \"${{ needs.preflight.outputs.target_sha }}\" \\\n")
	sb.WriteString("            --deployable \"$DEPLOYABLE\" \\\n")
	sb.WriteString("            --commit-push\n")
}
