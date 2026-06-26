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
// (setup, finalize, retry shims, and passthrough artifact helper jobs) when
// config.job_timeout_minutes is not set. GitHub
// Actions defaults jobs to 360 minutes (6 hours); cascade's orchestration jobs
// are meant to be fast, so a hung git push, CLI download, or API call should not
// hold a runner for six hours. Override per manifest via config.job_timeout_minutes.
const DefaultJobTimeoutMinutes = 30

// SetupJobName and FinalizeJobName are the GitHub Actions check-run names of the
// orchestrate workflow's two cascade-controlled steps jobs. GitHub records a
// job's check-run context under its name:, so these constants are the exact
// contexts a branch-protection rule can require with certainty. Both jobs are
// unconditional (the setup job has no if:, and finalize uses always()), so they
// always report on every run, which is what makes them safe to require. The
// branch-protection emitter references these same constants, so a rename here
// updates both the generated workflow and the emitted protection contexts in
// lockstep and never lets them drift apart.
const (
	SetupJobName    = "Setup"
	FinalizeJobName = "Finalize"
)

// normalizeWorkflowPath returns a GitHub-valid workflow path for a local callback.
// Cross-repo external refs (containing "@") are returned unchanged.
// Paths already under ./.github/workflows/ are returned unchanged.
// Paths starting with .github/workflows/ get the ./ prefix.
// Bare filenames (no "/") and any other local path are routed to
// ./.github/workflows/<basename>, which is where GitHub requires local reusable
// workflows to live.
func normalizeWorkflowPath(path string) string {
	// Cross-repo external refs contain "@" - leave them as-is.
	if strings.Contains(path, "@") {
		return path
	}
	// Already fully normalized.
	if strings.HasPrefix(path, "./.github/workflows/") {
		return path
	}
	// .github/workflows/x.yaml -> ./.github/workflows/x.yaml
	if strings.HasPrefix(path, ".github/workflows/") {
		return "./" + path
	}
	// .github/<other>/ - legacy edge case, prepend ./ (prior behavior).
	if strings.HasPrefix(path, ".github/") {
		return "./" + path
	}
	// Bare filename or any other local path: route to canonical location.
	return "./.github/workflows/" + filepath.Base(path)
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

// getStateTokenRef returns the token expression used to write manifest state to
// the trunk branch. Users configure the full expression via the state_token
// config option; it defaults to "${{ secrets.GITHUB_TOKEN }}".
func (g *Generator) getStateTokenRef() string {
	return resolveStateTokenRef(g.config)
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

// anyAutoCommits reports whether any build or deploy callback (including
// external deploys) has auto_commits: true. When true, the finalize step must
// re-resolve HEAD after the callbacks complete, because at least one of them
// may have pushed additional commits (e.g. a formatter/codegen step), meaning
// the triggering SHA no longer matches what was actually built or deployed.
func (g *Generator) anyAutoCommits() bool {
	for _, b := range g.config.Builds {
		if b.AutoCommits {
			return true
		}
	}
	for _, d := range g.config.Deploys {
		if d.AutoCommits {
			return true
		}
	}
	for _, ext := range g.config.External {
		for _, d := range ext.Deploys {
			if d.AutoCommits {
				return true
			}
		}
	}
	return false
}

// getCLIRef returns the Git ref to use for the cascade self-action. The default
// (cli_version unset or "latest") resolves to config.DefaultCLIVersion, an
// immutable release tag, so consumers never run an unpinned mutable ref.
// Supported values:
//   - unset / "latest" → config.DefaultCLIVersion (immutable, pinned default)
//   - "beta" → "master" branch (explicit opt-in, bleeding edge, may be unstable)
//   - "vX.Y.Z" → that specific version tag
func (g *Generator) getCLIRef() string {
	if g.config.CLIVersion == "beta" {
		return "master" // Explicit opt-in escape hatch to trunk.
	}
	return g.config.GetCLIVersion()
}

// getReleaseTokenRef returns the token expression for release operations.
// Users configure the full expression via release_token config option.
func (g *Generator) getReleaseTokenRef() string {
	return resolveReleaseTokenRef(g.config)
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

	// Check that dependents have inputs for dependency outputs. Iterate in
	// declaration order so emitted warnings are stable across runs.
	for _, jobID := range g.graph.Order {
		node := g.graph.Nodes[jobID]
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

	// Warn when gha_environment is configured but the deploys are external
	// reusable workflows. GitHub Actions forbids a job-level environment: key on
	// a reusable-workflow caller job (jobs.<id>.uses), so cascade cannot wire the
	// GitHub Environment protection rules onto the caller. cascade still passes
	// the environment name via the with: environment input; the protection rules
	// must be declared on the internal job inside the reusable workflow itself.
	g.warnings = append(g.warnings, g.externalDeployEnvironmentWarnings()...)

	return g.warnings
}

// externalDeployEnvironmentWarnings returns a warning when gha_environment is
// configured for any environment while one or more deploys are external
// reusable workflows. GitHub Actions forbids a job-level environment: key on a
// reusable-workflow caller job, so cascade cannot apply GitHub Environment
// protection to those deploys from the caller side. The environment name is
// still passed via the with: environment input; the protection must be declared
// inside the reusable workflow's own job.
func (g *Generator) externalDeployEnvironmentWarnings() []string {
	if !anyEnvHasGHAConfig(g.config) {
		return nil
	}

	externalDeploys := make([]string, 0, len(g.config.Deploys))
	for _, d := range g.config.Deploys {
		externalDeploys = append(externalDeploys, d.Name)
	}
	if len(externalDeploys) == 0 {
		return nil
	}

	return []string{fmt.Sprintf(
		"Note: gha_environment is configured but deploy(s) %s use an external "+
			"reusable workflow. GitHub Actions disallows a job-level environment: key "+
			"on a reusable-workflow caller job, so cascade cannot apply GitHub "+
			"Environment protection from the caller. cascade passes the environment "+
			"name as the reusable workflow's 'environment' input; declare "+
			"environment: on the job inside your reusable workflow to enforce "+
			"protection rules.",
		strings.Join(externalDeploys, ", "))}
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

	// Iterate in declaration order so the validation error list is stable.
	for _, jobID := range g.graph.Order {
		node := g.graph.Nodes[jobID]
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
		cbType   string
		workflow string
		inputs   map[string]interface{}
	}{}

	if g.config.Validate != nil {
		allCallbacks = append(allCallbacks, struct {
			jobID    string
			name     string
			cbType   string
			workflow string
			inputs   map[string]interface{}
		}{"validate", "validate", config.CallbackTypeValidate, g.config.Validate.Workflow, g.config.Validate.Inputs})
	}
	for _, b := range g.config.Builds {
		jobID := config.JobID(config.CallbackTypeBuild, b.Name)
		allCallbacks = append(allCallbacks, struct {
			jobID    string
			name     string
			cbType   string
			workflow string
			inputs   map[string]interface{}
		}{jobID, b.Name, config.CallbackTypeBuild, b.Workflow, b.Inputs})
	}
	for _, d := range g.config.Deploys {
		jobID := config.JobID(config.CallbackTypeDeploy, d.Name)
		allCallbacks = append(allCallbacks, struct {
			jobID    string
			name     string
			cbType   string
			workflow string
			inputs   map[string]interface{}
		}{jobID, d.Name, config.CallbackTypeDeploy, d.Workflow, d.Inputs})
	}

	for _, cb := range allCallbacks {
		// Cross-repo callbacks reference a reusable workflow in another
		// repository (org/repo/.github/workflows/file.yaml@ref). That file does
		// not exist on local disk, so we cannot parse it to discover its inputs
		// and outputs. Seed the callback's contract surface instead of doing a
		// local read that would always fail on the literal "@ref" path.
		if config.IsExternalWorkflow(cb.workflow) {
			g.inputs[cb.jobID] = crossRepoInputs(cb.cbType, cb.inputs)
			g.outputs[cb.jobID] = crossRepoOutputs(cb.cbType)
			// The framework always provides environment/sha/dry_run, so a
			// cross-repo callback has no required input cascade cannot satisfy
			// from its own knowledge. Leave required empty rather than guessing.
			g.requiredInputs[cb.jobID] = nil
			continue
		}

		// Read the stub from the normalized location so a bare filename
		// (build.yaml) resolves to .github/workflows/build.yaml, which is where
		// GitHub requires local reusable workflows to live and where the emitted
		// uses: reference points.
		path := filepath.Join(g.baseDir, normalizeWorkflowPath(cb.workflow))
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

// crossRepoInputs returns the set of input names a cross-repo callback is
// assumed to declare. Because the external workflow is in another repository and
// cannot be parsed locally, this falls back to the callback contract: every
// validate/build/deploy callback declares the standard environment, sha, and
// dry_run inputs, plus any inputs the operator wired explicitly in the manifest
// (inputs:/env_inputs:). Returning these makes writeWithInputs emit the standard
// with: wiring (environment, sha, dry_run when supported) for the caller job, so
// the live cross-repo call receives the contract inputs the external workflow
// expects. Names are de-duplicated; order is not significant to callers.
func crossRepoInputs(callbackType string, operatorInputs map[string]interface{}) []string {
	// Standard callback-contract inputs (see docs callback-contract.md).
	names := []string{"environment", "sha", "dry_run"}
	seen := map[string]struct{}{"environment": {}, "sha": {}, "dry_run": {}}

	// Operator-declared manifest inputs are passed through by writeWithInputs
	// only when the callback declares them, so surface them here too.
	for name := range operatorInputs {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	_ = callbackType // reserved for future per-type input differences
	return names
}

// crossRepoOutputs returns the output names a cross-repo callback is assumed to
// declare. Build callbacks follow the contract's recommended artifact_id output
// (captured to state and forwarded to dependents), which matches what the
// example fleet's external build callbacks expose. Validate and deploy callbacks
// declare no standard outputs, so none are assumed.
func crossRepoOutputs(callbackType string) []string {
	if callbackType == config.CallbackTypeBuild {
		return []string{"artifact_id"}
	}
	return nil
}

func (g *Generator) writeHeader(sb *strings.Builder) {
	sb.WriteString(GeneratedFileMarker + "\n")
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n\n", g.config.GetManifestFile())
}

func (g *Generator) writeWorkflowTriggers(sb *strings.Builder) {
	sb.WriteString("name: Orchestrate CI/CD\n\n")
	sb.WriteString("on:\n")

	// release_trigger: dispatch drops the push: trigger so orchestrate runs only
	// on workflow_dispatch. A maintainer-owned gate then decides when a release
	// candidate is cut, instead of every trunk merge producing one. Default
	// (push) keeps the trunk-push trigger and its paths filter.
	if !g.config.OrchestrateDispatchOnly() {
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
// Default: cancel an older in-progress run when a newer push lands.
// the older run's work is obsolete. Override via config.concurrency.
func (g *Generator) writeConcurrency(sb *strings.Builder) {
	sb.WriteString("concurrency:\n")
	fmt.Fprintf(sb, "  group: %s\n", g.config.GetConcurrencyGroup())
	fmt.Fprintf(sb, "  cancel-in-progress: %t\n", g.config.GetConcurrencyCancelInProgress())
	sb.WriteString("\n")
}

func (g *Generator) writePermissions(sb *strings.Builder) {
	// Default to a least-privilege top-level block: reads only. Write scopes are
	// pushed down to the single job that needs them (the finalize job carries
	// contents: write, plus deployments: write when the GitHub Deployments API is
	// enabled). Callback scopes (e.g. id-token: write for OIDC) are scoped to
	// their own caller job via writeCallbackPermissions.
	base := [][2]string{
		{"contents", "read"},
		{"actions", "read"},
	}
	writeTopLevelPermissions(sb, base)
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

	// A custom changelog is a reusable workflow and must run as its own
	// job-level `uses:` call (it cannot be a step). Emit it before finalize so
	// finalize can consume its output via needs.changelog.outputs.changelog.
	if g.changelogJobEnabled() {
		g.writeChangelogJob(sb)
	}

	g.writeFinalizeJob(sb, sorted)
}

// changelogJobEnabled reports whether a dedicated custom-changelog job should be
// emitted. This mirrors the condition under which the finalize job would
// otherwise produce a changelog: release and changelog enabled, with a custom
// reusable workflow configured.
func (g *Generator) changelogJobEnabled() bool {
	return g.config.ReleaseEnabled() && g.config.ChangelogEnabled() && g.config.HasCustomChangelog()
}

func (g *Generator) writeSetupJob(sb *strings.Builder) {
	sb.WriteString("  setup:\n")
	fmt.Fprintf(sb, "    name: %s\n", SetupJobName)
	sb.WriteString("    runs-on: ubuntu-latest\n")
	g.writeOwnedTimeout(sb, "    ")
	sb.WriteString("    outputs:\n")

	// All outputs come from the CLI setup command. Output keys are normalized to
	// underscores via OutputKey: GitHub Actions parses a hyphen in an expression
	// as subtraction, so a hyphenated key would never match the consuming if:.
	for _, b := range g.config.Builds {
		key := config.OutputKey(b.Name)
		fmt.Fprintf(sb, "      run_build_%s: ${{ steps.setup.outputs.run_build_%s }}\n", key, key)
	}
	for _, d := range g.config.Deploys {
		key := config.OutputKey(d.Name)
		fmt.Fprintf(sb, "      run_deploy_%s: ${{ steps.setup.outputs.run_deploy_%s }}\n", key, key)
	}
	sb.WriteString("      head_sha: ${{ steps.setup.outputs.head_sha }}\n")
	sb.WriteString("      version: ${{ steps.setup.outputs.version }}\n")
	sb.WriteString("      previous_tag: ${{ steps.setup.outputs.previous_tag }}\n")
	sb.WriteString("      changelog_base_sha: ${{ steps.setup.outputs.changelog_base_sha }}\n")

	// Per-deployable base SHAs for callbacks that need them. Keys are normalized
	// to underscores via OutputKey for the same GitHub Actions expression reason.
	for _, b := range g.config.Builds {
		key := config.OutputKey(b.Name)
		fmt.Fprintf(sb, "      base_build_%s: ${{ steps.setup.outputs.base_build_%s }}\n", key, key)
	}
	for _, d := range g.config.Deploys {
		key := config.OutputKey(d.Name)
		fmt.Fprintf(sb, "      base_deploy_%s: ${{ steps.setup.outputs.base_deploy_%s }}\n", key, key)
	}

	sb.WriteString("    steps:\n")
	writeMintSteps(sb, g.config, "      ", seamRelease)
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

// writeSecretsBlock writes the secrets configuration for a reusable-workflow job
// call. This function always writes a trailing blank line so callers do not need
// to manage job-boundary spacing themselves; in the no-op (nil/unset) case a
// single blank line is still written to preserve correct YAML job separation.
//
// secrets: inherit is opt-in, never the default: a callback with no secrets
// config emits no secrets block at all, so the called workflow receives only the
// secrets it explicitly declares (least privilege).
//
// Behavior by value of s:
//   - nil (unset): emit no secrets block; write one blank line for job separation.
//   - s.Inherit == true (explicit opt-in): emit "    secrets: inherit\n\n".
//   - len(s.Map) > 0: emit the per-entry mapping form
//     "    secrets:\n      CALLED_NAME: ${{ secrets.CALLER_NAME }}\n...".
//   - inherit:false with no map: treated as unset (no secrets block).
func writeSecretsBlock(sb *strings.Builder, s *config.SecretsConfig) {
	if s == nil || (!s.Inherit && len(s.Map) == 0) {
		sb.WriteString("\n")
		return
	}
	if s.Inherit {
		sb.WriteString("    secrets: inherit\n\n")
		return
	}
	sb.WriteString("    secrets:\n")
	keys := make([]string, 0, len(s.Map))
	for k := range s.Map {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(sb, "      %s: ${{ secrets.%s }}\n", k, s.Map[k])
	}
	sb.WriteString("\n")
}

func (g *Generator) writeCallbackJob(sb *strings.Builder, info CallbackInfo, workflow string) {
	// For reusable-workflow callbacks that declare passthrough artifact downloads,
	// emit a cascade-owned pre-job that fetches the artifacts before the callback
	// runs. The pre-job depends on the same upstream jobs as the callback itself so
	// it can start as soon as the producers finish.
	downloadPreJobID := ""
	if info.PassthroughArtifact != nil && len(info.PassthroughArtifact.Downloads) > 0 {
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
	// passed here; they sequence the job without gating it.
	g.writeIfCondition(sb, info, needs)

	// timeout-minutes is forbidden on a reusable-workflow caller job
	// (jobs.<id>.uses): GitHub rejects the workflow at parse time. Every callback
	// is now a reusable-workflow call, so the timeout must live inside the called
	// workflow and nothing is emitted here.

	// strategy: emitted only for build callbacks that declare matrix:
	if info.Matrix != nil && len(info.Matrix.Dimensions) > 0 {
		g.writeStrategyBlock(sb, info.Matrix)
	}

	// environment: GitHub Actions forbids a job-level environment: key on a
	// reusable-workflow caller job (one that uses jobs.<id>.uses). Every deploy is
	// now a reusable-workflow call, so cascade does not emit a job-level
	// environment: here; the environment name is threaded via the with:
	// environment input instead, and GitHub Environment protection (required
	// reviewers, wait timers, deployment branch policy, scoped secrets) must be
	// declared inside the reusable workflow's own job.

	// continue-on-error: a callback with on_failure: continue is one the operator
	// has explicitly marked as tolerable. Emitting continue-on-error keeps the
	// overall run green when only such callbacks fail, while the result is still
	// recorded (the failure check below never gates on continue callbacks).
	if info.OnFailure == config.OnFailureContinue {
		sb.WriteString("    continue-on-error: true\n")
	}

	// permissions: is allowed on a reusable-workflow caller job. Render the
	// callback's configured scopes so the GITHUB_TOKEN is least-privilege per
	// callback and OIDC (id-token: write) / provenance (attestations: write) work.
	writeCallbackPermissions(sb, "    ", info.Permissions)

	// Every callback is emitted as a jobs.<id>.uses reusable-workflow call.
	fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(workflow))

	// with: pass outputs from dependencies
	g.writeWithInputs(sb, info)

	writeSecretsBlock(sb, info.Secrets)

	// Generate retry jobs if retries > 0
	for i := 1; i <= info.Retries; i++ {
		g.writeRetryJob(sb, info, workflow, i)
	}

	// For reusable-workflow callbacks that declare a passthrough artifact upload,
	// emit a cascade-owned post-job that uploads the artifact after the callback
	// completes.
	if info.PassthroughArtifact != nil && info.PassthroughArtifact.Upload != "" {
		g.writePassthroughUploadJob(sb, info)
	}
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
	// Use github.event.inputs.dry_run rather than the bare inputs.dry_run context:
	// orchestrate is triggered by push/schedule/workflow_run as well as dispatch,
	// and on the non-dispatch events the inputs context is null so ${{ inputs.dry_run }}
	// renders empty. Passing "" into a callback's boolean dry_run input fails the
	// reusable-workflow dispatch. The github.event.inputs accessor is null-safe on
	// those events (the callback falls back to its dry_run default), and on dispatch
	// it still forwards the operator's value. Compare against 'true' so the result is
	// a real boolean: the callback input is type: boolean, and GitHub Actions rejects
	// the empty string that github.event.inputs.dry_run yields on non-dispatch events.
	if info.SupportsDryRun {
		inputs = append(inputs, "      dry_run: ${{ github.event.inputs.dry_run == 'true' }}")
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
				// Boolean dispatch_inputs forward through the inputs context, which is
				// a string. A callback declaring the matching input as type: boolean
				// rejects the bare string, so compare against 'true' to coerce it to a
				// real boolean. Non-boolean inputs are forwarded verbatim.
				if g.config.DispatchInputs[name].Type == config.DispatchInputTypeBoolean {
					inputs = append(inputs, fmt.Sprintf("      %s: ${{ inputs.%s == 'true' }}", name, name))
				} else {
					inputs = append(inputs, fmt.Sprintf("      %s: ${{ inputs.%s }}", name, name))
				}
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
	// timeout-minutes is forbidden on a reusable-workflow caller job
	// (jobs.<id>.uses): GitHub rejects the workflow at parse time. A retry shim
	// re-invokes the reusable workflow via uses:, so no timeout is emitted here.
	// Per-callback timeout_minutes is rejected at config validation
	// (validateCallbackTimeout); the timeout must live inside the called workflow.

	// Propagate the matrix strategy to the retry job so that ${{ matrix.* }}
	// references in the reusable workflow's inputs remain bound. Without this,
	// the retry job runs in a matrix-less context and GHA treats the expressions
	// as unresolved empty strings.
	if info.Matrix != nil && len(info.Matrix.Dimensions) > 0 {
		g.writeStrategyBlock(sb, info.Matrix)
	}

	// The retry shim re-invokes the same reusable workflow, so it needs the same
	// least-privilege job-level permissions: as the original callback.
	writeCallbackPermissions(sb, "    ", info.Permissions)

	fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(workflow))
	g.writeWithInputs(sb, info)
	writeSecretsBlock(sb, info.Secrets)
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
	// The custom changelog runs as its own job; finalize consumes its output,
	// so it must be in finalize's needs:.
	if g.changelogJobEnabled() {
		allJobs = append(allJobs, changelogJobID)
	}

	sb.WriteString("  finalize:\n")
	fmt.Fprintf(sb, "    name: %s\n", FinalizeJobName)
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

	// Push the write scopes down to the finalize job: it commits state (and the
	// release) and, when the GitHub Deployments API is enabled, creates
	// Deployments and posts status updates. The top-level block stays read-only.
	finalizeScopes := [][2]string{{"contents", "write"}}
	if nativeDeploymentsEnabled(g.config) {
		finalizeScopes = append(finalizeScopes, [2]string{"deployments", "write"})
	}
	writeJobPermissions(sb, "    ", finalizeScopes)

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
	writeMintSteps(sb, g.config, "      ", seamRelease, seamState)
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

	// Opt-in GitHub Deployments API reporting for the runtime-selected environment.
	g.writeNativeDeploymentSteps(sb, sorted)

	// Generate failure check step - only fail on callbacks with on_failure: abort
	g.writeFailureCheckStep(sb, sorted)
}

// writeNativeDeploymentSteps wires the GitHub Deployments API lifecycle into the
// orchestrate finalize job. The orchestrate run deploys to a single environment
// selected at run time (github.event.inputs.environment, defaulting to the first
// configured environment), so the Deployment targets that resolved name. The
// terminal status reflects whether every deploy callback succeeded.
func (g *Generator) writeNativeDeploymentSteps(sb *strings.Builder, sorted []string) {
	if !nativeDeploymentsEnabled(g.config) {
		return
	}

	envExpr := "${{ github.event.inputs.environment }}"
	if len(g.config.Environments) > 0 {
		envExpr = fmt.Sprintf("${{ github.event.inputs.environment || '%s' }}", g.config.Environments[0])
	}

	// Collect deploy job IDs so the terminal status reflects the real deploy
	// outcome. The deployment succeeds only when every deploy callback succeeded.
	var deployJobs []string
	for _, jobID := range sorted {
		if g.graph.Nodes[jobID].Type == config.CallbackTypeDeploy {
			deployJobs = append(deployJobs, jobID)
		}
	}
	resultExpr := "success"
	if len(deployJobs) > 0 {
		var conds []string
		for _, jobID := range deployJobs {
			conds = append(conds, fmt.Sprintf("needs.%s.result == 'success'", jobID))
		}
		resultExpr = fmt.Sprintf("${{ (%s) && 'success' || 'failure' }}", strings.Join(conds, " && "))
	}

	writeNativeDeploymentSteps(sb, g.config, envExpr, resultExpr, "      ")
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
	fmt.Fprintf(sb, "          GH_TOKEN: %s\n", g.getStateTokenRef())
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
	// workflow declares an `artifact_id` output. The generator discovers
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

	// When any callback has auto_commits: true it may have pushed commits after
	// the workflow started, so HEAD is now ahead of the triggering SHA. Re-resolve
	// HEAD here so apply_state_edits() writes the post-callback commit to
	// state.<env>.sha rather than the stale triggering SHA.
	if g.anyAutoCommits() {
		sb.WriteString("          # One or more callbacks declared auto_commits: true; re-read HEAD\n")
		sb.WriteString("          # so state records the post-callback commit, not the triggering SHA.\n")
		sb.WriteString("          HEAD_SHA=\"$(git rev-parse HEAD)\"\n")
		sb.WriteString("          \n")
	}

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

	// Persist the manifest state to the trunk branch. On real GitHub this writes
	// through the Contents REST API so the commit is signed (Verified) and can
	// bypass branch protection with a capable token; in act/gitea it pushes with
	// the existing fetch/reset/reapply/commit/push retry loop. Concurrent
	// orchestrate runs racing to write state are handled by retrying on top of
	// the latest tip in both paths.
	commitMessage := "chore: update state [skip ci]"
	if len(g.config.Environments) > 0 {
		commitMessage = "chore: update state for $ENVIRONMENT [skip ci]"
	}
	writeStateCommitPush(sb, "          ", stateWriteParams{
		applyFn:       "apply_state_edits",
		commitMessage: commitMessage,
		noChangeLabel: "No state changes",
		successLabel:  "Pushed state",
		authorName:    g.config.GetGitUserName(),
		authorEmail:   g.config.GetGitUserEmail(),
	})
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
			fmt.Fprintf(sb, "            if (context.jobs['%s']?.outputs?.%s) {\n", jobID, out)
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

	// deploy_name is required: true by the primary's external-update consumer, so
	// it must always be present and non-empty. An explicit notify.deploy_name
	// override wins: it lets a satellite dispatch the name the primary recognizes
	// when that differs from the satellite's local deploy/build name. Otherwise,
	// prefer the first deploy name (satellites typically have one deploy); fall
	// back to the first build name for build-only artifact satellites; otherwise
	// use the satellite's own repo name from the runtime context as a last resort.
	switch {
	case g.config.Notify.DeployName != "":
		fmt.Fprintf(sb, "                deploy_name: '%s',\n", g.config.Notify.DeployName)
	case len(g.config.Deploys) > 0:
		fmt.Fprintf(sb, "                deploy_name: '%s',\n", g.config.Deploys[0].Name)
	case len(g.config.Builds) > 0:
		fmt.Fprintf(sb, "                deploy_name: '%s',\n", g.config.Builds[0].Name)
	default:
		sb.WriteString("                deploy_name: context.repo.repo,\n")
	}

	// An explicit notify.environment override wins: it lets a satellite dispatch
	// the environment the primary recognizes when that differs from the
	// satellite's first local environment (or when the satellite has none).
	switch {
	case g.config.Notify.Environment != "":
		fmt.Fprintf(sb, "                environment: '%s',\n", g.config.Notify.Environment)
	case len(g.config.Environments) > 0:
		fmt.Fprintf(sb, "                environment: context.payload.inputs?.environment || '%s',\n", g.config.Environments[0])
	default:
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

// writeChangelogStep emits the built-in changelog generation as a step inside
// the finalize job. The custom changelog path is NOT a step: a reusable
// workflow cannot be invoked as a step `uses:`, so it is hoisted into its own
// job (see writeChangelogJob) and this function does nothing for that case.
func (g *Generator) writeChangelogStep(sb *strings.Builder) {
	if g.config.HasCustomChangelog() {
		// Custom changelog is emitted as a dedicated job (writeChangelogJob),
		// not as a step, because a reusable workflow is invalid as a step uses:.
		return
	}
	{
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

// changelogJobID is the job name used for the hoisted custom changelog job.
// The finalize job depends on it and the release step reads its `changelog`
// output via needs.changelog.outputs.changelog.
const changelogJobID = "changelog"

// writeChangelogJob emits the custom changelog reusable workflow as a
// dedicated job-level `uses:` call. A reusable workflow cannot be invoked as a
// step `uses:`, so the custom changelog (config.Changelog.Workflow) is hoisted
// into its own job. The job depends on setup so it can read the SHAs the setup
// job exposes, and exposes the called workflow's `changelog` output for the
// finalize/release step to consume.
//
// This is only emitted when g.config.HasCustomChangelog() is true; the built-in
// changelog remains a step inside the finalize job (writeChangelogStep).
func (g *Generator) writeChangelogJob(sb *strings.Builder) {
	fmt.Fprintf(sb, "  %s:\n", changelogJobID)
	sb.WriteString("    name: Changelog\n")
	sb.WriteString("    needs: [setup]\n")
	fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(g.config.Changelog.Workflow))
	sb.WriteString("    with:\n")
	// Pass the base SHA from the output the setup job actually declares:
	// changelog_base_sha (not base_sha, which does not exist).
	sb.WriteString("      changelog_base_sha: ${{ needs.setup.outputs.changelog_base_sha }}\n")
	sb.WriteString("      head_sha: ${{ needs.setup.outputs.head_sha }}\n")
	sb.WriteString("      repo: ${{ github.repository }}\n")
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
		// Find the job ID for this callback name. Iterate in declaration order
		// (Order), not by ranging the Nodes map: a map range is randomized per
		// process, so when two callbacks share a name across sections the break
		// could pick either one run to run.
		var jobID string
		for _, jid := range g.graph.Order {
			if g.graph.Nodes[jid].Name == callbackName {
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
		if g.config.HasCustomChangelog() {
			// Custom changelog runs as its own job; read its job output.
			fmt.Fprintf(sb, "          changelog: ${{ needs.%s.outputs.changelog }}\n", changelogJobID)
		} else {
			// Built-in changelog runs as a step in this job; read the step output.
			sb.WriteString("          changelog: ${{ steps.changelog.outputs.changelog }}\n")
		}
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

	// Each consumed artifact is produced by a sibling <producer>-upload post-job
	// (writePassthroughUploadJob), not by the producer callback itself. The
	// download must wait for that upload job, otherwise it races ahead and fails
	// at runtime with "Artifact not found". A consumer's depends_on (if any) only
	// sequences it after the producer callback, which can finish before its
	// upload job does, so the upload edge is required independently. Only add the
	// edge when the producer actually declares an upload (its -upload job exists).
	seen := make(map[string]bool, len(needs))
	for _, n := range needs {
		seen[n] = true
	}
	for _, src := range info.PassthroughArtifact.Downloads {
		producerJobID := config.JobID(config.CallbackTypeBuild, src)
		producer, ok := g.graph.Nodes[producerJobID]
		if !ok || producer.PassthroughArtifact == nil || producer.PassthroughArtifact.Upload == "" {
			continue
		}
		uploadJobID := fmt.Sprintf("%s-upload", producerJobID)
		if !seen[uploadJobID] {
			seen[uploadJobID] = true
			needs = append(needs, uploadJobID)
		}
	}
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

// passthroughLegPattern returns the download-artifact pattern that a matrix
// build's per-leg artifacts must match to be collected into the consolidated
// build-{name} upload. cascade cannot inject upload steps into a reusable
// callback's matrix legs, so each leg is expected to upload an artifact named
// "{build-name}-{leg-suffix}" (e.g. image-linux-amd64 for a build named
// "image"). The "{build-name}-*" pattern collects every leg deterministically
// without hardcoding any dimension values.
func passthroughLegPattern(buildName string) string {
	return fmt.Sprintf("%s-*", buildName)
}

// passthroughUploadDir converts an upload path into a directory suitable as the
// download-artifact destination. download-artifact writes into a directory, so
// a trailing recursive glob ("dist/**", "dist/", "dist") is reduced to the
// base directory ("dist"). A bare "**" (upload the whole workspace) maps to "."
// so the merged legs land back at the workspace root.
func passthroughUploadDir(upload string) string {
	dir := strings.TrimSuffix(upload, "**")
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		return "."
	}
	return dir
}

// writePassthroughUploadJob emits a cascade-owned post-job that runs
// actions/upload-artifact after info's reusable-workflow callback completes.
// The artifact is named "build-{job-name}".
// Used for reusable-workflow callbacks where steps cannot be injected into the
// jobs.<id>.uses block.
//
// For matrix builds the callback fans out across legs that each run on their
// own runner, so the upload-path files only ever exist on those leg runners,
// never on this fresh post-job runner. Uploading directly here would find an
// empty path and produce no artifact, and a downstream consumer's download then
// fails with "Artifact not found". To consolidate, this job first downloads the
// per-leg artifacts (which the legs are expected to upload under the
// "{build-name}-*" convention; see passthroughLegPattern), merges them into the
// upload directory, and only then uploads the single consolidated build-{name}
// artifact consumers download. Non-matrix builds run on one runner whose files
// already live at the upload path, so they upload directly with no collect step.
func (g *Generator) writePassthroughUploadJob(sb *strings.Builder, info CallbackInfo) {
	postJobID := fmt.Sprintf("%s-upload", info.JobID)
	name := passthroughArtifactName(info.Name)
	isMatrix := info.Matrix != nil && len(info.Matrix.Dimensions) > 0
	fmt.Fprintf(sb, "  %s:\n", postJobID)
	fmt.Fprintf(sb, "    name: Upload artifact %s\n", name)
	fmt.Fprintf(sb, "    needs: [%s]\n", info.JobID)
	fmt.Fprintf(sb, "    if: needs.%s.result == 'success'\n", info.JobID)
	sb.WriteString("    runs-on: ubuntu-latest\n")
	g.writeOwnedTimeout(sb, "    ")
	sb.WriteString("    steps:\n")
	if isMatrix {
		// Collect the per-leg artifacts before consolidating. The legs upload
		// "{build-name}-*" artifacts; merge them into the upload directory.
		dir := passthroughUploadDir(info.PassthroughArtifact.Upload)
		sb.WriteString("      - name: Collect matrix leg artifacts\n")
		writeActionUses(sb, g.config, "        ", actionDownloadArtifact)
		sb.WriteString("        with:\n")
		fmt.Fprintf(sb, "          pattern: %s\n", passthroughLegPattern(info.Name))
		fmt.Fprintf(sb, "          path: %s\n", dir)
		sb.WriteString("          merge-multiple: true\n")
	}
	fmt.Fprintf(sb, "      - name: Upload artifact %s\n", name)
	writeActionUses(sb, g.config, "        ", actionUploadArtifact)
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          name: %s\n", name)
	fmt.Fprintf(sb, "          path: %s\n", info.PassthroughArtifact.Upload)
	sb.WriteString("\n")
}
