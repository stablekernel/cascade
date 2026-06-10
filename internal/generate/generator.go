package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// DefaultJobTimeoutMinutes is the timeout-minutes applied to cascade-owned jobs
// (setup, finalize, inline run: callbacks, retry shims, and passthrough
// artifact helper jobs) when config.job_timeout_minutes is not set. GitHub
// Actions defaults jobs to 360 minutes (6 hours); cascade's orchestration jobs
// are meant to be fast, so a hung git push, CLI download, or API call should not
// hold a runner for six hours. Override per manifest via config.job_timeout_minutes.
const DefaultJobTimeoutMinutes = 30

// normalizeWorkflowPath adds ./ prefix to local workflow paths (required by GitHub Actions)
func normalizeWorkflowPath(path string) string {
	if strings.HasPrefix(path, ".github/") {
		return "./" + path
	}
	return path
}

// envGHAName returns the GitHub Environment name for a given cascade environment
// name. When the config has an EnvironmentConfig entry for that env whose
// GHAEnvironment field is non-empty, that value is returned; otherwise the
// cascade env name itself is used as the GitHub Environment name.
func envGHAName(cfg *config.TrunkConfig, cascadeEnvName string) string {
	if ec, ok := cfg.EnvironmentConfig[cascadeEnvName]; ok && ec.GHAEnvironment != "" {
		return ec.GHAEnvironment
	}
	return cascadeEnvName
}

// anyEnvHasGHAConfig reports whether any environment in the config has an
// EnvironmentConfig entry with a non-empty GHAEnvironment field.
func anyEnvHasGHAConfig(cfg *config.TrunkConfig) bool {
	for _, ec := range cfg.EnvironmentConfig {
		if ec.GHAEnvironment != "" {
			return true
		}
	}
	return false
}

// writeGitConfigSteps writes git configuration steps based on config.git settings
// indent is the number of spaces for each line
func writeGitConfigSteps(sb *strings.Builder, cfg *config.TrunkConfig, indent string) {
	mode := cfg.GetGitMode()

	// External mode: skip git config entirely
	if mode == config.GitModeExternal {
		sb.WriteString(indent + "# Git identity configured externally\n")
		return
	}

	// Default or custom mode: configure git identity
	userName := cfg.GetGitUserName()
	userEmail := cfg.GetGitUserEmail()

	fmt.Fprintf(sb, "%sgit config user.name \"%s\"\n", indent, userName)
	fmt.Fprintf(sb, "%sgit config user.email \"%s\"\n", indent, userEmail)

	// GPG signing if configured
	if cfg.HasGPGSigning() {
		// Import GPG key and configure signing
		fmt.Fprintf(sb, "%secho \"${{ secrets.%s }}\" | gpg --batch --import\n", indent, cfg.Git.GPGKeySecret)
		fmt.Fprintf(sb, "%sgit config commit.gpgsign true\n", indent)
		fmt.Fprintf(sb, "%sgit config user.signingkey \"${{ secrets.%s }}\"\n", indent, cfg.Git.GPGKeyID)
	}
}

// Generator handles workflow file generation
type Generator struct {
	config         *config.TrunkConfig
	baseDir        string
	outputs        map[string][]string // callback name -> output names
	inputs         map[string][]string // callback name -> input names
	requiredInputs map[string][]string // callback name -> required input names
	graph          *DependencyGraph
	warnings       []string
	// state is the manifest state block, used to resolve cascade-owned
	// ${{ state.<env>.<field> }} references in callback inputs at generation
	// time. Optional: nil when no state is threaded (e.g. unit tests).
	state map[string]*config.EnvState
}

// NewGenerator creates a new workflow generator
func NewGenerator(cfg *config.TrunkConfig, baseDir string) *Generator {
	return &Generator{
		config:         cfg,
		baseDir:        baseDir,
		outputs:        make(map[string][]string),
		inputs:         make(map[string][]string),
		requiredInputs: make(map[string][]string),
	}
}

// ownedJobTimeoutMinutes returns the timeout-minutes to emit on cascade-owned
// jobs: the manifest's config.job_timeout_minutes when set (>0), otherwise
// DefaultJobTimeoutMinutes. Reusable-workflow callbacks (jobs.<id>.uses) own
// their own timeout and are never bounded by this value.
func (g *Generator) ownedJobTimeoutMinutes() int {
	if g.config.JobTimeoutMinutes > 0 {
		return g.config.JobTimeoutMinutes
	}
	return DefaultJobTimeoutMinutes
}

// writeOwnedTimeout emits the timeout-minutes line for a cascade-owned job at
// the given indent.
func (g *Generator) writeOwnedTimeout(sb *strings.Builder, indent string) {
	fmt.Fprintf(sb, "%stimeout-minutes: %d\n", indent, g.ownedJobTimeoutMinutes())
}

// SetState threads the manifest state block into the generator so
// ${{ state.<env>.<field> }} input references resolve at generation time.
func (g *Generator) SetState(state map[string]*config.EnvState) {
	g.state = state
}

// getCLIRef returns the Git ref to use for the cascade actions.
// Supported values:
//   - "latest" → uses the "latest" tag (updated with each stable release)
//   - "beta" → uses "master" branch (bleeding edge, may be unstable)
//   - "vX.Y.Z" → uses a specific version tag
func (g *Generator) getCLIRef() string {
	version := g.config.GetCLIVersion()
	switch version {
	case "latest", "":
		return "latest" // Points to the most recent stable release
	case "beta":
		return "master" // Bleeding edge from trunk
	default:
		return version // Specific version tag (e.g., v1.0.0)
	}
}

// getReleaseTokenRef returns the token expression for release operations.
// Users configure the full expression via release_token config option.
func (g *Generator) getReleaseTokenRef() string {
	return g.config.GetReleaseToken()
}

// getManifestFilePath returns the manifest file path for use in generated scripts.
// Converts absolute paths to repo-relative paths since workflows run in checked out repos.
func (g *Generator) getManifestFilePath() string {
	manifestPath := g.config.GetManifestFile()

	// If it's already relative, return as-is
	if !filepath.IsAbs(manifestPath) {
		return manifestPath
	}

	// If baseDir is set and manifestPath starts with it, make relative
	if g.baseDir != "" {
		if rel, err := filepath.Rel(g.baseDir, manifestPath); err == nil {
			return rel
		}
	}

	// Fallback: return default relative path
	return ".github/manifest.yaml"
}

// getManifestKey returns the manifest key for nested access
func (g *Generator) getManifestKey() string {
	return g.config.GetManifestKey()
}

// getActionPath returns the path to the manage-release action
func (g *Generator) getActionPath() string {
	return fmt.Sprintf("./.github/actions/%s", g.config.GetActionFolder())
}

// Generate creates the orchestration workflow content
func (g *Generator) Generate() (string, error) {
	// Build dependency graph
	g.graph = BuildDependencyGraph(g.config)

	// Parse all workflow files to discover outputs/inputs
	if err := g.discoverOutputsAndInputs(); err != nil {
		return "", err
	}

	// Validate that all required inputs can be satisfied
	if err := g.validateRequiredInputs(); err != nil {
		return "", err
	}

	// Generate workflow YAML
	var sb strings.Builder

	g.writeHeader(&sb)
	g.writeWorkflowTriggers(&sb)
	g.writeConcurrency(&sb)
	g.writePermissions(&sb)
	// Note: top-level outputs removed - only valid for workflow_call triggers
	// Outputs are still available via jobs.finalize.outputs.* if needed
	g.writeJobs(&sb)

	return sb.String(), nil
}

// Validate checks for potential issues and returns warnings
func (g *Generator) Validate() []string {
	g.warnings = nil

	// Build dependency graph and discover outputs/inputs
	g.graph = BuildDependencyGraph(g.config)
	if err := g.discoverOutputsAndInputs(); err != nil {
		g.warnings = append(g.warnings, fmt.Sprintf("error discovering outputs: %v", err))
		return g.warnings
	}

	// Check that dependents have inputs for dependency outputs
	for _, node := range g.graph.Nodes {
		deps := g.graph.GetDirectDependencies(node.JobID)
		declaredInputs := g.inputs[node.JobID]
		inputSet := make(map[string]bool)
		for _, in := range declaredInputs {
			inputSet[in] = true
		}

		// Check that callback declares inputs for dependency outputs
		for _, depJobID := range deps {
			depInfo := g.graph.Nodes[depJobID]
			for _, out := range g.outputs[depInfo.JobID] {
				if !inputSet[out] {
					g.warnings = append(g.warnings,
						fmt.Sprintf("Warning: %s depends on '%s' but doesn't declare input '%s'",
							node.DisplayName, depInfo.DisplayName, out))
				}
			}
		}
	}

	// Validate that all required inputs can be satisfied
	if err := g.validateRequiredInputs(); err != nil {
		g.warnings = append(g.warnings, fmt.Sprintf("ERROR: %v", err))
	}

	// Warn when builds are configured but no publish callback is present.
	// When a release is published, build artifacts (Docker images, Helm
	// charts, etc.) remain tagged with their RC versions unless the user
	// provides a publish workflow to retag them. This is the most common
	// gap: builds are defined, but the publish: callback is missing.
	if len(g.config.Builds) > 0 && g.config.Publish == nil {
		g.warnings = append(g.warnings,
			"Note: builds are configured but no publish: callback is defined. "+
				"When a release is published, artifact registries will still hold "+
				"the RC-tagged versions (e.g., v1.0.0-rc.2). Add a publish: workflow "+
				"to retag artifacts on release.")
	}

	// Warn when an input value looks like an expression the operator forgot to
	// wrap in ${{ }} (e.g. a bare vars.X or state.prod.sha). Such values are
	// emitted as dead literals; the operator likely intended passthrough.
	g.warnings = append(g.warnings, g.unwrappedExpressionWarnings()...)

	return g.warnings
}

// unwrappedExpressionWarnings scans all callback inputs/env_inputs for literal
// values that resemble an unwrapped expression (bare context.path) and returns
// a warning for each, so operators catch a forgotten ${{ }} wrapper.
func (g *Generator) unwrappedExpressionWarnings() []string {
	var warnings []string
	check := func(callback string, inputs map[string]interface{}, envInputs map[string]map[string]interface{}) {
		report := func(key string, v interface{}) {
			if s, ok := v.(string); ok && looksLikeUnwrappedExpression(s) {
				warnings = append(warnings, fmt.Sprintf(
					"Warning: %s input %q value %q looks like an expression missing its ${{ }} wrapper; "+
						"it will be emitted as a literal. Wrap it as ${{ %s }} for passthrough.",
					callback, key, s, strings.TrimSpace(s)))
			}
		}
		for k, v := range inputs {
			report(k, v)
		}
		for _, env := range envInputs {
			for k, v := range env {
				report(k, v)
			}
		}
	}
	for i := range g.config.Builds {
		check("build "+g.config.Builds[i].Name, g.config.Builds[i].Inputs, g.config.Builds[i].EnvInputs)
	}
	for i := range g.config.Deploys {
		check("deploy "+g.config.Deploys[i].Name, g.config.Deploys[i].Inputs, g.config.Deploys[i].EnvInputs)
	}
	if g.config.Validate != nil {
		check("validate", g.config.Validate.Inputs, g.config.Validate.EnvInputs)
	}
	return warnings
}

// validateRequiredInputs checks that all required workflow inputs can be provided
func (g *Generator) validateRequiredInputs() error {
	// Standard inputs always provided by the generator
	standardInputs := map[string]bool{
		"environment": true,
		"sha":         true,
	}
	// Operator dispatch_inputs are available to any callback that declares them.
	for name := range g.config.DispatchInputs {
		standardInputs[name] = true
	}

	var errors []string

	for _, node := range g.graph.Nodes {
		requiredInputs := g.requiredInputs[node.JobID]
		if len(requiredInputs) == 0 {
			continue
		}

		// Collect all available inputs for this callback
		availableInputs := make(map[string]string) // input name -> source description

		// Standard inputs
		for input := range standardInputs {
			availableInputs[input] = "generator (standard)"
		}

		// Outputs from dependencies
		deps := g.graph.GetDirectDependencies(node.JobID)
		for _, depJobID := range deps {
			depInfo := g.graph.Nodes[depJobID]
			for _, out := range g.outputs[depInfo.JobID] {
				availableInputs[out] = fmt.Sprintf("output from %s", depInfo.DisplayName)
			}
		}

		// Check each required input
		for _, required := range requiredInputs {
			if _, ok := availableInputs[required]; !ok {
				errors = append(errors,
					fmt.Sprintf("%s requires input '%s' but it cannot be provided (not a standard input and no dependency outputs it)",
						node.DisplayName, required))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("required input validation failed:\n  - %s", strings.Join(errors, "\n  - "))
	}
	return nil
}

func (g *Generator) discoverOutputsAndInputs() error {
	// Discover from all callbacks
	// Use job ID as key to avoid collisions when builds and deploys share names
	allCallbacks := []struct {
		jobID    string
		name     string
		workflow string
		run      string
		inputs   map[string]interface{}
	}{}

	if g.config.Validate != nil {
		allCallbacks = append(allCallbacks, struct {
			jobID    string
			name     string
			workflow string
			run      string
			inputs   map[string]interface{}
		}{"validate", "validate", g.config.Validate.Workflow, g.config.Validate.Run, g.config.Validate.Inputs})
	}
	for _, b := range g.config.Builds {
		jobID := config.JobID(config.CallbackTypeBuild, b.Name)
		allCallbacks = append(allCallbacks, struct {
			jobID    string
			name     string
			workflow string
			run      string
			inputs   map[string]interface{}
		}{jobID, b.Name, b.Workflow, b.Run, b.Inputs})
	}
	for _, d := range g.config.Deploys {
		jobID := config.JobID(config.CallbackTypeDeploy, d.Name)
		allCallbacks = append(allCallbacks, struct {
			jobID    string
			name     string
			workflow string
			run      string
			inputs   map[string]interface{}
		}{jobID, d.Name, d.Workflow, d.Run, d.Inputs})
	}

	for _, cb := range allCallbacks {
		// Inline run: callbacks have no reusable-workflow file. Their inputs come
		// from the manifest (declared inputs keys); they emit no outputs.
		if cb.run != "" {
			g.outputs[cb.jobID] = nil
			g.inputs[cb.jobID] = inputKeys(cb.inputs)
			g.requiredInputs[cb.jobID] = nil
			continue
		}

		path := filepath.Join(g.baseDir, cb.workflow)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading workflow %s: %w", cb.workflow, err)
		}

		outputs, err := ParseWorkflowOutputs(data)
		if err != nil {
			return fmt.Errorf("parsing outputs from %s: %w", cb.workflow, err)
		}
		g.outputs[cb.jobID] = outputs

		inputs, err := ParseWorkflowInputs(data)
		if err != nil {
			return fmt.Errorf("parsing inputs from %s: %w", cb.workflow, err)
		}
		g.inputs[cb.jobID] = inputs

		requiredInputs, err := ParseWorkflowRequiredInputs(data)
		if err != nil {
			return fmt.Errorf("parsing required inputs from %s: %w", cb.workflow, err)
		}
		g.requiredInputs[cb.jobID] = requiredInputs
	}

	return nil
}

func (g *Generator) writeHeader(sb *strings.Builder) {
	sb.WriteString("# AUTO-GENERATED by cascade - DO NOT EDIT MANUALLY\n")
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n\n", g.config.GetManifestFile())
}

func (g *Generator) writeWorkflowTriggers(sb *strings.Builder) {
	sb.WriteString("name: Orchestrate CI/CD\n\n")
	sb.WriteString("on:\n")
	sb.WriteString("  push:\n")
	fmt.Fprintf(sb, "    branches: [%s]\n", g.config.TrunkBranch)

	// Add paths filter based on all configured triggers
	// This prevents orchestration from running when no relevant files changed
	triggers := g.config.GetAllTriggers()
	if len(triggers) > 0 {
		sb.WriteString("    paths:\n")
		for _, trigger := range triggers {
			fmt.Fprintf(sb, "      - '%s'\n", trigger)
		}
	}

	sb.WriteString("  workflow_dispatch:\n")
	sb.WriteString("    inputs:\n")

	// Only add environment input if there are environments
	// For no-environment setup, builds go directly to pre-release
	if len(g.config.Environments) > 0 {
		sb.WriteString("      environment:\n")
		sb.WriteString("        description: 'Target environment'\n")
		sb.WriteString("        type: choice\n")
		sb.WriteString("        options:\n")
		for _, env := range g.config.Environments {
			fmt.Fprintf(sb, "          - %s\n", env)
		}
		fmt.Fprintf(sb, "        default: '%s'\n", g.config.Environments[0])
	}
	sb.WriteString("      dry_run:\n")
	sb.WriteString("        description: 'Dry run mode'\n")
	sb.WriteString("        type: boolean\n")
	sb.WriteString("        default: false\n")

	// Emit operator-defined dispatch_inputs (sorted for determinism).
	if len(g.config.DispatchInputs) > 0 {
		names := make([]string, 0, len(g.config.DispatchInputs))
		for name := range g.config.DispatchInputs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			di := g.config.DispatchInputs[name]
			fmt.Fprintf(sb, "      %s:\n", name)
			if di.Description != "" {
				fmt.Fprintf(sb, "        description: %q\n", di.Description)
			}
			inputType := di.Type
			if inputType == "" {
				inputType = config.DispatchInputTypeString
			}
			fmt.Fprintf(sb, "        type: %s\n", inputType)
			if inputType == config.DispatchInputTypeChoice && len(di.Options) > 0 {
				sb.WriteString("        options:\n")
				for _, opt := range di.Options {
					fmt.Fprintf(sb, "          - %s\n", opt)
				}
			}
			if di.Default != nil {
				fmt.Fprintf(sb, "        default: '%v'\n", di.Default)
			}
			if di.Required {
				sb.WriteString("        required: true\n")
			}
		}
	}

	// Emit extra triggers when configured.
	if et := g.config.ExtraTriggers; et != nil {
		g.writeExtraTriggers(sb, et)
	}

	sb.WriteString("\n")
}

// writeExtraTriggers appends non-push trigger entries to the on: block.
func (g *Generator) writeExtraTriggers(sb *strings.Builder, et *config.ExtraTriggers) {
	if len(et.Schedule) > 0 {
		sb.WriteString("  schedule:\n")
		for _, s := range et.Schedule {
			fmt.Fprintf(sb, "    - cron: '%s'\n", s.Cron)
		}
	}

	if rd := et.RepositoryDispatch; rd != nil {
		sb.WriteString("  repository_dispatch:\n")
		if len(rd.Types) > 0 {
			sb.WriteString("    types:\n")
			for _, t := range rd.Types {
				fmt.Fprintf(sb, "      - %s\n", t)
			}
		}
	}

	if wr := et.WorkflowRun; wr != nil {
		sb.WriteString("  workflow_run:\n")
		if len(wr.Workflows) > 0 {
			sb.WriteString("    workflows:\n")
			for _, w := range wr.Workflows {
				fmt.Fprintf(sb, "      - '%s'\n", w)
			}
		}
		if len(wr.Types) > 0 {
			sb.WriteString("    types:\n")
			for _, t := range wr.Types {
				fmt.Fprintf(sb, "      - %s\n", t)
			}
		}
	}

	if et.MergeGroup != nil {
		sb.WriteString("  merge_group:\n")
	}
}

// writeConcurrency emits a top-level concurrency: block. Two rapid pushes to
// trunk used to fire concurrent orchestrate runs, which raced on state writes
// and produced duplicate RC tags + non-fast-forward push failures (#92).
// Default: cancel an older in-progress run when a newer push lands —
// the older run's work is obsolete. Override via config.concurrency.
func (g *Generator) writeConcurrency(sb *strings.Builder) {
	sb.WriteString("concurrency:\n")
	fmt.Fprintf(sb, "  group: %s\n", g.config.GetConcurrencyGroup())
	fmt.Fprintf(sb, "  cancel-in-progress: %t\n", g.config.GetConcurrencyCancelInProgress())
	sb.WriteString("\n")
}

func (g *Generator) writePermissions(sb *strings.Builder) {
	// Permissions needed for release management (tags, releases) and state commits
	sb.WriteString("permissions:\n")
	sb.WriteString("  contents: write\n")
	sb.WriteString("  actions: read\n")
	sb.WriteString("\n")
}

func (g *Generator) writeJobs(sb *strings.Builder) {
	sb.WriteString("jobs:\n")
	g.writeSetupJob(sb)

	// Write callback jobs in topological order
	sorted, _ := g.graph.TopologicalSort()
	for _, jobID := range sorted {
		info := g.graph.Nodes[jobID]
		switch info.Type {
		case config.CallbackTypeValidate:
			g.writeCallbackJob(sb, info, g.config.Validate.Workflow)
		case config.CallbackTypeBuild:
			for _, b := range g.config.Builds {
				if b.Name == info.Name {
					g.writeCallbackJob(sb, info, b.Workflow)
					break
				}
			}
		case config.CallbackTypeDeploy:
			for _, d := range g.config.Deploys {
				if d.Name == info.Name {
					g.writeCallbackJob(sb, info, d.Workflow)
					break
				}
			}
		}
	}

	g.writeFinalizeJob(sb, sorted)
}

func (g *Generator) writeSetupJob(sb *strings.Builder) {
	sb.WriteString("  setup:\n")
	sb.WriteString("    name: Setup\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	g.writeOwnedTimeout(sb, "    ")
	sb.WriteString("    outputs:\n")

	// All outputs come from the CLI setup command
	for _, b := range g.config.Builds {
		fmt.Fprintf(sb, "      run_build_%s: ${{ steps.setup.outputs.run_build_%s }}\n", b.Name, b.Name)
	}
	for _, d := range g.config.Deploys {
		fmt.Fprintf(sb, "      run_deploy_%s: ${{ steps.setup.outputs.run_deploy_%s }}\n", d.Name, d.Name)
	}
	sb.WriteString("      head_sha: ${{ steps.setup.outputs.head_sha }}\n")
	sb.WriteString("      version: ${{ steps.setup.outputs.version }}\n")
	sb.WriteString("      previous_tag: ${{ steps.setup.outputs.previous_tag }}\n")
	sb.WriteString("      changelog_base_sha: ${{ steps.setup.outputs.changelog_base_sha }}\n")

	// Per-deployable base SHAs for callbacks that need them
	for _, b := range g.config.Builds {
		fmt.Fprintf(sb, "      base_build_%s: ${{ steps.setup.outputs.base_build_%s }}\n", b.Name, b.Name)
	}
	for _, d := range g.config.Deploys {
		fmt.Fprintf(sb, "      base_deploy_%s: ${{ steps.setup.outputs.base_deploy_%s }}\n", d.Name, d.Name)
	}

	sb.WriteString("    steps:\n")
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")

	// Setup CLI
	sb.WriteString("      - name: Setup CLI\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())

	// Single CLI call does all setup work
	sb.WriteString("      - name: Run Setup\n")
	sb.WriteString("        id: setup\n")

	// For no-environment setup, don't pass environment at all
	if len(g.config.Environments) > 0 {
		sb.WriteString("        env:\n")
		fmt.Fprintf(sb, "          ENVIRONMENT: ${{ github.event.inputs.environment || '%s' }}\n", g.config.Environments[0])
		sb.WriteString("        run: |\n")
		sb.WriteString("          cascade orchestrate setup \\\n")
		sb.WriteString("            --environment \"$ENVIRONMENT\" \\\n")
	} else {
		sb.WriteString("        run: |\n")
		sb.WriteString("          cascade orchestrate setup \\\n")
	}
	fmt.Fprintf(sb, "            --config %s \\\n", g.getManifestFilePath())
	sb.WriteString("            --gha-output\n")

	sb.WriteString("\n")
}

func (g *Generator) writeCallbackJob(sb *strings.Builder, info CallbackInfo, workflow string) {
	// For reusable-workflow callbacks that declare passthrough artifact downloads,
	// emit a cascade-owned pre-job that fetches the artifacts before the callback
	// runs. The pre-job depends on the same upstream jobs as the callback itself so
	// it can start as soon as the producers finish.
	downloadPreJobID := ""
	if info.Run == "" && info.PassthroughArtifact != nil && len(info.PassthroughArtifact.Downloads) > 0 {
		downloadPreJobID = fmt.Sprintf("%s-download", info.JobID)
		g.writePassthroughDownloadJob(sb, info, downloadPreJobID)
	}

	fmt.Fprintf(sb, "  %s:\n", info.JobID)
	fmt.Fprintf(sb, "    name: %s\n", info.DisplayName)

	// needs: always includes setup, plus hard dependencies (already stored as job
	// IDs). optional_depends_on adds ordering-only edges: they go into needs: so
	// this job waits for them, but they are excluded from the if: skip-gate below
	// (#18) so a skipped optional dep does not skip this job.
	hardDeps := g.graph.GetDirectDependencies(info.JobID)
	needs := []string{"setup"}
	needs = append(needs, hardDeps...)
	needs = append(needs, g.graph.GetOptionalDependencies(info.JobID)...)
	// When a pre-download job was emitted, make the callback depend on it so the
	// downloaded artifacts are available in the runner's workspace.
	if downloadPreJobID != "" {
		needs = append(needs, downloadPreJobID)
	}
	fmt.Fprintf(sb, "    needs: [%s]\n", strings.Join(needs, ", "))

	// if: condition based on run_policy. Optional deps are intentionally not
	// passed here — they sequence the job without gating it.
	g.writeIfCondition(sb, info, needs)

	switch {
	case info.TimeoutMinutes > 0:
		// Explicit per-callback timeout always wins.
		fmt.Fprintf(sb, "    timeout-minutes: %d\n", info.TimeoutMinutes)
	case info.Run != "":
		// Inline run: callbacks are cascade-owned jobs, so they inherit the
		// cascade-owned-job timeout default (#37). Reusable-workflow callbacks
		// (jobs.<id>.uses) own their own timeout and get nothing here.
		g.writeOwnedTimeout(sb, "    ")
	}

	// strategy: emitted only for build callbacks that declare matrix:
	if info.Matrix != nil && len(info.Matrix.Dimensions) > 0 {
		g.writeStrategyBlock(sb, info.Matrix)
	}

	// environment: — emitted on deploy jobs when the config declares a
	// gha_environment for at least one environment. The job-level environment:
	// key wires the job to a GitHub Environment so that the environment's
	// protection rules (required reviewers, wait timers, deployment branch
	// policy, scoped secrets) apply at runtime. Actual protection configuration
	// lives in GitHub's Environment settings, not in the manifest.
	//
	// For orchestrate, the target environment is chosen at run time via the
	// workflow_dispatch input, so we emit an expression that resolves to the
	// cascade environment name. When gha_environment differs from the cascade
	// env name, users should align their GitHub Environment names accordingly.
	if info.Type == config.CallbackTypeDeploy && len(g.config.Environments) > 0 && anyEnvHasGHAConfig(g.config) {
		defaultEnv := g.config.Environments[0]
		fmt.Fprintf(sb, "    environment: ${{ github.event.inputs.environment || '%s' }}\n", defaultEnv)
	}

	// continue-on-error: a callback with on_failure: continue is one the operator
	// has explicitly marked as tolerable. Emitting continue-on-error keeps the
	// overall run green when only such callbacks fail, while the result is still
	// recorded (the failure check below never gates on continue callbacks).
	if info.OnFailure == config.OnFailureContinue {
		sb.WriteString("    continue-on-error: true\n")
	}

	// Inline run: callback — emit a cascade-owned job with an inline run: step
	// instead of a jobs.<id>.uses reusable-workflow call. Standard inputs reach
	// the step as env: variables rather than reusable-workflow with: inputs.
	if info.Run != "" {
		g.writeInlineRunBody(sb, info)
	} else {
		fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(workflow))

		// with: pass outputs from dependencies
		g.writeWithInputs(sb, info)

		sb.WriteString("    secrets: inherit\n\n")
	}

	// Generate retry jobs if retries > 0
	for i := 1; i <= info.Retries; i++ {
		g.writeRetryJob(sb, info, workflow, i)
	}

	// For reusable-workflow callbacks that declare a passthrough artifact upload,
	// emit a cascade-owned post-job that uploads the artifact after the callback
	// completes. Inline-run callbacks handle upload inline (see writeInlineRunBody).
	if info.Run == "" && info.PassthroughArtifact != nil && info.PassthroughArtifact.Upload != "" {
		g.writePassthroughUploadJob(sb, info)
	}
}

// writeInlineRunBody emits the runs-on / steps body of a cascade-owned inline
// run: callback job. The standard inputs that a reusable-workflow callback would
// receive via with: are surfaced to the inline step as env: variables (uppercased,
// e.g. ENVIRONMENT, SHA, and any dependency outputs the callback declares).
// When info.PassthroughArtifact is set, download steps are injected before the
// run step and an upload step is appended after it.
func (g *Generator) writeInlineRunBody(sb *strings.Builder, info CallbackInfo) {
	// Per-callback job attributes (inline-run jobs only): runner selection (#12),
	// permissions incl. id-token: write OIDC (#35/#15), and concurrency (#17). The
	// config-level runs_on default applies when the callback sets no runner.
	writeRunsOn(sb, "    ", info.RunsOn, g.config.RunsOn)
	writeJobPermissions(sb, "    ", info.Permissions)
	writeJobConcurrency(sb, "    ", info.Concurrency)
	sb.WriteString("    steps:\n")

	// Inject download-artifact steps before the run step so the artifacts are
	// present in the workspace when the inline command executes.
	g.writePassthroughDownloadSteps(sb, info)

	fmt.Fprintf(sb, "      - name: %s\n", info.DisplayName)

	envVars := g.inlineEnvInputs(info)
	if len(envVars) > 0 {
		sb.WriteString("        env:\n")
		for _, ev := range envVars {
			sb.WriteString(ev + "\n")
		}
	}

	shell := info.Shell
	if shell == "" {
		shell = "bash"
	}
	fmt.Fprintf(sb, "        shell: %s\n", shell)

	sb.WriteString("        run: |\n")
	for _, line := range strings.Split(strings.TrimRight(info.Run, "\n"), "\n") {
		fmt.Fprintf(sb, "          %s\n", line)
	}
	sb.WriteString("\n")

	// Inject upload-artifact step after the run step.
	g.writePassthroughUploadStep(sb, info)
}

// inlineEnvInputs returns the env: lines that surface the standard callback
// inputs (environment, sha, and declared dependency outputs) to an inline run:
// step. It mirrors writeWithInputs but renders to env: rather than with:.
func (g *Generator) inlineEnvInputs(info CallbackInfo) []string {
	deps := g.graph.GetDirectDependencies(info.JobID)

	var envVars []string
	// Track emitted env-var names so a dependency output named "sha" doesn't
	// emit a second SHA: alongside the standard one (GHA rejects duplicate keys).
	seen := map[string]bool{}
	emit := func(name, value string) {
		if seen[name] {
			return
		}
		seen[name] = true
		envVars = append(envVars, fmt.Sprintf("          %s: %s", name, value))
	}

	// Only pass environment if there are environments configured
	if len(g.config.Environments) > 0 {
		emit("ENVIRONMENT", fmt.Sprintf("${{ github.event.inputs.environment || '%s' }}", g.config.Environments[0]))
	}

	// Optional standard inputs - only passed if callback declares them
	if g.jobHasInput(info.JobID, "sha") {
		emit("SHA", "${{ needs.setup.outputs.head_sha }}")
	}

	// For build callbacks with a matrix, surface each dimension's current value.
	if info.Matrix != nil && len(info.Matrix.Dimensions) > 0 {
		keys := make([]string, 0, len(info.Matrix.Dimensions))
		for k := range info.Matrix.Dimensions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if g.jobHasInput(info.JobID, k) {
				emit(envVarName(k), fmt.Sprintf("${{ matrix.%s }}", k))
			}
		}
	}

	// Pass outputs from dependencies the callback declares as inputs.
	for _, depJobID := range deps {
		depInfo := g.graph.Nodes[depJobID]
		for _, out := range g.outputs[depInfo.JobID] {
			if g.jobHasInput(info.JobID, out) {
				emit(envVarName(out), fmt.Sprintf("${{ needs.%s.outputs.%s }}", depJobID, out))
			}
		}
	}

	return envVars
}

// envVarName converts an input key to a shell-safe env-var name: uppercased with
// hyphens translated to underscores (e.g. "image-tag" -> "IMAGE_TAG", reachable
// in the run step as $IMAGE_TAG).
func envVarName(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

// writeStrategyBlock emits the GHA strategy: block for a build matrix.
// max-parallel is omitted when 0 (GHA default). fail-fast is emitted only
// when explicitly set (non-nil pointer).
func (g *Generator) writeStrategyBlock(sb *strings.Builder, m *config.MatrixConfig) {
	sb.WriteString("    strategy:\n")
	sb.WriteString("      matrix:\n")

	// Emit dimensions in sorted order for deterministic output
	keys := make([]string, 0, len(m.Dimensions))
	for k := range m.Dimensions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := m.Dimensions[k]
		valsJSON := "["
		for i, v := range vals {
			if i > 0 {
				valsJSON += ", "
			}
			valsJSON += fmt.Sprintf("%q", v)
		}
		valsJSON += "]"
		fmt.Fprintf(sb, "        %s: %s\n", k, valsJSON)
	}

	if m.MaxParallel > 0 {
		fmt.Fprintf(sb, "      max-parallel: %d\n", m.MaxParallel)
	}
	if m.FailFast != nil {
		fmt.Fprintf(sb, "      fail-fast: %t\n", *m.FailFast)
	}
}

func (g *Generator) writeIfCondition(sb *strings.Builder, info CallbackInfo, needs []string) {
	var conditions []string
	buildLinkedDeploy := false

	// For deploys with depends_on build, check if build ran successfully
	// instead of using setup detection
	if info.Type == config.CallbackTypeDeploy {
		for _, d := range g.config.Deploys {
			if d.Name == info.Name && len(d.DependsOn) > 0 {
				// Build-linked deploy: condition is build success (not setup detection)
				buildLinkedDeploy = true
				for _, dep := range d.DependsOn {
					// Resolve the dependency to get the job ID
					depJobID, err := g.config.ResolveDependency(dep, config.CallbackTypeDeploy)
					if err != nil {
						continue
					}
					// Apply run_policy to build dependency check
					switch info.RunPolicy {
					case config.RunPolicyAlways:
						conditions = append(conditions, fmt.Sprintf("(needs.%s.result == 'success' || needs.%s.result == 'skipped')", depJobID, depJobID))
					case config.RunPolicyForce:
						// No condition needed for force
					default:
						conditions = append(conditions, fmt.Sprintf("needs.%s.result == 'success'", depJobID))
					}
				}
				break
			}
		}
	}

	// Standard detection for builds and trigger-based deploys
	// Note: validate always runs if defined (no change detection needed)
	// Use OutputKey to convert hyphens to underscores for GitHub Actions compatibility
	if !buildLinkedDeploy && info.Type != config.CallbackTypeValidate {
		outputKey := config.OutputKey(info.JobID)
		conditions = append(conditions, fmt.Sprintf("needs.setup.outputs.run_%s == 'true'", outputKey))
	}

	// Add dependency conditions based on run_policy (skip build deps for build-linked deploys as they're already added)
	for _, depJobID := range g.graph.GetDirectDependencies(info.JobID) {
		depInfo := g.graph.Nodes[depJobID]

		// Skip build dependencies for build-linked deploys (already handled above)
		if buildLinkedDeploy && depInfo.Type == config.CallbackTypeBuild {
			continue
		}

		switch info.RunPolicy {
		case config.RunPolicyDefault, "":
			conditions = append(conditions, fmt.Sprintf("needs.%s.result == 'success'", depJobID))
		case config.RunPolicyAlways:
			conditions = append(conditions, fmt.Sprintf("(needs.%s.result == 'success' || needs.%s.result == 'skipped')", depJobID, depJobID))
		case config.RunPolicyForce:
			// No dependency condition
		}
	}

	// If no conditions and not always/force policy, skip the if: block entirely
	// (e.g., validate job that should always run)
	if len(conditions) == 0 && info.RunPolicy != config.RunPolicyAlways && info.RunPolicy != config.RunPolicyForce {
		return
	}

	if info.RunPolicy == config.RunPolicyAlways || info.RunPolicy == config.RunPolicyForce {
		sb.WriteString("    if: |\n      always() &&\n")
	} else if len(conditions) > 0 {
		sb.WriteString("    if: |\n")
	}

	for i, cond := range conditions {
		if i < len(conditions)-1 {
			fmt.Fprintf(sb, "      %s &&\n", cond)
		} else {
			fmt.Fprintf(sb, "      %s\n", cond)
		}
	}
}

// inputKeys returns the sorted keys of a manifest inputs map. Used to seed the
// declared-input set for inline run: callbacks, which have no reusable-workflow
// file to parse inputs from.
func inputKeys(m map[string]interface{}) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jobHasInput checks if a job declares a specific input
func (g *Generator) jobHasInput(jobID, inputName string) bool {
	for _, input := range g.inputs[jobID] {
		if input == inputName {
			return true
		}
	}
	return false
}

func (g *Generator) writeWithInputs(sb *strings.Builder, info CallbackInfo) {
	deps := g.graph.GetDirectDependencies(info.JobID)

	var inputs []string

	// Only pass environment if there are environments configured
	if len(g.config.Environments) > 0 {
		inputs = append(inputs, fmt.Sprintf("      environment: ${{ github.event.inputs.environment || '%s' }}", g.config.Environments[0]))
	}

	// Optional standard inputs - only passed if callback declares them
	if g.jobHasInput(info.JobID, "sha") {
		inputs = append(inputs, "      sha: ${{ needs.setup.outputs.head_sha }}")
	}

	// When a callback opts in to dry-run emulation, pass the dry_run dispatch
	// input through so the callback can emulate internally instead of being skipped.
	if info.SupportsDryRun {
		inputs = append(inputs, "      dry_run: ${{ inputs.dry_run }}")
	}

	// For build callbacks with a matrix, pass each dimension's current value to
	// the reusable workflow via `with:` so the callback can act on it. Dimension
	// keys are passed through only when the callback workflow declares a matching
	// input; unknown inputs are silently skipped (GHA rejects undeclared inputs).
	if info.Matrix != nil && len(info.Matrix.Dimensions) > 0 {
		keys := make([]string, 0, len(info.Matrix.Dimensions))
		for k := range info.Matrix.Dimensions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if g.jobHasInput(info.JobID, k) {
				inputs = append(inputs, fmt.Sprintf("      %s: ${{ matrix.%s }}", k, k))
			}
		}
	}

	// Pass operator dispatch_inputs to callbacks that declare a matching input.
	if len(g.config.DispatchInputs) > 0 {
		names := make([]string, 0, len(g.config.DispatchInputs))
		for name := range g.config.DispatchInputs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if g.jobHasInput(info.JobID, name) {
				inputs = append(inputs, fmt.Sprintf("      %s: ${{ inputs.%s }}", name, name))
			}
		}
	}

	// Pass outputs from dependencies
	// Only pass if the callback declares the input
	for _, depJobID := range deps {
		depInfo := g.graph.Nodes[depJobID]

		for _, out := range g.outputs[depInfo.JobID] {
			if g.jobHasInput(info.JobID, out) {
				inputs = append(inputs, fmt.Sprintf("      %s: ${{ needs.%s.outputs.%s }}", out, depJobID, out))
			}
		}
	}

	// Operator-authored inputs from the manifest (inputs:/env_inputs:). These
	// carry per-callback config. Expression values survive verbatim:
	//   - passthrough expressions (${{ vars.X }}, ${{ secrets.Y }}, ...) emit
	//     as-is for GitHub Actions to evaluate at run time.
	//   - cascade-owned ${{ state.<env>.<field> }} refs resolve at generation
	//     time from the manifest state.
	//   - literals emit as-is.
	// Standard inputs (environment, sha, dependency outputs) already handled
	// above take precedence and are not duplicated here.
	inputs = append(inputs, g.operatorInputLines(info, seen(inputs))...)

	// Only write with: block if there are inputs
	if len(inputs) > 0 {
		sb.WriteString("    with:\n")
		for _, input := range inputs {
			sb.WriteString(input + "\n")
		}
	}
}

// seen extracts the set of input keys already present in the given "      key: value"
// lines, so operator inputs don't duplicate standard ones.
func seen(lines []string) map[string]struct{} {
	out := make(map[string]struct{}, len(lines))
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if i := strings.Index(trimmed, ":"); i > 0 {
			out[trimmed[:i]] = struct{}{}
		}
	}
	return out
}

// callbackInputs returns the manifest-declared default inputs for a callback,
// looked up by name and type.
func (g *Generator) callbackInputs(info CallbackInfo) map[string]interface{} {
	switch info.Type {
	case config.CallbackTypeDeploy:
		for i := range g.config.Deploys {
			if g.config.Deploys[i].Name == info.Name {
				return g.config.Deploys[i].Inputs
			}
		}
	case config.CallbackTypeBuild:
		for i := range g.config.Builds {
			if g.config.Builds[i].Name == info.Name {
				return g.config.Builds[i].Inputs
			}
		}
	case config.CallbackTypeValidate:
		if g.config.Validate != nil {
			return g.config.Validate.Inputs
		}
	}
	return nil
}

// operatorInputLines builds the "      key: value" with: lines for an
// orchestrate callback's manifest-declared inputs. Passthrough expressions
// survive verbatim; ${{ state.* }} references resolve at generation time;
// literals emit as-is. Keys already present in skip (standard inputs) are
// omitted. Output is sorted for deterministic generation.
func (g *Generator) operatorInputLines(info CallbackInfo, skip map[string]struct{}) []string {
	declared := g.callbackInputs(info)
	if len(declared) == 0 {
		return nil
	}
	keys := make([]string, 0, len(declared))
	for k := range declared {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var lines []string
	for _, k := range keys {
		if _, ok := skip[k]; ok {
			continue
		}
		// Only emit inputs the callback actually declares as workflow inputs.
		if !g.jobHasInput(info.JobID, k) {
			continue
		}
		s, ok := declared[k].(string)
		if !ok {
			lines = append(lines, fmt.Sprintf("      %s: %v", k, declared[k]))
			continue
		}
		val, err := resolveInputValue(s, g.state)
		if err != nil {
			// Unresolved state ref: emit verbatim so the failure is visible in
			// the generated workflow rather than silently dropped.
			val = s
		}
		lines = append(lines, fmt.Sprintf("      %s: %s", k, val))
	}
	return lines
}

func (g *Generator) writeRetryJob(sb *strings.Builder, info CallbackInfo, workflow string, retryNum int) {
	retryJobName := fmt.Sprintf("%s-retry-%d", info.JobID, retryNum)
	prevJobName := info.JobID
	if retryNum > 1 {
		prevJobName = fmt.Sprintf("%s-retry-%d", info.JobID, retryNum-1)
	}

	fmt.Fprintf(sb, "  %s:\n", retryJobName)
	fmt.Fprintf(sb, "    name: %s - Retry %d\n", info.DisplayName, retryNum)
	fmt.Fprintf(sb, "    needs: [setup, %s]\n", prevJobName)
	fmt.Fprintf(sb, "    if: needs.%s.result == 'failure'\n", prevJobName)
	switch {
	case info.TimeoutMinutes > 0:
		fmt.Fprintf(sb, "    timeout-minutes: %d\n", info.TimeoutMinutes)
	case info.Run != "":
		// Inline run: retry shims are cascade-owned (#37).
		g.writeOwnedTimeout(sb, "    ")
	}

	// Propagate the matrix strategy to the retry job so that ${{ matrix.* }}
	// references in the reusable workflow's inputs remain bound. Without this,
	// the retry job runs in a matrix-less context and GHA treats the expressions
	// as unresolved empty strings.
	if info.Matrix != nil && len(info.Matrix.Dimensions) > 0 {
		g.writeStrategyBlock(sb, info.Matrix)
	}

	if info.Run != "" {
		g.writeInlineRunBody(sb, info)
		return
	}
	fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(workflow))
	g.writeWithInputs(sb, info)
	sb.WriteString("    secrets: inherit\n\n")
}

func (g *Generator) writeFinalizeJob(sb *strings.Builder, sorted []string) {
	// Collect all job IDs including retries
	var allJobs []string
	allJobs = append(allJobs, "setup")
	for _, jobID := range sorted {
		info := g.graph.Nodes[jobID]
		allJobs = append(allJobs, jobID)
		for i := 1; i <= info.Retries; i++ {
			allJobs = append(allJobs, fmt.Sprintf("%s-retry-%d", jobID, i))
		}
	}

	sb.WriteString("  finalize:\n")
	sb.WriteString("    name: Finalize\n")
	fmt.Fprintf(sb, "    needs: [%s]\n", strings.Join(allJobs, ", "))
	// Run finalize whenever setup succeeded, regardless of how the callbacks
	// ended. always() makes finalize fire even when a callback failed OR was
	// cancelled, so the run that progressed partway before being superseded
	// still records the state it actually reached instead of leaving the
	// manifest stuck at a stale value. A cancelled setup, by contrast, means no
	// state was produced yet, so finalize correctly stays gated on setup success.
	sb.WriteString("    if: always() && needs.setup.result == 'success'\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	g.writeOwnedTimeout(sb, "    ")

	// Output all callback outputs (sorted for deterministic output)
	// g.outputs is keyed by job ID (e.g., "build-app")
	var outputLines []string
	for jobID, outputs := range g.outputs {
		info, ok := g.graph.Nodes[jobID]
		if !ok {
			continue
		}
		for _, out := range outputs {
			outputLines = append(outputLines, fmt.Sprintf("      %s_%s: ${{ needs.%s.outputs.%s }}\n", info.Name, out, jobID, out))
		}
	}
	// Only write outputs section if there are actual outputs
	if len(outputLines) > 0 {
		sb.WriteString("    outputs:\n")
		sort.Strings(outputLines)
		for _, line := range outputLines {
			sb.WriteString(line)
		}
	}

	sb.WriteString("    steps:\n")
	writeActionStep(sb, g.config, "      ", actionCheckout)
	// Need full git history for changelog generation
	if g.config.ChangelogEnabled() {
		sb.WriteString("        with:\n")
		sb.WriteString("          fetch-depth: 0\n")
	}

	// Generate summary step
	g.writeSummaryStep(sb, sorted)

	// Changelog and release steps (conditional)
	if g.config.ReleaseEnabled() {
		if g.config.ChangelogEnabled() {
			g.writeChangelogStep(sb)
		}
		// Download release artifacts before creating release
		if g.config.HasReleaseArtifacts() {
			g.writeArtifactDownloadStep(sb)
		}
		g.writeReleaseStep(sb)
		// Upload artifacts to release after it's created
		if g.config.HasReleaseArtifacts() {
			g.writeArtifactUploadStep(sb)
		}
	}

	// Generate manifest update step (always)
	g.writeManifestUpdateStep(sb, sorted)

	// Notify primary repo if this is a satellite
	if g.config.IsSatellite() {
		g.writeNotifyPrimaryStep(sb)
	}

	// Generate failure check step - only fail on callbacks with on_failure: abort
	g.writeFailureCheckStep(sb, sorted)
}

func (g *Generator) writeSummaryStep(sb *strings.Builder, sorted []string) {
	sb.WriteString("      - name: Generate Summary\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          echo \"## Orchestration Complete\" >> \"$GITHUB_STEP_SUMMARY\"\n")
	sb.WriteString("          echo \"\" >> \"$GITHUB_STEP_SUMMARY\"\n")
	sb.WriteString("          echo \"### Callback Results\" >> \"$GITHUB_STEP_SUMMARY\"\n")
	sb.WriteString("          echo \"| Callback | Result | On Failure |\" >> \"$GITHUB_STEP_SUMMARY\"\n")
	sb.WriteString("          echo \"|----------|--------|------------|\" >> \"$GITHUB_STEP_SUMMARY\"\n")

	for _, jobID := range sorted {
		info := g.graph.Nodes[jobID]
		onFailure := info.OnFailure
		if onFailure == "" {
			onFailure = config.OnFailureAbort
		}
		// Use DisplayName for the table and JobID for the needs reference
		fmt.Fprintf(sb, "          echo \"| %s | ${{ needs.%s.result }} | %s |\" >> \"$GITHUB_STEP_SUMMARY\"\n", info.DisplayName, info.JobID, onFailure)
	}

	// Add outputs section to summary (only if there are outputs with values)
	if len(g.outputs) > 0 {
		sb.WriteString("          echo \"\" >> \"$GITHUB_STEP_SUMMARY\"\n")
		sb.WriteString("          echo \"### Outputs\" >> \"$GITHUB_STEP_SUMMARY\"\n")
		sb.WriteString("          HAS_OUTPUTS=false\n")

		// Collect and sort outputs for deterministic order
		var outputEntries []struct {
			name  string
			jobID string
			out   string
		}
		// g.outputs is keyed by job ID
		for jobID, outputs := range g.outputs {
			info, ok := g.graph.Nodes[jobID]
			if !ok {
				continue
			}
			for _, out := range outputs {
				outputEntries = append(outputEntries, struct {
					name  string
					jobID string
					out   string
				}{info.Name, jobID, out})
			}
		}
		sort.Slice(outputEntries, func(i, j int) bool {
			return outputEntries[i].name+"_"+outputEntries[i].out < outputEntries[j].name+"_"+outputEntries[j].out
		})

		// Only show outputs that have values
		for _, entry := range outputEntries {
			fmt.Fprintf(sb, "          if [[ -n \"${{ needs.%s.outputs.%s }}\" ]]; then\n", entry.jobID, entry.out)
			sb.WriteString("            if [[ \"$HAS_OUTPUTS\" == \"false\" ]]; then\n")
			sb.WriteString("              echo \"| Output | Value |\" >> \"$GITHUB_STEP_SUMMARY\"\n")
			sb.WriteString("              echo \"|--------|-------|\" >> \"$GITHUB_STEP_SUMMARY\"\n")
			sb.WriteString("              HAS_OUTPUTS=true\n")
			sb.WriteString("            fi\n")
			fmt.Fprintf(sb, "            echo \"| %s_%s | ${{ needs.%s.outputs.%s }} |\" >> \"$GITHUB_STEP_SUMMARY\"\n",
				entry.name, entry.out, entry.jobID, entry.out)
			sb.WriteString("          fi\n")
		}
		sb.WriteString("          if [[ \"$HAS_OUTPUTS\" == \"false\" ]]; then\n")
		sb.WriteString("            echo \"_No outputs produced_\" >> \"$GITHUB_STEP_SUMMARY\"\n")
		sb.WriteString("          fi\n")
	}
}

func (g *Generator) writeManifestUpdateStep(sb *strings.Builder, sorted []string) {
	sb.WriteString("      - name: Update Manifest\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          HEAD_SHA: ${{ needs.setup.outputs.head_sha }}\n")
	sb.WriteString("          VERSION: ${{ needs.setup.outputs.version }}\n")

	// Only include environment if there are environments configured
	if len(g.config.Environments) > 0 {
		fmt.Fprintf(sb, "          ENVIRONMENT: ${{ github.event.inputs.environment || '%s' }}\n", g.config.Environments[0])
	}

	// Add env vars for each deploy result
	for _, d := range g.config.Deploys {
		envName := strings.ToUpper(strings.ReplaceAll(d.Name, "-", "_"))
		jobName := fmt.Sprintf("deploy-%s", d.Name)
		fmt.Fprintf(sb, "          %s_RESULT: ${{ needs.%s.result }}\n", envName, jobName)
	}

	// Add env vars for build artifact IDs. Only emitted when the build
	// workflow declares an `artifact_id` output — the generator discovers
	// this via discoverOutputsAndInputs. When present, finalize captures
	// the immutable identifier (e.g., a Docker image digest) so it can be
	// stored in state and later passed to the publish callback on release.
	for _, b := range g.config.Builds {
		jobID := config.JobID(config.CallbackTypeBuild, b.Name)
		for _, out := range g.outputs[jobID] {
			if out == "artifact_id" {
				envName := strings.ToUpper(strings.ReplaceAll(b.Name, "-", "_"))
				fmt.Fprintf(sb, "          BUILD_ARTIFACT_%s: ${{ needs.%s.outputs.artifact_id }}\n", envName, jobID)
				break
			}
		}
	}

	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          MANIFEST_FILE=\"%s\"\n", g.getManifestFilePath())
	fmt.Fprintf(sb, "          MANIFEST_KEY=\"%s\"\n", g.getManifestKey())
	sb.WriteString("          if [[ ! -f \"$MANIFEST_FILE\" ]]; then\n")
	sb.WriteString("            echo \"No $MANIFEST_FILE found - skipping state update\"\n")
	sb.WriteString("            exit 0\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          \n")
	writeGitConfigSteps(sb, g.config, "          ")

	// Capture branch name for the rebase loop. Push events checkout in detached
	// HEAD by default, so we resolve the branch from $GITHUB_REF.
	sb.WriteString("          BRANCH=\"${GITHUB_REF##refs/heads/}\"\n")
	sb.WriteString("          \n")

	// Define apply_state_edits() so the retry loop can re-apply yq edits after
	// each rebase. This avoids merge conflicts on concurrent runs: the slower
	// run discards its yq output, fast-forwards to the latest tip, then writes
	// fresh edits on top.
	sb.WriteString("          apply_state_edits() {\n")
	sb.WriteString("            TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)\n")

	if len(g.config.Environments) > 0 {
		sb.WriteString("            # Update environment-level state (committed, not deployed)\n")
		sb.WriteString("            yq eval -i \".$MANIFEST_KEY.state.$ENVIRONMENT.sha = \\\"$HEAD_SHA\\\"\" \"$MANIFEST_FILE\"\n")
		sb.WriteString("            yq eval -i \".$MANIFEST_KEY.state.$ENVIRONMENT.version = \\\"$VERSION\\\"\" \"$MANIFEST_FILE\"\n")
		sb.WriteString("            yq eval -i \".$MANIFEST_KEY.state.$ENVIRONMENT.committed_at = \\\"$TIMESTAMP\\\"\" \"$MANIFEST_FILE\"\n")
		sb.WriteString("            yq eval -i \".$MANIFEST_KEY.state.$ENVIRONMENT.committed_by = \\\"${{ github.actor }}\\\"\" \"$MANIFEST_FILE\"\n")

		for _, d := range g.config.Deploys {
			envName := strings.ToUpper(strings.ReplaceAll(d.Name, "-", "_"))
			fmt.Fprintf(sb, "            if [[ \"$%s_RESULT\" == \"success\" ]]; then\n", envName)
			fmt.Fprintf(sb, "              yq eval -i \".$MANIFEST_KEY.state.$ENVIRONMENT.deploys.%s.sha = \\\"$HEAD_SHA\\\"\" \"$MANIFEST_FILE\"\n", d.Name)
			fmt.Fprintf(sb, "              yq eval -i \".$MANIFEST_KEY.state.$ENVIRONMENT.deploys.%s.deployed_at = \\\"$TIMESTAMP\\\"\" \"$MANIFEST_FILE\"\n", d.Name)
			fmt.Fprintf(sb, "              yq eval -i \".$MANIFEST_KEY.state.$ENVIRONMENT.deploys.%s.deployed_by = \\\"${{ github.actor }}\\\"\" \"$MANIFEST_FILE\"\n", d.Name)
			sb.WriteString("            fi\n")
		}

		for _, b := range g.config.Builds {
			jobID := config.JobID(config.CallbackTypeBuild, b.Name)
			for _, out := range g.outputs[jobID] {
				if out == "artifact_id" {
					envName := strings.ToUpper(strings.ReplaceAll(b.Name, "-", "_"))
					fmt.Fprintf(sb, "            if [[ -n \"$BUILD_ARTIFACT_%s\" ]]; then\n", envName)
					fmt.Fprintf(sb, "              yq eval -i \".$MANIFEST_KEY.state.$ENVIRONMENT.builds.%s.sha = \\\"$HEAD_SHA\\\"\" \"$MANIFEST_FILE\"\n", b.Name)
					fmt.Fprintf(sb, "              yq eval -i \".$MANIFEST_KEY.state.$ENVIRONMENT.builds.%s.artifact_id = \\\"$BUILD_ARTIFACT_%s\\\"\" \"$MANIFEST_FILE\"\n", b.Name, envName)
					sb.WriteString("            fi\n")
					break
				}
			}
		}
	} else {
		// No environments - update state under 'prerelease' key (consistent with orchestrator.DefaultStateKey)
		sb.WriteString("            # Update state under prerelease key (no environments)\n")
		sb.WriteString("            yq eval -i \".$MANIFEST_KEY.state.prerelease.sha = \\\"$HEAD_SHA\\\"\" \"$MANIFEST_FILE\"\n")
		sb.WriteString("            yq eval -i \".$MANIFEST_KEY.state.prerelease.version = \\\"$VERSION\\\"\" \"$MANIFEST_FILE\"\n")
		sb.WriteString("            yq eval -i \".$MANIFEST_KEY.state.prerelease.committed_at = \\\"$TIMESTAMP\\\"\" \"$MANIFEST_FILE\"\n")
		sb.WriteString("            yq eval -i \".$MANIFEST_KEY.state.prerelease.committed_by = \\\"${{ github.actor }}\\\"\" \"$MANIFEST_FILE\"\n")
	}

	sb.WriteString("          }\n")
	sb.WriteString("          \n")

	// Retry loop: fetch + reset + reapply + commit + push, up to 5 attempts.
	// Concurrent orchestrate runs racing to push state used to fail with
	// non-fast-forward; this loop keeps the slower run trying with a fresh
	// commit on the latest tip.
	sb.WriteString("          for attempt in 1 2 3 4 5; do\n")
	sb.WriteString("            git fetch origin \"$BRANCH\"\n")
	sb.WriteString("            git reset --hard \"origin/$BRANCH\"\n")
	sb.WriteString("            apply_state_edits\n")
	sb.WriteString("            if git diff --quiet \"$MANIFEST_FILE\"; then\n")
	sb.WriteString("              echo \"No state changes\"\n")
	sb.WriteString("              exit 0\n")
	sb.WriteString("            fi\n")
	sb.WriteString("            git add \"$MANIFEST_FILE\"\n")
	if len(g.config.Environments) > 0 {
		sb.WriteString("            git commit -m \"chore: update state for $ENVIRONMENT [skip ci]\"\n")
	} else {
		sb.WriteString("            git commit -m \"chore: update state [skip ci]\"\n")
	}
	sb.WriteString("            if git push origin \"HEAD:$BRANCH\"; then\n")
	sb.WriteString("              echo \"Pushed state on attempt $attempt\"\n")
	sb.WriteString("              exit 0\n")
	sb.WriteString("            fi\n")
	sb.WriteString("            echo \"Push attempt $attempt rejected (likely concurrent run); retrying...\" >&2\n")
	sb.WriteString("            sleep $((RANDOM % 5 + 2))\n")
	sb.WriteString("          done\n")
	sb.WriteString("          echo \"::error::Failed to push state after 5 attempts\" >&2\n")
	sb.WriteString("          exit 1\n")
}

func (g *Generator) writeNotifyPrimaryStep(sb *strings.Builder) {
	if g.config.Notify == nil {
		return
	}

	// Parse primary repo owner and name
	repoParts := strings.SplitN(g.config.Notify.Repo, "/", 2)
	if len(repoParts) != 2 {
		return
	}
	repoOwner := repoParts[0]
	repoName := repoParts[1]

	sb.WriteString("      - name: Notify Primary Repo\n")
	// Only notify if we're deploying to the first environment (dev)
	if len(g.config.Environments) > 0 {
		fmt.Fprintf(sb, "        if: github.event.inputs.environment == '%s' || github.event.inputs.environment == ''\n", g.config.Environments[0])
	}
	writeActionUses(sb, g.config, "        ", actionGithubScript)
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          github-token: %s\n", g.config.Notify.GetToken())
	sb.WriteString("          script: |\n")
	sb.WriteString("            // Collect deploy outputs for artifacts\n")
	sb.WriteString("            const artifacts = {};\n")

	// Collect outputs from deploys
	for _, d := range g.config.Deploys {
		jobID := config.JobID(config.CallbackTypeDeploy, d.Name)
		for _, out := range g.outputs[jobID] {
			fmt.Fprintf(sb, "            if (context.jobs.['%s']?.outputs?.%s) {\n", jobID, out)
			fmt.Fprintf(sb, "              artifacts['%s'] = context.jobs['%s'].outputs.%s;\n", out, jobID, out)
			sb.WriteString("            }\n")
		}
	}

	sb.WriteString("            \n")
	sb.WriteString("            // Dispatch to primary repo's external-update workflow\n")
	sb.WriteString("            await github.rest.actions.createWorkflowDispatch({\n")
	fmt.Fprintf(sb, "              owner: '%s',\n", repoOwner)
	fmt.Fprintf(sb, "              repo: '%s',\n", repoName)
	fmt.Fprintf(sb, "              workflow_id: '%s',\n", g.config.Notify.GetWorkflow())
	fmt.Fprintf(sb, "              ref: '%s',\n", g.config.TrunkBranch)
	sb.WriteString("              inputs: {\n")
	sb.WriteString("                source_repo: context.repo.owner + '/' + context.repo.repo,\n")

	// Use first deploy name as the deploy_name (satellites typically have one deploy)
	if len(g.config.Deploys) > 0 {
		fmt.Fprintf(sb, "                deploy_name: '%s',\n", g.config.Deploys[0].Name)
	}

	if len(g.config.Environments) > 0 {
		fmt.Fprintf(sb, "                environment: context.payload.inputs?.environment || '%s',\n", g.config.Environments[0])
	} else {
		sb.WriteString("                environment: 'dev',\n")
	}
	sb.WriteString("                sha: context.sha,\n")
	sb.WriteString("                version: '${{ needs.setup.outputs.version }}',\n")
	sb.WriteString("                artifacts: JSON.stringify(artifacts)\n")
	sb.WriteString("              }\n")
	sb.WriteString("            });\n")
	sb.WriteString("            console.log('Notified primary repo: " + g.config.Notify.Repo + "');\n")
}

func (g *Generator) writeFailureCheckStep(sb *strings.Builder, sorted []string) {
	// Collect callbacks with on_failure: abort (default behavior)
	var abortCallbacks []string
	for _, jobID := range sorted {
		info := g.graph.Nodes[jobID]
		onFailure := info.OnFailure
		if onFailure == "" {
			onFailure = config.OnFailureAbort
		}
		if onFailure == config.OnFailureAbort {
			abortCallbacks = append(abortCallbacks, info.JobID)
		}
	}

	if len(abortCallbacks) == 0 {
		// All callbacks have on_failure: continue, don't add failure check
		return
	}

	sb.WriteString("      - name: Check for Failures\n")

	// Build condition that only checks abort callbacks. A cancelled predecessor
	// (e.g. a run superseded by a newer push under cancel-in-progress) is treated
	// the same as a failure so a mid-flight cancellation is not silently tolerated.
	var conditions []string
	for _, jobName := range abortCallbacks {
		conditions = append(conditions, failureOrCancelledCond(jobName))
	}

	fmt.Fprintf(sb, "        if: %s\n", strings.Join(conditions, " || "))
	sb.WriteString("        run: |\n")
	sb.WriteString("          echo \"One or more critical callbacks failed or were cancelled\"\n")
	sb.WriteString("          exit 1\n")
}

// failureOrCancelledCond builds a GitHub Actions condition that matches when the
// named job ended in either the 'failure' or 'cancelled' state. Cancellation is
// an actionable, non-success outcome: under cancel-in-progress, a deploy
// superseded mid-flight reports 'cancelled' rather than 'failure', and treating
// it as a non-event would leave recorded state out of sync with reality.
func failureOrCancelledCond(jobName string) string {
	return fmt.Sprintf("contains(fromJSON('[\"failure\", \"cancelled\"]'), needs.%s.result)", jobName)
}

func (g *Generator) writeChangelogStep(sb *strings.Builder) {
	if g.config.HasCustomChangelog() {
		// Call custom changelog workflow
		sb.WriteString("      - name: Generate Changelog (Custom)\n")
		sb.WriteString("        id: changelog\n")
		fmt.Fprintf(sb, "        uses: %s\n", g.config.Changelog.Workflow)
		sb.WriteString("        with:\n")
		sb.WriteString("          base_sha: ${{ needs.setup.outputs.base_sha }}\n")
		sb.WriteString("          head_sha: ${{ needs.setup.outputs.head_sha }}\n")
		sb.WriteString("          repo: ${{ github.repository }}\n")
	} else {
		// Use built-in changelog generation
		sb.WriteString("      - name: Setup CLI\n")
		fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
		sb.WriteString("        with:\n")
		fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())
		fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())
		sb.WriteString("      - name: Generate Changelog\n")
		sb.WriteString("        id: changelog\n")
		sb.WriteString("        env:\n")
		fmt.Fprintf(sb, "          GH_TOKEN: %s\n", g.getReleaseTokenRef())
		sb.WriteString("        run: |\n")
		sb.WriteString("          # Use changelog_base_sha which compares this env to next env\n")
		sb.WriteString("          # This shows commits in this env NOT yet promoted to next env\n")
		sb.WriteString("          RESULT=$(cascade generate-changelog \\\n")
		sb.WriteString("            --base-sha \"${{ needs.setup.outputs.changelog_base_sha }}\" \\\n")
		sb.WriteString("            --head-sha \"${{ needs.setup.outputs.head_sha }}\" \\\n")
		// Add contributors flag if enabled in config
		if g.config.Changelog != nil && g.config.Changelog.Contributors {
			sb.WriteString("            --contributors \\\n")
		}
		sb.WriteString("            --repo \"${{ github.repository }}\")\n")
		sb.WriteString("          echo \"changelog<<EOF\" >> \"$GITHUB_OUTPUT\"\n")
		sb.WriteString("          echo \"$RESULT\" | jq -r '.changelog' >> \"$GITHUB_OUTPUT\"\n")
		sb.WriteString("          echo \"EOF\" >> \"$GITHUB_OUTPUT\"\n")
	}
}

func (g *Generator) writeReleaseStep(sb *strings.Builder) {
	sb.WriteString("      - name: Manage Release\n")
	fmt.Fprintf(sb, "        uses: %s\n", g.getActionPath())
	sb.WriteString("        with:\n")
	sb.WriteString("          repo: ${{ github.repository }}\n")

	if g.config.HasExternalRelease() {
		// External release - update existing
		sb.WriteString("          action: update\n")
		// Parse callback.output reference to get the job output
		parts := strings.SplitN(g.config.Release.Tag, ".", 2)
		callbackName := parts[0]
		outputName := parts[1]
		// Find the job ID for this callback name
		var jobID string
		for jid, info := range g.graph.Nodes {
			if info.Name == callbackName {
				jobID = jid
				break
			}
		}
		fmt.Fprintf(sb, "          tag: ${{ needs.%s.outputs.%s }}\n", jobID, outputName)
	} else {
		// Framework-managed release - create with version tag
		sb.WriteString("          action: update\n")
		sb.WriteString("          tag: ${{ needs.setup.outputs.version }}\n")
		sb.WriteString("          create_tag: 'true'\n")
	}

	// Only include environment if there are environments configured
	if len(g.config.Environments) > 0 {
		fmt.Fprintf(sb, "          environment: ${{ github.event.inputs.environment || '%s' }}\n", g.config.Environments[0])
	} else {
		// For no-environment repos, use a placeholder for release tracking
		sb.WriteString("          environment: prerelease\n")
	}
	sb.WriteString("          sha: ${{ needs.setup.outputs.head_sha }}\n")
	if g.config.ChangelogEnabled() {
		sb.WriteString("          changelog: ${{ steps.changelog.outputs.changelog }}\n")
	}
	sb.WriteString("          previous_tag: ${{ needs.setup.outputs.previous_tag }}\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())
}

func (g *Generator) writeArtifactDownloadStep(sb *strings.Builder) {
	sb.WriteString("      - name: Download Release Artifacts\n")
	writeActionUses(sb, g.config, "        ", actionDownloadArtifact)
	sb.WriteString("        with:\n")
	sb.WriteString("          pattern: release-*\n")
	sb.WriteString("          path: release-artifacts\n")
	sb.WriteString("          merge-multiple: true\n")
}

func (g *Generator) writeArtifactUploadStep(sb *strings.Builder) {
	sb.WriteString("      - name: Upload Release Artifacts\n")
	sb.WriteString("        env:\n")
	fmt.Fprintf(sb, "          GH_TOKEN: %s\n", g.getReleaseTokenRef())
	sb.WriteString("        run: |\n")
	sb.WriteString("          TAG=\"${{ needs.setup.outputs.version }}\"\n")
	sb.WriteString("          \n")

	// Generate artifact upload commands for each configured artifact
	for _, b := range g.config.Builds {
		for _, a := range b.Artifacts {
			artifactName := fmt.Sprintf("release-%s-%s", b.Name, a.Name)
			required := true
			if !a.Required {
				required = false
			}

			fmt.Fprintf(sb, "          # Upload %s artifacts\n", artifactName)
			fmt.Fprintf(sb, "          if ls release-artifacts/%s 2>/dev/null; then\n", a.Path)
			fmt.Fprintf(sb, "            echo \"Uploading %s artifacts...\"\n", artifactName)
			fmt.Fprintf(sb, "            gh release upload \"$TAG\" release-artifacts/%s --clobber\n", a.Path)
			if required {
				sb.WriteString("          else\n")
				fmt.Fprintf(sb, "            echo \"::error::Required artifact '%s' not found\"\n", artifactName)
				sb.WriteString("            exit 1\n")
			} else {
				sb.WriteString("          else\n")
				fmt.Fprintf(sb, "            echo \"::warning::Optional artifact '%s' not found\"\n", artifactName)
			}
			sb.WriteString("          fi\n")
			sb.WriteString("          \n")
		}
	}
}

// passthroughArtifactName returns the canonical GHA artifact name for a build
// job's passthrough upload: "build-{name}". Consumers reference the same name.
func passthroughArtifactName(buildName string) string {
	return fmt.Sprintf("build-%s", buildName)
}

// writePassthroughDownloadSteps emits one download-artifact step per entry in
// info.PassthroughArtifact.Downloads. Each step downloads the artifact produced
// by the named upstream build job and places it in a directory named after the
// artifact so multiple downloads do not collide.
func (g *Generator) writePassthroughDownloadSteps(sb *strings.Builder, info CallbackInfo) {
	if info.PassthroughArtifact == nil || len(info.PassthroughArtifact.Downloads) == 0 {
		return
	}
	for _, src := range info.PassthroughArtifact.Downloads {
		name := passthroughArtifactName(src)
		fmt.Fprintf(sb, "      - name: Download artifact from %s\n", src)
		writeActionUses(sb, g.config, "        ", actionDownloadArtifact)
		sb.WriteString("        with:\n")
		fmt.Fprintf(sb, "          name: %s\n", name)
		fmt.Fprintf(sb, "          path: %s\n", name)
		sb.WriteString("\n")
	}
}

// writePassthroughUploadStep emits an upload-artifact step for
// info.PassthroughArtifact.Upload when set. The artifact is named
// "build-{job-name}" so downstream jobs can reference it by name.
// Used only for inline-run callbacks where the step is injected inside the
// cascade-owned job's steps list.
func (g *Generator) writePassthroughUploadStep(sb *strings.Builder, info CallbackInfo) {
	if info.PassthroughArtifact == nil || info.PassthroughArtifact.Upload == "" {
		return
	}
	name := passthroughArtifactName(info.Name)
	fmt.Fprintf(sb, "      - name: Upload artifact %s\n", name)
	writeActionUses(sb, g.config, "        ", actionUploadArtifact)
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          name: %s\n", name)
	fmt.Fprintf(sb, "          path: %s\n", info.PassthroughArtifact.Upload)
	sb.WriteString("\n")
}

// writePassthroughDownloadJob emits a cascade-owned job (jobID) that runs
// actions/download-artifact for each entry in info.PassthroughArtifact.Downloads.
// Used for reusable-workflow callbacks where steps cannot be injected into the
// jobs.<id>.uses block. The download job has the same needs/if as the callback
// so it runs under the same conditions.
func (g *Generator) writePassthroughDownloadJob(sb *strings.Builder, info CallbackInfo, jobID string) {
	fmt.Fprintf(sb, "  %s:\n", jobID)
	fmt.Fprintf(sb, "    name: Download artifacts for %s\n", info.DisplayName)

	needs := []string{"setup"}
	needs = append(needs, g.graph.GetDirectDependencies(info.JobID)...)
	fmt.Fprintf(sb, "    needs: [%s]\n", strings.Join(needs, ", "))

	// Mirror the callback's if: condition so this job is skipped when the
	// callback would be skipped (same run_policy).
	g.writeIfCondition(sb, info, needs)

	sb.WriteString("    runs-on: ubuntu-latest\n")
	g.writeOwnedTimeout(sb, "    ")
	sb.WriteString("    steps:\n")
	for _, src := range info.PassthroughArtifact.Downloads {
		name := passthroughArtifactName(src)
		fmt.Fprintf(sb, "      - name: Download artifact from %s\n", src)
		writeActionUses(sb, g.config, "        ", actionDownloadArtifact)
		sb.WriteString("        with:\n")
		fmt.Fprintf(sb, "          name: %s\n", name)
		fmt.Fprintf(sb, "          path: %s\n", name)
		sb.WriteString("\n")
	}
}

// writePassthroughUploadJob emits a cascade-owned post-job that runs
// actions/upload-artifact after info's reusable-workflow callback completes.
// The artifact is named "build-{job-name}".
// Used for reusable-workflow callbacks where steps cannot be injected into the
// jobs.<id>.uses block.
func (g *Generator) writePassthroughUploadJob(sb *strings.Builder, info CallbackInfo) {
	postJobID := fmt.Sprintf("%s-upload", info.JobID)
	name := passthroughArtifactName(info.Name)
	fmt.Fprintf(sb, "  %s:\n", postJobID)
	fmt.Fprintf(sb, "    name: Upload artifact %s\n", name)
	fmt.Fprintf(sb, "    needs: [%s]\n", info.JobID)
	fmt.Fprintf(sb, "    if: needs.%s.result == 'success'\n", info.JobID)
	sb.WriteString("    runs-on: ubuntu-latest\n")
	g.writeOwnedTimeout(sb, "    ")
	sb.WriteString("    steps:\n")
	fmt.Fprintf(sb, "      - name: Upload artifact %s\n", name)
	writeActionUses(sb, g.config, "        ", actionUploadArtifact)
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          name: %s\n", name)
	fmt.Fprintf(sb, "          path: %s\n", info.PassthroughArtifact.Upload)
	sb.WriteString("\n")
}
