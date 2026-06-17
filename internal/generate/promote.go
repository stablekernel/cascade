package generate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// PromoteGenerator handles promote workflow generation
type PromoteGenerator struct {
	config         *config.TrunkConfig
	baseDir        string
	inputs         map[string][]string // deploy name -> input names
	requiredInputs map[string][]string // deploy name -> required input names
	// state is the manifest state block, used to resolve cascade-owned
	// ${{ state.<env>.<field> }} references in deploy inputs at generation
	// time. Optional: nil when no state is threaded.
	state map[string]*config.EnvState
}

// NewPromoteGenerator creates a new promote workflow generator
func NewPromoteGenerator(cfg *config.TrunkConfig, baseDir string) *PromoteGenerator {
	return &PromoteGenerator{
		config:         cfg,
		baseDir:        baseDir,
		inputs:         make(map[string][]string),
		requiredInputs: make(map[string][]string),
	}
}

// SetState threads the manifest state block into the promote generator so
// ${{ state.<env>.<field> }} input references resolve at generation time.
func (g *PromoteGenerator) SetState(state map[string]*config.EnvState) {
	g.state = state
}

// getCLIRef returns the Git ref for the cascade self-action. The default
// (cli_version unset or "latest") resolves to config.DefaultCLIVersion, an
// immutable release tag, so consumers never run an unpinned mutable ref.
// Supported values:
//   - unset / "latest" → config.DefaultCLIVersion (immutable, pinned default)
//   - "beta" → "master" branch (explicit opt-in, bleeding edge, may be unstable)
//   - "vX.Y.Z" → that specific version tag
func (g *PromoteGenerator) getCLIRef() string {
	if g.config.CLIVersion == "beta" {
		return "master" // Explicit opt-in escape hatch to trunk.
	}
	return g.config.GetCLIVersion()
}

// getReleaseTokenRef returns the token expression for release operations.
// Users configure the full expression via release_token config option.
func (g *PromoteGenerator) getReleaseTokenRef() string {
	return g.config.GetReleaseToken()
}

// getStateTokenRef returns the token expression used to write manifest state to
// the trunk branch. Users configure the full expression via the state_token
// config option; it defaults to "${{ secrets.GITHUB_TOKEN }}".
func (g *PromoteGenerator) getStateTokenRef() string {
	return g.config.GetStateToken()
}

// getManifestFilePath returns the manifest file path for use in generated scripts.
// Converts absolute paths to repo-relative paths since workflows run in checked out repos.
func (g *PromoteGenerator) getManifestFilePath() string {
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

// getActionPath returns the path to the manage-release action
func (g *PromoteGenerator) getActionPath() string {
	return fmt.Sprintf("./.github/actions/%s", g.config.GetActionFolder())
}

// Generate creates the promote workflow content
func (g *PromoteGenerator) Generate() (string, error) {
	// Discover inputs and required inputs for deploy workflows
	if err := g.discoverDeployInputs(); err != nil {
		return "", err
	}

	// Validate that all required inputs can be satisfied
	if err := g.validateRequiredInputs(); err != nil {
		return "", err
	}

	var sb strings.Builder

	g.writeHeader(&sb)
	g.writeWorkflowTriggers(&sb)
	g.writeConcurrency(&sb)
	g.writeJobs(&sb)

	return sb.String(), nil
}

// discoverDeployInputs parses deploy workflow files to discover their inputs
func (g *PromoteGenerator) discoverDeployInputs() error {
	for _, d := range g.config.Deploys {
		workflowPath := filepath.Join(g.baseDir, d.Workflow)
		data, err := os.ReadFile(workflowPath)
		if err != nil {
			// Skip if workflow doesn't exist yet
			continue
		}

		inputs, err := ParseWorkflowInputs(data)
		if err != nil {
			return fmt.Errorf("failed to parse inputs from %s: %w", d.Workflow, err)
		}
		g.inputs[d.Name] = inputs

		requiredInputs, err := ParseWorkflowRequiredInputs(data)
		if err != nil {
			return fmt.Errorf("failed to parse required inputs from %s: %w", d.Workflow, err)
		}
		g.requiredInputs[d.Name] = requiredInputs
	}
	return nil
}

// validateRequiredInputs checks that all required inputs can be satisfied
func (g *PromoteGenerator) validateRequiredInputs() error {
	// In promote workflow, available inputs come from preflight outputs
	availableInputs := map[string]string{
		"environment":  "preflight.outputs.target_env",
		"sha":          "preflight.outputs.source_sha",
		"image_tag":    "preflight.outputs.source_image_tag",
		"image_digest": "preflight.outputs.source_image_digest",
	}

	var errors []string

	for _, d := range g.config.Deploys {
		requiredInputs := g.requiredInputs[d.Name]
		if len(requiredInputs) == 0 {
			continue
		}

		for _, required := range requiredInputs {
			if _, ok := availableInputs[required]; !ok {
				errors = append(errors,
					fmt.Sprintf("deploy-%s requires input '%s' but it cannot be provided in promote workflow (available: environment, sha, image_tag, image_digest)",
						d.Name, required))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("required input validation failed:\n  - %s", strings.Join(errors, "\n  - "))
	}
	return nil
}

// deployHasInput checks if a deploy workflow accepts a specific input
func (g *PromoteGenerator) deployHasInput(deployName, inputName string) bool {
	for _, input := range g.inputs[deployName] {
		if input == inputName {
			return true
		}
	}
	return false
}

// resolveDeployInputs builds the inputs map for a deploy, merging defaults with env-specific overrides.
// Special variables are substituted: ${{ matrix.environment }}, ${{ matrix.sha }}, ${{ matrix.version }}
func (g *PromoteGenerator) resolveDeployInputs(deployName, env, sha, version string) map[string]interface{} {
	// Find deploy config
	var deploy *config.DeployConfig
	for i := range g.config.Deploys {
		if g.config.Deploys[i].Name == deployName {
			deploy = &g.config.Deploys[i]
			break
		}
	}
	if deploy == nil {
		return nil
	}

	// Start with defaults
	result := make(map[string]interface{})
	for k, v := range deploy.Inputs {
		result[k] = v
	}

	// Merge env-specific overrides
	if envInputs, ok := deploy.EnvInputs[env]; ok {
		for k, v := range envInputs {
			result[k] = v
		}
	}

	// Substitute special variables
	substitutions := map[string]string{
		"${{ matrix.environment }}": env,
		"${{ matrix.sha }}":         sha,
		"${{ matrix.version }}":     version,
	}

	for k, v := range result {
		strVal, ok := v.(string)
		if !ok {
			continue
		}
		// Pure passthrough expressions (e.g. ${{ vars.X }}, ${{ secrets.Y }})
		// are emitted directly into the deploy job's with: block, not routed
		// through the matrix JSON. Drop them here so they don't become dead
		// literals trapped inside the matrix payload.
		if classifyInputValue(strVal) == inputPassthrough {
			delete(result, k)
			continue
		}
		// Cascade-owned state.* references resolve at generation time against
		// the manifest state for this environment.
		if classifyInputValue(strVal) == inputStateRef {
			resolved, rerr := resolveInputValue(strVal, g.state)
			if rerr != nil {
				// Leave the value in place; generation surfaces the error via
				// validation. Keep the unresolved expression visible rather
				// than silently dropping it.
				result[k] = strVal
				continue
			}
			result[k] = resolved
			continue
		}
		// Literal or matrix.* placeholder: apply matrix substitutions.
		for placeholder, replacement := range substitutions {
			strVal = strings.ReplaceAll(strVal, placeholder, replacement)
		}
		result[k] = strVal
	}

	return result
}

// buildDeployMatrix creates a matrix of deploy inputs by resolving each promotion through resolveDeployInputs.
// Each promotion gets its environment, sha, and version applied to the deploy's inputs template.
func (g *PromoteGenerator) buildDeployMatrix(deployName string, promotions []map[string]string) []map[string]interface{} {
	var matrix []map[string]interface{}
	for _, promo := range promotions {
		inputs := g.resolveDeployInputs(deployName, promo["environment"], promo["sha"], promo["version"])
		if inputs != nil {
			matrix = append(matrix, inputs)
		}
	}
	return matrix
}

// writeMatrixBuildingStep generates the bash step that builds deploy matrices from promotion result.
// This step parses the promotion_result JSON and generates a matrix for each deploy that has inputs configured.
func (g *PromoteGenerator) writeMatrixBuildingStep(sb *strings.Builder) {
	// Only generate this step if at least one deploy has inputs
	hasDeploysWithInputs := false
	for _, d := range g.config.Deploys {
		if len(d.Inputs) > 0 {
			hasDeploysWithInputs = true
			break
		}
	}

	if !hasDeploysWithInputs {
		return
	}

	sb.WriteString("      - name: Build Deploy Matrices\n")
	sb.WriteString("        id: build-matrices\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          PROMOTION_RESULT: ${{ steps.preflight.outputs.promotion_result }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          # Extract promotions array from promotion result\n")
	sb.WriteString("          PROMOTIONS=$(echo \"$PROMOTION_RESULT\" | jq -c '.promotions // []')\n")
	sb.WriteString("          \n")

	// Generate matrix building logic for each deploy with inputs
	for _, d := range g.config.Deploys {
		if len(d.Inputs) == 0 {
			continue
		}

		outputName := strings.ReplaceAll(d.Name, "-", "_")
		fmt.Fprintf(sb, "          # Build matrix for deploy: %s\n", d.Name)
		fmt.Fprintf(sb, "          MATRIX_%s='['\n", strings.ToUpper(outputName))

		// Serialize default inputs and env-specific inputs as JSON for bash
		g.writeMatrixBuildingLogic(sb, &d, outputName)

		fmt.Fprintf(sb, "          MATRIX_%s=\"${MATRIX_%s}]\"\n", strings.ToUpper(outputName), strings.ToUpper(outputName))
		fmt.Fprintf(sb, "          echo \"deploy_%s_matrix=$MATRIX_%s\" >> \"$GITHUB_OUTPUT\"\n", outputName, strings.ToUpper(outputName))
		fmt.Fprintf(sb, "          echo \"::notice::Deploy %s matrix: $MATRIX_%s\"\n", d.Name, strings.ToUpper(outputName))
		sb.WriteString("          \n")
	}
}

// passthroughInputNames returns the sorted set of input keys on a deploy whose
// value is a pure passthrough expression (e.g. ${{ vars.X }}) in either the
// default inputs or any env override. These are emitted directly into the
// deploy job's with: block rather than routed through the matrix JSON, so the
// expression survives verbatim to GitHub Actions for run-time evaluation.
func passthroughInputNames(deploy *config.DeployConfig) []string {
	seen := make(map[string]struct{})
	consider := func(k string, v interface{}) {
		if s, ok := v.(string); ok && classifyInputValue(s) == inputPassthrough {
			seen[k] = struct{}{}
		}
	}
	for k, v := range deploy.Inputs {
		consider(k, v)
	}
	for _, envMap := range deploy.EnvInputs {
		for k, v := range envMap {
			consider(k, v)
		}
	}
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// passthroughInputValue returns the verbatim expression to emit for a
// passthrough input key. The default value wins; an env override is used only
// when the default is absent. Passthrough expressions (vars/secrets/env/...) are
// resolved by GitHub Actions at run time, so a single verbatim emission is
// correct across the matrix.
func passthroughInputValue(deploy *config.DeployConfig, key string) string {
	if v, ok := deploy.Inputs[key]; ok {
		if s, ok := v.(string); ok && classifyInputValue(s) == inputPassthrough {
			return strings.TrimSpace(s)
		}
	}
	for _, envMap := range deploy.EnvInputs {
		if v, ok := envMap[key]; ok {
			if s, ok := v.(string); ok && classifyInputValue(s) == inputPassthrough {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// matrixInputsJSON returns the default-inputs JSON with passthrough-expression
// keys removed and state.* references resolved against the manifest state for
// the given environment. Used to build the per-env matrix payload.
func (g *PromoteGenerator) matrixDefaultInputs(deploy *config.DeployConfig) map[string]interface{} {
	out := make(map[string]interface{})
	for k, v := range deploy.Inputs {
		if s, ok := v.(string); ok {
			switch classifyInputValue(s) {
			case inputPassthrough:
				continue // emitted directly in with:
			case inputStateRef:
				// Resolved per-env below in env-specific maps; default state
				// refs without an env anchor are left as-is for validation to
				// flag. Skip from default so a wrong default can't leak.
				continue
			}
		}
		out[k] = v
	}
	return out
}

// matrixEnvInputs returns env_inputs with passthrough keys removed and state.*
// references resolved at generation time per environment.
func (g *PromoteGenerator) matrixEnvInputs(deploy *config.DeployConfig) map[string]map[string]interface{} {
	out := make(map[string]map[string]interface{})
	// Seed every env so default-level state refs resolve into each env entry.
	envNames := make(map[string]struct{})
	for env := range deploy.EnvInputs {
		envNames[env] = struct{}{}
	}
	for _, env := range g.config.Environments {
		envNames[env] = struct{}{}
	}
	for env := range envNames {
		merged := make(map[string]interface{})
		// Default-level state refs resolve per env.
		for k, v := range deploy.Inputs {
			if s, ok := v.(string); ok && classifyInputValue(s) == inputStateRef {
				if resolved, rerr := resolveInputValue(s, g.state); rerr == nil {
					merged[k] = resolved
				}
			}
		}
		for k, v := range deploy.EnvInputs[env] {
			if s, ok := v.(string); ok {
				switch classifyInputValue(s) {
				case inputPassthrough:
					continue
				case inputStateRef:
					if resolved, rerr := resolveInputValue(s, g.state); rerr == nil {
						merged[k] = resolved
					}
					continue
				}
			}
			merged[k] = v
		}
		if len(merged) > 0 {
			out[env] = merged
		}
	}
	return out
}

// writeMatrixBuildingLogic generates the bash logic to build a matrix for a single deploy.
// It iterates through promotions and builds matrix entries with resolved inputs.
func (g *PromoteGenerator) writeMatrixBuildingLogic(sb *strings.Builder, deploy *config.DeployConfig, outputName string) {
	// Serialize default inputs (passthrough expressions excluded; state.*
	// refs resolved per-env into env_inputs below).
	defaultInputsJSON, err := json.Marshal(g.matrixDefaultInputs(deploy))
	if err != nil {
		defaultInputsJSON = []byte("{}")
	}

	// Serialize env_inputs (passthrough excluded, state.* resolved).
	envInputsJSON, err := json.Marshal(g.matrixEnvInputs(deploy))
	if err != nil {
		envInputsJSON = []byte("{}")
	}

	fmt.Fprintf(sb, "          DEFAULT_INPUTS='%s'\n", string(defaultInputsJSON))
	fmt.Fprintf(sb, "          ENV_INPUTS='%s'\n", string(envInputsJSON))
	sb.WriteString("          \n")
	sb.WriteString("          # Iterate through promotions\n")
	sb.WriteString("          FIRST=true\n")
	sb.WriteString("          for PROMO in $(echo \"$PROMOTIONS\" | jq -c '.[]'); do\n")
	sb.WriteString("            ENV=$(echo \"$PROMO\" | jq -r '.environment')\n")
	sb.WriteString("            SHA=$(echo \"$PROMO\" | jq -r '.sha')\n")
	sb.WriteString("            VERSION=$(echo \"$PROMO\" | jq -r '.version')\n")
	sb.WriteString("            \n")
	sb.WriteString("            # Merge default inputs with env-specific overrides\n")
	sb.WriteString("            RESOLVED=$(echo \"$DEFAULT_INPUTS\" | jq -c \".\")\n")
	sb.WriteString("            ENV_OVERRIDE=$(echo \"$ENV_INPUTS\" | jq -c --arg env \"$ENV\" '.[$env] // {}')\n")
	sb.WriteString("            RESOLVED=$(echo \"$RESOLVED\" | jq -c --argjson override \"$ENV_OVERRIDE\" '. + $override')\n")
	sb.WriteString("            \n")
	sb.WriteString("            # Substitute special variables\n")
	sb.WriteString("            RESOLVED=$(echo \"$RESOLVED\" | jq -c \\\n")
	sb.WriteString("              --arg env \"$ENV\" \\\n")
	sb.WriteString("              --arg sha \"$SHA\" \\\n")
	sb.WriteString("              --arg version \"$VERSION\" \\\n")
	sb.WriteString("              'walk(if type == \"string\" then \n")
	sb.WriteString("                gsub(\"\\\\$\\\\{\\\\{ matrix.environment \\\\}\\\\}\"; $env) |\n")
	sb.WriteString("                gsub(\"\\\\$\\\\{\\\\{ matrix.sha \\\\}\\\\}\"; $sha) |\n")
	sb.WriteString("                gsub(\"\\\\$\\\\{\\\\{ matrix.version \\\\}\\\\}\"; $version)\n")
	sb.WriteString("              else . end)')\n")
	sb.WriteString("            \n")
	sb.WriteString("            # Add to matrix (with comma separator)\n")
	sb.WriteString("            if [ \"$FIRST\" = \"true\" ]; then\n")
	fmt.Fprintf(sb, "              MATRIX_%s=\"${MATRIX_%s}${RESOLVED}\"\n", strings.ToUpper(outputName), strings.ToUpper(outputName))
	sb.WriteString("              FIRST=false\n")
	sb.WriteString("            else\n")
	fmt.Fprintf(sb, "              MATRIX_%s=\"${MATRIX_%s},${RESOLVED}\"\n", strings.ToUpper(outputName), strings.ToUpper(outputName))
	sb.WriteString("            fi\n")
	sb.WriteString("          done\n")
	sb.WriteString("          \n")
}

func (g *PromoteGenerator) writeHeader(sb *strings.Builder) {
	sb.WriteString("# AUTO-GENERATED by cascade - DO NOT EDIT MANUALLY\n")
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n", g.getManifestFilePath())
	sb.WriteString("#\n")

	// Document environments
	sb.WriteString("# Environments: ")
	for i, env := range g.config.Environments {
		if i > 0 {
			sb.WriteString(" → ")
		}
		sb.WriteString(env)
	}
	sb.WriteString("\n#\n")

	// Document promotion modes
	sb.WriteString("# Promotion modes:\n")
	sb.WriteString("#   default  - Sequential single-step promotion (each env → immediate next)\n")
	sb.WriteString("#              Supports --force to continue on failure\n")
	sb.WriteString("#   cascade  - Atomic cascade from source to target (e.g., dev-to-prod)\n")
	sb.WriteString("#              All intermediate environments updated with same artifact\n")
	sb.WriteString("#              Fails entirely if any step fails (no partial state)\n")
	sb.WriteString("#\n")
	sb.WriteString("# Release states (based on position):\n")
	if len(g.config.Environments) >= 2 {
		fmt.Fprintf(sb, "#   %s (second-from-top) = prerelease\n", g.config.Environments[len(g.config.Environments)-2])
		fmt.Fprintf(sb, "#   %s (top)             = released\n", g.config.Environments[len(g.config.Environments)-1])
	}
	sb.WriteString("#\n")

	// Document cascade targets
	cascadeTargets := g.config.GetCascadeTargets()
	if len(cascadeTargets) > 0 {
		sb.WriteString("# Cascade targets (for cascade mode):\n")
		for _, ct := range cascadeTargets {
			fmt.Fprintf(sb, "#   %-16s - Promotes %s → %s", ct.Name, ct.FromEnv, ct.ToEnv)
			if len(ct.EnvsToUpdate) > 1 {
				fmt.Fprintf(sb, " (also updates %s)", strings.Join(ct.EnvsToUpdate[:len(ct.EnvsToUpdate)-1], ", "))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("#\n")
	}

	// Document breaking change gate
	sb.WriteString("# Breaking changes:\n")
	sb.WriteString("#   Breaking changes block at: pre-release → release AND release → prod\n")
	sb.WriteString("#   Check 'allow_breaking_changes' to proceed with breaking changes.\n")
	sb.WriteString("\n")
}

func (g *PromoteGenerator) writeWorkflowTriggers(sb *strings.Builder) {
	sb.WriteString("name: Promote\n\n")
	sb.WriteString("on:\n")
	sb.WriteString("  workflow_dispatch:\n")
	sb.WriteString("    inputs:\n")

	// Promotion mode input - default (sequential) plus all cascade targets
	sb.WriteString("      mode:\n")
	sb.WriteString("        description: 'Promotion mode - default (sequential) or select a cascade target'\n")
	sb.WriteString("        type: choice\n")
	sb.WriteString("        required: true\n")
	sb.WriteString("        options:\n")
	sb.WriteString("          - default\n")
	for _, ct := range g.config.GetCascadeTargets() {
		fmt.Fprintf(sb, "          - %s\n", ct.Name)
	}
	sb.WriteString("        default: default\n")

	// Force flag (only used when mode=default)
	sb.WriteString("      force:\n")
	sb.WriteString("        description: 'Continue on failure (default mode only)'\n")
	sb.WriteString("        type: boolean\n")
	sb.WriteString("        default: false\n")

	// Breaking changes checkbox
	sb.WriteString("      allow_breaking_changes:\n")
	sb.WriteString("        description: 'Required if promoting breaking changes past pre-release → release'\n")
	sb.WriteString("        type: boolean\n")
	sb.WriteString("        default: false\n")

	// Dry run option
	sb.WriteString("      dry_run:\n")
	sb.WriteString("        description: 'Dry run mode'\n")
	sb.WriteString("        type: boolean\n")
	sb.WriteString("        default: false\n")

	// Selective deploys input
	sb.WriteString("      deploys:\n")
	sb.WriteString("        description: 'Deploys to promote (comma-separated names or \"all\")'\n")
	sb.WriteString("        type: string\n")
	sb.WriteString("        default: 'all'\n")

	// Rollback on failure option
	sb.WriteString("      rollback_on_failure:\n")
	sb.WriteString("        description: 'Revert successful deploys if any fails (atomic promotion)'\n")
	sb.WriteString("        type: boolean\n")
	sb.WriteString("        default: true\n")

	// Allow downgrade option
	sb.WriteString("      allow_downgrade:\n")
	sb.WriteString("        description: 'Permit promoting an older version (downgrade); prod always requires this'\n")
	sb.WriteString("        type: boolean\n")
	sb.WriteString("        default: false\n")

	// Per-deploy checkboxes (kept for backwards compatibility, deprecated)
	if len(g.config.Deploys) > 0 {
		sb.WriteString("      # Per-deploy selection (deprecated, use 'deploys' input instead)\n")
		for _, d := range g.config.Deploys {
			fmt.Fprintf(sb, "      deploy_%s:\n", d.Name)
			fmt.Fprintf(sb, "        description: '[Deprecated] Include %s deployment'\n", d.Name)
			sb.WriteString("        type: boolean\n")
			sb.WriteString("        default: true\n")
		}
	}
	sb.WriteString("\n")

	// Base: permissions needed for release management, state commits, and job
	// queries. actions:write is required to dispatch the Release workflow from
	// the finalize job when a final release is published. A reusable callback
	// cannot set its own job permissions, so any scope a deploy callback declares
	// (e.g. id-token: write for OIDC) is unioned in at the top level here.
	base := [][2]string{
		{"contents", "write"},
		{"actions", "write"},
	}
	writeTopLevelPermissions(sb, base, collectCallbackPermissions(g.config))
}

func (g *PromoteGenerator) writeJobs(sb *strings.Builder) {
	sb.WriteString("jobs:\n")
	g.writePreflightJob(sb)
	g.writePromoteJob(sb)
	g.writeDeployJobs(sb)
	g.writeRollbackJobs(sb)
	g.writeFinalizeJob(sb)
}

func (g *PromoteGenerator) writePreflightJob(sb *strings.Builder) {
	sb.WriteString("  preflight:\n")
	sb.WriteString("    name: Pre-flight Check\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    outputs:\n")
	// All outputs come from the CLI now
	sb.WriteString("      source_env: ${{ steps.preflight.outputs.source_env }}\n")
	sb.WriteString("      target_env: ${{ steps.preflight.outputs.target_env }}\n")
	sb.WriteString("      source_sha: ${{ steps.preflight.outputs.source_sha }}\n")
	sb.WriteString("      source_version: ${{ steps.preflight.outputs.source_version }}\n")
	sb.WriteString("      source_image_tag: ${{ steps.preflight.outputs.source_image_tag }}\n")
	sb.WriteString("      source_image_digest: ${{ steps.preflight.outputs.source_image_digest }}\n")
	sb.WriteString("      changelog_base_sha: ${{ steps.preflight.outputs.changelog_base_sha }}\n")
	sb.WriteString("      rollback_sha: ${{ steps.preflight.outputs.rollback_sha }}\n")
	sb.WriteString("      rollback_on_failure: ${{ steps.preflight.outputs.rollback_on_failure }}\n")
	sb.WriteString("      envs_to_update: ${{ steps.preflight.outputs.envs_to_update }}\n")
	sb.WriteString("      skipped_envs: ${{ steps.preflight.outputs.skipped_envs }}\n")
	sb.WriteString("      deploys_to_run: ${{ steps.preflight.outputs.deploys_to_run }}\n")
	sb.WriteString("      external_deploys_to_run: ${{ steps.preflight.outputs.external_deploys_to_run }}\n")
	sb.WriteString("      is_prerelease_env: ${{ steps.preflight.outputs.is_prerelease_env }}\n")
	sb.WriteString("      is_final_env: ${{ steps.preflight.outputs.is_final_env }}\n")
	sb.WriteString("      is_cascade: ${{ steps.preflight.outputs.is_cascade }}\n")
	sb.WriteString("      release_action: ${{ steps.preflight.outputs.release_action }}\n")
	sb.WriteString("      has_prod_deployment: ${{ steps.preflight.outputs.has_prod_deployment }}\n")
	sb.WriteString("      prod_sha: ${{ steps.preflight.outputs.prod_sha }}\n")
	sb.WriteString("      prod_version: ${{ steps.preflight.outputs.prod_version }}\n")
	sb.WriteString("      has_breaking: ${{ steps.preflight.outputs.has_breaking }}\n")
	sb.WriteString("      can_proceed: ${{ steps.preflight.outputs.can_proceed }}\n")
	sb.WriteString("      promotion_result: ${{ steps.preflight.outputs.promotion_result }}\n")

	// Add matrix outputs for each deploy with inputs
	for _, d := range g.config.Deploys {
		if len(d.Inputs) > 0 {
			outputName := strings.ReplaceAll(d.Name, "-", "_")
			fmt.Fprintf(sb, "      deploy_%s_matrix: ${{ steps.build-matrices.outputs.deploy_%s_matrix }}\n", outputName, outputName)
		}
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

	// Single CLI call does everything
	sb.WriteString("      - name: Run Preflight\n")
	sb.WriteString("        id: preflight\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          PROMOTION_MODE: ${{ github.event.inputs.mode }}\n")
	sb.WriteString("          PROMOTION_FORCE: ${{ github.event.inputs.force }}\n")
	sb.WriteString("          ALLOW_BREAKING: ${{ github.event.inputs.allow_breaking_changes }}\n")
	sb.WriteString("          DEPLOYS: ${{ github.event.inputs.deploys }}\n")
	sb.WriteString("          ROLLBACK_ON_FAILURE: ${{ github.event.inputs.rollback_on_failure }}\n")
	sb.WriteString("          ALLOW_DOWNGRADE: ${{ github.event.inputs.allow_downgrade }}\n")
	for _, d := range g.config.Deploys {
		fmt.Fprintf(sb, "          DEPLOY_%s: ${{ github.event.inputs.deploy_%s }}\n",
			strings.ToUpper(strings.ReplaceAll(d.Name, "-", "_")), d.Name)
	}
	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          cascade promote preflight \\\n")
	fmt.Fprintf(sb, "            --mode \"${PROMOTION_MODE:-default}\" \\\n")
	sb.WriteString("            --force=\"${PROMOTION_FORCE:-false}\" \\\n")
	fmt.Fprintf(sb, "            --config %s \\\n", g.getManifestFilePath())
	sb.WriteString("            --allow-breaking=\"${ALLOW_BREAKING:-false}\" \\\n")
	sb.WriteString("            --deploys=\"${DEPLOYS:-all}\" \\\n")
	sb.WriteString("            --rollback-on-failure=\"${ROLLBACK_ON_FAILURE:-true}\" \\\n")
	sb.WriteString("            --allow-downgrade=\"${ALLOW_DOWNGRADE:-false}\" \\\n")
	sb.WriteString("            --gha-output\n")

	// Fail if cannot proceed
	sb.WriteString("      - name: Fail if Cannot Proceed\n")
	sb.WriteString("        if: steps.preflight.outputs.can_proceed == 'false'\n")
	sb.WriteString("        run: exit 1\n")

	// Add matrix building step for deploys with inputs (still needed for deploy jobs)
	g.writeMatrixBuildingStep(sb)

	sb.WriteString("\n")
}

func (g *PromoteGenerator) writePromoteJob(sb *strings.Builder) {
	sb.WriteString("  promote:\n")
	sb.WriteString("    name: Promote\n")
	sb.WriteString("    needs: preflight\n")
	sb.WriteString("    if: ${{ github.event.inputs.dry_run != 'true' }}\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    steps:\n")
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("      - name: Setup CLI\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())
	sb.WriteString("      - name: Validate Promotion\n")
	// The mode input is untrusted workflow_dispatch data. GitHub expands ${{ ... }}
	// into the run: script before the shell runs it, so a mode value carrying shell
	// metacharacters would break out of the echo. Bind it to env: and print the
	// quoted shell variable instead.
	sb.WriteString("        env:\n")
	sb.WriteString("          MODE: ${{ github.event.inputs.mode }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          echo \"Promotion validated by preflight job\"\n")
	sb.WriteString("          echo \"Mode: $MODE\"\n")
	sb.WriteString("          echo \"Source: ${{ needs.preflight.outputs.source_env }}\"\n")
	sb.WriteString("          echo \"Final Env: ${{ needs.preflight.outputs.target_env }}\"\n")
	sb.WriteString("          echo \"::notice::Promotion validation completed successfully\"\n\n")
}

// writeDeployStrategyOptions emits the fail-fast and (when set) max-parallel
// lines inside a strategy: block. It must be called after the caller writes
// "    strategy:\n". The fail-fast default is false (preserves historical
// behaviour for callers that have no rollout config).
func (g *PromoteGenerator) writeDeployStrategyOptions(sb *strings.Builder, rollout *config.RolloutConfig) {
	failFast := false
	if rollout != nil && rollout.FailFast != nil {
		failFast = *rollout.FailFast
	}
	if failFast {
		sb.WriteString("      fail-fast: true\n")
	} else {
		sb.WriteString("      fail-fast: false\n")
	}
	if rollout != nil && rollout.MaxParallel > 0 {
		fmt.Fprintf(sb, "      max-parallel: %d\n", rollout.MaxParallel)
	}
}

func (g *PromoteGenerator) writeDeployJobs(sb *strings.Builder) {
	// Skip deploy jobs if no environments
	if len(g.config.Environments) == 0 {
		return
	}

	finalEnv := g.config.Environments[len(g.config.Environments)-1]

	// Write local deploy jobs
	for _, d := range g.config.Deploys {
		outputName := strings.ReplaceAll(d.Name, "-", "_")
		hasInputs := len(d.Inputs) > 0

		fmt.Fprintf(sb, "  deploy-%s:\n", d.Name)

		if hasInputs {
			// Matrix-based deploy job
			fmt.Fprintf(sb, "    name: Deploy %s (${{ matrix.environment }})\n", d.Name)
			sb.WriteString("    needs: [preflight, promote]\n")
			if d.SupportsDryRun {
				// Callback handles dry-run internally: run regardless of dry_run input.
				fmt.Fprintf(sb, "    if: ${{ needs.preflight.outputs.deploy_%s_matrix != '[]' }}\n", outputName)
			} else {
				fmt.Fprintf(sb, "    if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.deploy_%s_matrix != '[]' }}\n", outputName)
			}
			sb.WriteString("    strategy:\n")
			g.writeDeployStrategyOptions(sb, d.Rollout)
			sb.WriteString("      matrix:\n")
			fmt.Fprintf(sb, "        include: ${{ fromJSON(needs.preflight.outputs.deploy_%s_matrix) }}\n", outputName)
			writeCallbackPermissions(sb, "    ", d.Permissions)
			fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(d.Workflow))
			sb.WriteString("    with:\n")

			// When the callback opts in to dry-run passthrough, forward the
			// dispatch input so it can emulate internally.
			if d.SupportsDryRun {
				sb.WriteString("      dry_run: ${{ github.event.inputs.dry_run == 'true' }}\n")
			}

			// Passthrough-expression inputs (e.g. ${{ vars.X }}) are excluded
			// from the matrix JSON and emitted verbatim so GitHub Actions
			// evaluates them at run time.
			passthrough := passthroughInputNames(&d)
			passSet := make(map[string]struct{}, len(passthrough))
			for _, name := range passthrough {
				passSet[name] = struct{}{}
				fmt.Fprintf(sb, "      %s: %s\n", name, passthroughInputValue(&d, name))
			}

			// Remaining inputs (literals, matrix.* placeholders, resolved
			// state.* refs) come from the per-promotion matrix entry. Sorted
			// for deterministic output.
			matrixNames := make([]string, 0, len(d.Inputs))
			for inputName := range d.Inputs {
				if _, ok := passSet[inputName]; ok {
					continue
				}
				matrixNames = append(matrixNames, inputName)
			}
			sort.Strings(matrixNames)
			for _, inputName := range matrixNames {
				fmt.Fprintf(sb, "      %s: ${{ matrix.%s }}\n", inputName, inputName)
			}
		} else {
			// Single deploy job (backwards compatibility)
			fmt.Fprintf(sb, "    name: Deploy %s\n", d.Name)
			sb.WriteString("    needs: [preflight, promote]\n")
			if d.SupportsDryRun {
				// Callback handles dry-run internally: run regardless of dry_run input.
				fmt.Fprintf(sb, "    if: ${{ contains(fromJSON(needs.preflight.outputs.deploys_to_run), '%s') }}\n", d.Name)
			} else {
				fmt.Fprintf(sb, "    if: ${{ github.event.inputs.dry_run != 'true' && contains(fromJSON(needs.preflight.outputs.deploys_to_run), '%s') }}\n", d.Name)
			}
			// This branch only runs for external (uses:) deploys, so no job-level
			// environment: key is emitted: GitHub Actions forbids it on a
			// reusable-workflow caller job. The environment name is threaded via
			// the with: environment input below, and GitHub Environment protection
			// must be declared inside the reusable workflow's own job.
			writeCallbackPermissions(sb, "    ", d.Permissions)
			fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(d.Workflow))
			sb.WriteString("    with:\n")
			sb.WriteString("      environment: ${{ needs.preflight.outputs.target_env }}\n")
			sb.WriteString("      sha: ${{ needs.preflight.outputs.source_sha }}\n")
			// Pass image_tag if the deploy workflow accepts it
			if g.deployHasInput(d.Name, "image_tag") {
				sb.WriteString("      image_tag: ${{ needs.preflight.outputs.source_image_tag }}\n")
			}
			// Additively pass image_digest (the immutable artifact id) when the
			// deploy workflow declares it. This is gated independently of image_tag
			// so deploys that only want the digest, only the tag, or both all work.
			if g.deployHasInput(d.Name, "image_digest") {
				sb.WriteString("      image_digest: ${{ needs.preflight.outputs.source_image_digest }}\n")
			}
			// When the callback opts in to dry-run passthrough, forward the
			// dispatch input so it can emulate internally.
			if d.SupportsDryRun {
				sb.WriteString("      dry_run: ${{ github.event.inputs.dry_run == 'true' }}\n")
			}
		}
		writeSecretsBlock(sb, d.Secrets)
	}

	// Add prod deploy jobs for cascade mode (separate from intermediate promotions)
	// These run in parallel with other deploy jobs when has_prod_deployment == 'true'
	for _, d := range g.config.Deploys {
		fmt.Fprintf(sb, "  deploy-%s-prod:\n", d.Name)
		fmt.Fprintf(sb, "    name: Deploy %s (%s)\n", d.Name, finalEnv)
		sb.WriteString("    needs: [preflight, promote]\n")
		if d.SupportsDryRun {
			// Callback handles dry-run internally: run regardless of dry_run input.
			sb.WriteString("    if: ${{ needs.preflight.outputs.has_prod_deployment == 'true' }}\n")
		} else {
			sb.WriteString("    if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.has_prod_deployment == 'true' }}\n")
		}
		// The prod deploy job always targets a single known env (the final
		// environment in the pipeline). This is a reusable-workflow (uses:) caller
		// job, on which GitHub Actions forbids a job-level environment: key, so the
		// environment name is threaded via the with: environment input below and
		// GitHub Environment protection must be declared inside the reusable
		// workflow's own job.
		writeCallbackPermissions(sb, "    ", d.Permissions)
		fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(d.Workflow))
		sb.WriteString("    with:\n")
		fmt.Fprintf(sb, "      environment: %s\n", finalEnv)
		sb.WriteString("      sha: ${{ needs.preflight.outputs.prod_sha }}\n")
		// Pass image_tag if the deploy workflow accepts it
		if g.deployHasInput(d.Name, "image_tag") {
			sb.WriteString("      image_tag: ${{ needs.preflight.outputs.prod_version }}\n")
		}
		// image_digest is intentionally not threaded on the prod path: there is no
		// prod_image_digest preflight output today, so prod-path digest pinning is
		// not yet supported.
		// When the callback opts in to dry-run passthrough, forward the
		// dispatch input so it can emulate internally.
		if d.SupportsDryRun {
			sb.WriteString("      dry_run: ${{ github.event.inputs.dry_run == 'true' }}\n")
		}
		writeSecretsBlock(sb, d.Secrets)
	}

	// Write external deploy jobs (for multi-repo orchestration)
	g.writeExternalDeployJobs(sb, finalEnv)
}

func (g *PromoteGenerator) writeExternalDeployJobs(sb *strings.Builder, finalEnv string) {
	// Skip if no external deploys configured
	if len(g.config.External) == 0 {
		return
	}

	for _, ext := range g.config.External {
		for _, d := range ext.Deploys {
			// Standard external deploy job
			fmt.Fprintf(sb, "  deploy-%s:\n", d.Name)
			fmt.Fprintf(sb, "    name: Deploy %s (external)\n", d.Name)
			sb.WriteString("    needs: [preflight, promote]\n")
			fmt.Fprintf(sb, "    if: ${{ github.event.inputs.dry_run != 'true' && contains(fromJSON(needs.preflight.outputs.external_deploys_to_run), '%s') }}\n", d.Name)
			writeCallbackPermissions(sb, "    ", d.Permissions)
			fmt.Fprintf(sb, "    uses: %s\n", g.resolveExternalWorkflow(ext, d.Workflow))
			sb.WriteString("    with:\n")
			sb.WriteString("      environment: ${{ needs.preflight.outputs.target_env }}\n")
			// For external deploys, we pass the external deploy's SHA from state
			// The workflow will receive this via inputs
			sb.WriteString("      sha: ${{ needs.preflight.outputs.source_sha }}\n")
			writeSecretsBlock(sb, d.Secrets)

			// Prod deploy job for cascade mode
			fmt.Fprintf(sb, "  deploy-%s-prod:\n", d.Name)
			fmt.Fprintf(sb, "    name: Deploy %s (%s, external)\n", d.Name, finalEnv)
			sb.WriteString("    needs: [preflight, promote]\n")
			sb.WriteString("    if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.has_prod_deployment == 'true' }}\n")
			writeCallbackPermissions(sb, "    ", d.Permissions)
			fmt.Fprintf(sb, "    uses: %s\n", g.resolveExternalWorkflow(ext, d.Workflow))
			sb.WriteString("    with:\n")
			fmt.Fprintf(sb, "      environment: %s\n", finalEnv)
			sb.WriteString("      sha: ${{ needs.preflight.outputs.prod_sha }}\n")
			writeSecretsBlock(sb, d.Secrets)
		}
	}
}

// resolveExternalWorkflow resolves the workflow path for an external deploy
// If the workflow starts with .github/, it's a local workflow in the primary repo
// Otherwise, it should be a full path like org/repo/.github/workflows/deploy.yaml@ref
func (g *PromoteGenerator) resolveExternalWorkflow(ext config.ExternalRepoConfig, workflow string) string {
	if strings.HasPrefix(workflow, ".github/") {
		// Local workflow in primary repo
		return "./" + workflow
	}
	// External workflow - should already be in org/repo/.github/workflows/file.yaml@ref format
	// If it doesn't have @, add the ref from the external config
	if !strings.Contains(workflow, "@") && ext.Ref != "" {
		return workflow + "@" + ext.Ref
	}
	return workflow
}

// writeRollbackJobs generates rollback jobs that revert successful deploys when any deploy fails
// Rollback jobs only run when:
// 1. rollback_on_failure is 'true'
// 2. The deploy job for this deploy succeeded
// 3. At least one other deploy job failed
func (g *PromoteGenerator) writeRollbackJobs(sb *strings.Builder) {
	// Skip if no deploys configured
	if len(g.config.Deploys) == 0 && len(g.config.External) == 0 {
		return
	}

	// Deploy jobs are only emitted when there is at least one environment (see
	// writeDeployJobs). With no environments, the deploy jobs a rollback would
	// depend on do not exist, so emitting rollback jobs would produce a
	// needs: reference to a nonexistent job that GitHub rejects at parse time.
	if len(g.config.Environments) == 0 {
		return
	}

	// Collect all deploy job names for failure detection
	var allDeployJobs []string
	for _, d := range g.config.Deploys {
		allDeployJobs = append(allDeployJobs, fmt.Sprintf("deploy-%s", d.Name))
		allDeployJobs = append(allDeployJobs, fmt.Sprintf("deploy-%s-prod", d.Name))
	}
	for _, ext := range g.config.External {
		for _, d := range ext.Deploys {
			allDeployJobs = append(allDeployJobs, fmt.Sprintf("deploy-%s", d.Name))
			allDeployJobs = append(allDeployJobs, fmt.Sprintf("deploy-%s-prod", d.Name))
		}
	}

	// Build the failure condition: any deploy job failed
	var failureConditions []string
	for _, job := range allDeployJobs {
		failureConditions = append(failureConditions, fmt.Sprintf("needs.%s.result == 'failure'", job))
	}
	anyFailure := strings.Join(failureConditions, " || ")

	sb.WriteString("  # Rollback jobs - revert successful deploys if any deploy fails\n")

	// Write rollback jobs for local deploys
	for _, d := range g.config.Deploys {
		jobName := fmt.Sprintf("deploy-%s", d.Name)

		fmt.Fprintf(sb, "  rollback-%s:\n", d.Name)
		fmt.Fprintf(sb, "    name: Rollback %s\n", d.Name)
		fmt.Fprintf(sb, "    needs: [preflight, %s]\n", strings.Join(allDeployJobs, ", "))
		sb.WriteString("    if: |\n")
		sb.WriteString("      always() &&\n")
		sb.WriteString("      needs.preflight.outputs.rollback_on_failure == 'true' &&\n")
		sb.WriteString("      needs.preflight.outputs.rollback_sha != '' &&\n")
		fmt.Fprintf(sb, "      needs.%s.result == 'success' &&\n", jobName)
		fmt.Fprintf(sb, "      (%s)\n", anyFailure)
		writeCallbackPermissions(sb, "    ", d.Permissions)
		fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(d.Workflow))
		sb.WriteString("    with:\n")
		sb.WriteString("      environment: ${{ needs.preflight.outputs.target_env }}\n")
		sb.WriteString("      sha: ${{ needs.preflight.outputs.rollback_sha }}\n")
		// Pass image_tag if the deploy workflow accepts it
		if g.deployHasInput(d.Name, "image_tag") {
			sb.WriteString("      image_tag: ${{ needs.preflight.outputs.changelog_base_sha }}\n")
		}
		writeSecretsBlock(sb, d.Secrets)
	}

	// Write rollback jobs for external deploys
	for _, ext := range g.config.External {
		for _, d := range ext.Deploys {
			jobName := fmt.Sprintf("deploy-%s", d.Name)

			fmt.Fprintf(sb, "  rollback-%s:\n", d.Name)
			fmt.Fprintf(sb, "    name: Rollback %s (external)\n", d.Name)
			fmt.Fprintf(sb, "    needs: [preflight, %s]\n", strings.Join(allDeployJobs, ", "))
			sb.WriteString("    if: |\n")
			sb.WriteString("      always() &&\n")
			sb.WriteString("      needs.preflight.outputs.rollback_on_failure == 'true' &&\n")
			sb.WriteString("      needs.preflight.outputs.rollback_sha != '' &&\n")
			fmt.Fprintf(sb, "      needs.%s.result == 'success' &&\n", jobName)
			fmt.Fprintf(sb, "      (%s)\n", anyFailure)
			writeCallbackPermissions(sb, "    ", d.Permissions)
			fmt.Fprintf(sb, "    uses: %s\n", g.resolveExternalWorkflow(ext, d.Workflow))
			sb.WriteString("    with:\n")
			sb.WriteString("      environment: ${{ needs.preflight.outputs.target_env }}\n")
			sb.WriteString("      sha: ${{ needs.preflight.outputs.rollback_sha }}\n")
			writeSecretsBlock(sb, d.Secrets)
		}
	}
}

func (g *PromoteGenerator) writeFinalizeJob(sb *strings.Builder) {
	// Build needs list: [preflight, promote, deploy-<name1>, deploy-<name2>, deploy-<name1>-prod, ...]
	//
	// Deploy jobs are only emitted when there is at least one environment (see
	// writeDeployJobs). With no environments there are no deploy jobs, so
	// referencing them in needs: would produce a "needs job X which does not
	// exist" parse error on GitHub.
	needs := []string{"preflight", "promote"}
	if len(g.config.Environments) > 0 {
		for _, d := range g.config.Deploys {
			needs = append(needs, fmt.Sprintf("deploy-%s", d.Name))
		}
		// Add prod deploy jobs
		for _, d := range g.config.Deploys {
			needs = append(needs, fmt.Sprintf("deploy-%s-prod", d.Name))
		}
		// Add external deploy jobs
		for _, ext := range g.config.External {
			for _, d := range ext.Deploys {
				needs = append(needs, fmt.Sprintf("deploy-%s", d.Name))
				needs = append(needs, fmt.Sprintf("deploy-%s-prod", d.Name))
			}
		}
	}

	sb.WriteString("  finalize:\n")
	sb.WriteString("    name: Finalize\n")
	fmt.Fprintf(sb, "    needs: [%s]\n", strings.Join(needs, ", "))
	sb.WriteString("    if: always() && needs.preflight.result == 'success'\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    steps:\n")
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")
	sb.WriteString("      - name: Setup CLI\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())

	// Generate changelog
	sb.WriteString("      - name: Generate Changelog\n")
	sb.WriteString("        id: changelog\n")
	sb.WriteString("        env:\n")
	fmt.Fprintf(sb, "          GH_TOKEN: %s\n", g.getReleaseTokenRef())
	sb.WriteString("          CHANGELOG_BASE_SHA: ${{ needs.preflight.outputs.changelog_base_sha }}\n")
	sb.WriteString("          SOURCE_SHA: ${{ needs.preflight.outputs.source_sha }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          # Use changelog base SHA from preflight (first target env's current state)\n")
	sb.WriteString("          TARGET_SHA=\"$CHANGELOG_BASE_SHA\"\n")
	sb.WriteString("          if [[ -z \"$TARGET_SHA\" ]]; then\n")
	sb.WriteString("            # First deployment ever - compare against initial commit\n")
	sb.WriteString("            TARGET_SHA=$(git rev-list --max-parents=0 HEAD | tail -n 1)\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          \n")

	// Build changelog command with optional --contributors flag
	changelogCmd := "cascade generate-changelog --base-sha \"$TARGET_SHA\" --head-sha \"$SOURCE_SHA\" --repo \"${{ github.repository }}\""
	if g.config.Changelog != nil && g.config.Changelog.Contributors {
		changelogCmd += " --contributors"
	}
	fmt.Fprintf(sb, "          RESULT=$(%s)\n", changelogCmd)
	sb.WriteString("          echo \"changelog<<EOF\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("          echo \"$RESULT\" | jq -r '.changelog' >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("          echo \"EOF\" >> \"$GITHUB_OUTPUT\"\n")

	// Extract release data from promotion result (for prerelease/publish steps)
	// This contains the correct SHA and versions for the environment being released,
	// NOT the source (dev) environment values
	sb.WriteString("      - name: Extract Release Data\n")
	sb.WriteString("        id: release-data\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          PROMOTION_RESULT: ${{ needs.preflight.outputs.promotion_result }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          # Extract release_data from promotion result\n")
	sb.WriteString("          # This contains the correct values for the version being released:\n")
	sb.WriteString("          # - sha: the commit SHA of the release\n")
	sb.WriteString("          # - rc_version: the RC tag (e.g., v1.0.0-rc.0) - used for prerelease\n")
	sb.WriteString("          # - sem_version: the semver tag (e.g., v1.0.0) - used for publish\n")
	sb.WriteString("          RELEASE_DATA=$(echo \"$PROMOTION_RESULT\" | jq -r '.release_data // {}')\n")
	sb.WriteString("          \n")
	sb.WriteString("          if [[ \"$RELEASE_DATA\" != \"{}\" && \"$RELEASE_DATA\" != \"null\" ]]; then\n")
	sb.WriteString("            RELEASE_SHA=$(echo \"$RELEASE_DATA\" | jq -r '.sha // \"\"')\n")
	sb.WriteString("            RC_VERSION=$(echo \"$RELEASE_DATA\" | jq -r '.rc_version // \"\"')\n")
	sb.WriteString("            SEM_VERSION=$(echo \"$RELEASE_DATA\" | jq -r '.sem_version // \"\"')\n")
	sb.WriteString("            echo \"sha=$RELEASE_SHA\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("            echo \"rc_version=$RC_VERSION\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("            echo \"sem_version=$SEM_VERSION\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("            echo \"::notice::Release data - SHA: ${RELEASE_SHA:0:7}, RC: $RC_VERSION, Semver: $SEM_VERSION\"\n")
	sb.WriteString("          else\n")
	sb.WriteString("            echo \"::notice::No release data (not a prerelease/publish promotion)\"\n")
	sb.WriteString("            echo \"sha=\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("            echo \"rc_version=\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("            echo \"sem_version=\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("          fi\n")

	// Update release - for intermediate promotions (non-prerelease, non-final)
	sb.WriteString("      - name: Update Release\n")
	sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.is_prerelease_env != 'true' && needs.preflight.outputs.is_final_env != 'true' }}\n")
	fmt.Fprintf(sb, "        uses: %s\n", g.getActionPath())
	sb.WriteString("        with:\n")
	sb.WriteString("          repo: ${{ github.repository }}\n")
	sb.WriteString("          action: update\n")
	sb.WriteString("          environment: ${{ needs.preflight.outputs.target_env }}\n")
	sb.WriteString("          sha: ${{ needs.preflight.outputs.source_sha }}\n")
	sb.WriteString("          tag: ${{ needs.preflight.outputs.source_version }}\n")
	sb.WriteString("          changelog: ${{ steps.changelog.outputs.changelog }}\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())

	// For prerelease/final env targets, ensure the release exists first (create as draft if needed)
	// This handles the case where we're going directly to prerelease or prod without intermediate steps
	sb.WriteString("      - name: Ensure Release Exists\n")
	sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' && (needs.preflight.outputs.is_prerelease_env == 'true' || needs.preflight.outputs.is_final_env == 'true') }}\n")
	fmt.Fprintf(sb, "        uses: %s\n", g.getActionPath())
	sb.WriteString("        with:\n")
	sb.WriteString("          repo: ${{ github.repository }}\n")
	sb.WriteString("          action: update\n")
	sb.WriteString("          environment: ${{ needs.preflight.outputs.source_env }}\n")
	sb.WriteString("          sha: ${{ needs.preflight.outputs.source_sha }}\n")
	sb.WriteString("          tag: ${{ needs.preflight.outputs.source_version }}\n")
	sb.WriteString("          changelog: ${{ steps.changelog.outputs.changelog }}\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())

	// Prerelease - at second-to-last environment, mark as pre-release but KEEP RC tag
	// The RC suffix is only stripped when publishing to prod
	sb.WriteString("      - name: Create Prerelease\n")
	sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.is_prerelease_env == 'true' }}\n")
	fmt.Fprintf(sb, "        uses: %s\n", g.getActionPath())
	sb.WriteString("        with:\n")
	sb.WriteString("          repo: ${{ github.repository }}\n")
	sb.WriteString("          action: prerelease\n")
	sb.WriteString("          environment: ${{ needs.preflight.outputs.target_env }}\n")
	sb.WriteString("          sha: ${{ steps.release-data.outputs.sha }}\n")
	sb.WriteString("          tag: ${{ steps.release-data.outputs.rc_version }}\n")
	sb.WriteString("          changelog: ${{ steps.changelog.outputs.changelog }}\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())

	// Clean up orphaned releases for skipped environments
	sb.WriteString("      - name: Cleanup Orphaned Releases\n")
	sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.skipped_envs != '' }}\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          SKIPPED_ENVS: ${{ needs.preflight.outputs.skipped_envs }}\n")
	sb.WriteString("          SOURCE_VERSION: ${{ needs.preflight.outputs.source_version }}\n")
	fmt.Fprintf(sb, "          GITHUB_TOKEN: %s\n", g.getReleaseTokenRef())
	sb.WriteString("        run: |\n")
	sb.WriteString("          IFS=',' read -ra ENVS <<< \"$SKIPPED_ENVS\"\n")
	sb.WriteString("          for ENV in \"${ENVS[@]}\"; do\n")
	sb.WriteString("            echo \"Cleaning up orphaned release for $ENV\"\n")
	sb.WriteString("            cascade manage-release \\\n")
	sb.WriteString("              --repo \"${{ github.repository }}\" \\\n")
	sb.WriteString("              --action delete \\\n")
	sb.WriteString("              --environment \"$ENV\" \\\n")
	sb.WriteString("              --sha \"\" \\\n")
	sb.WriteString("              --tag \"$SOURCE_VERSION\" || true\n")
	sb.WriteString("          done\n")

	// Publish release - at final environment, convert prerelease to released
	// Creates semver tag (v1.0.0), updates release, and cleans up RC tags (v1.0.0-rc.*)
	// Uses release_data values (from release env), NOT source (dev) values
	sb.WriteString("      - name: Publish Release\n")
	sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.is_final_env == 'true' }}\n")
	fmt.Fprintf(sb, "        uses: %s\n", g.getActionPath())
	sb.WriteString("        with:\n")
	sb.WriteString("          repo: ${{ github.repository }}\n")
	sb.WriteString("          action: publish\n")
	sb.WriteString("          environment: ${{ needs.preflight.outputs.target_env }}\n")
	sb.WriteString("          sha: ${{ steps.release-data.outputs.sha }}\n")
	sb.WriteString("          tag: ${{ steps.release-data.outputs.sem_version }}\n")
	sb.WriteString("          delete_tag: ${{ steps.release-data.outputs.rc_version }}\n") // RC tag to find release
	sb.WriteString("          changelog: ${{ steps.changelog.outputs.changelog }}\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())

	// Trigger the configured release-build workflow to build and attach binaries.
	// GitHub does not reliably fire release event webhooks when a draft release is
	// PATCHed to non-draft via API calls inside a workflow run (cf. #86), so an
	// explicit workflow_dispatch is the only reliably-triggered path. This step is
	// emitted only when `release.workflow` is set, dispatches that configured
	// workflow, and runs on publish (final release creation) but not on prerelease
	// or env-to-env promotions.
	if g.config.Release != nil && g.config.Release.Workflow != "" {
		sb.WriteString("      - name: Trigger Release Build\n")
		sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.is_final_env == 'true' }}\n")
		sb.WriteString("        env:\n")
		fmt.Fprintf(sb, "          GITHUB_TOKEN: %s\n", g.getReleaseTokenRef())
		sb.WriteString("          TAG: ${{ steps.release-data.outputs.sem_version }}\n")
		sb.WriteString("        run: |\n")
		sb.WriteString("          # Dispatch the configured release-build workflow against the\n")
		sb.WriteString("          # published tag so it can build and attach release binaries.\n")
		sb.WriteString("          gh workflow run " + normalizeWorkflowPath(g.config.Release.Workflow) + " \\\n")
		sb.WriteString("            --repo \"${{ github.repository }}\" \\\n")
		sb.WriteString("            --ref \"$TAG\"\n\n")
	}

	// Publish callback: invoke once per configured build so users can retag
	// artifacts in their registries (Docker, Helm, npm, etc.). Only emitted
	// when the manifest has a `publish:` callback configured; the step is
	// skipped when this is not a final-env publication.
	if g.config.Publish != nil && g.config.Publish.Workflow != "" {
		sb.WriteString("      - name: Publish Artifacts\n")
		sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.is_final_env == 'true' }}\n")
		sb.WriteString("        env:\n")
		fmt.Fprintf(sb, "          GITHUB_TOKEN: %s\n", g.getReleaseTokenRef())
		sb.WriteString("          OLD_VERSION: ${{ steps.release-data.outputs.rc_version }}\n")
		sb.WriteString("          NEW_VERSION: ${{ steps.release-data.outputs.sem_version }}\n")
		sb.WriteString("          SOURCE_SHA: ${{ steps.release-data.outputs.sha }}\n")
		sb.WriteString("          SOURCE_ENV: ${{ needs.preflight.outputs.source_env }}\n")
		fmt.Fprintf(sb, "          MANIFEST_FILE: %s\n", g.getManifestFilePath())
		fmt.Fprintf(sb, "          MANIFEST_KEY: %s\n", g.config.GetManifestKey())
		sb.WriteString("        run: |\n")
		sb.WriteString("          # Invoke the publish callback once per configured build.\n")
		sb.WriteString("          # Reads artifact_id from the source env's build state, passing it\n")
		sb.WriteString("          # alongside version metadata so the user can retag artifacts.\n")
		for _, b := range g.config.Builds {
			bName := b.Name
			sb.WriteString("          ARTIFACT_ID_" + strings.ToUpper(strings.ReplaceAll(bName, "-", "_")))
			sb.WriteString("=$(yq eval \".$MANIFEST_KEY.state.$SOURCE_ENV.builds." + bName + ".artifact_id // \\\"\\\"\" \"$MANIFEST_FILE\")\n")
			sb.WriteString("          gh workflow run " + normalizeWorkflowPath(g.config.Publish.Workflow) + " \\\n")
			sb.WriteString("            --repo \"${{ github.repository }}\" \\\n")
			sb.WriteString("            --ref \"$NEW_VERSION\" \\\n")
			sb.WriteString("            -f build_name=" + bName + " \\\n")
			sb.WriteString("            -f old_version=\"$OLD_VERSION\" \\\n")
			sb.WriteString("            -f new_version=\"$NEW_VERSION\" \\\n")
			sb.WriteString("            -f sha=\"$SOURCE_SHA\" \\\n")
			sb.WriteString("            -f artifact_id=\"$ARTIFACT_ID_" + strings.ToUpper(strings.ReplaceAll(bName, "-", "_")) + "\" || true\n")
		}
		sb.WriteString("\n")
	}

	// Finalize promotion with CLI (replaces "Query Deploy Results" and "Update State")
	//
	// Each deploy job's conclusion is passed in as DEPLOY_RESULT_<NAME>, derived
	// from `needs.deploy-<name>.result`. finalize reads these env vars to know
	// which deploys succeeded. More reliable than the legacy
	// `gh api ... /jobs` query, which can't reach the GitHub API in act/Gitea
	// test environments.
	sb.WriteString("      - name: Finalize Promotion\n")
	sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' }}\n")
	sb.WriteString("        env:\n")
	// GH_TOKEN authenticates the Contents REST API write that finalize performs
	// on real GitHub (signed commit, branch-protection bypass). It defaults to
	// the same token as the release operations but is independently configurable
	// via state_token so a bot/App token can be supplied for protected trunks.
	fmt.Fprintf(sb, "          GH_TOKEN: %s\n", g.getStateTokenRef())
	fmt.Fprintf(sb, "          GITHUB_TOKEN: %s\n", g.getReleaseTokenRef())
	sb.WriteString("          PROMOTION_RESULT: ${{ needs.preflight.outputs.promotion_result }}\n")
	// Deploy result env vars reference deploy jobs, which only exist when there
	// is at least one environment. Skip them otherwise so finalize does not
	// dereference a job that was never emitted.
	if len(g.config.Environments) > 0 {
		for _, d := range g.config.Deploys {
			envKey := "DEPLOY_RESULT_" + strings.ToUpper(strings.ReplaceAll(d.Name, "-", "_"))
			fmt.Fprintf(sb, "          %s: ${{ needs.deploy-%s.result }}\n", envKey, d.Name)
		}
	}
	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          cascade promote finalize \\\n")
	fmt.Fprintf(sb, "            --config %s \\\n", g.getManifestFilePath())
	sb.WriteString("            --promotion-result \"$PROMOTION_RESULT\" \\\n")
	sb.WriteString("            --repo \"${{ github.repository }}\" \\\n")
	sb.WriteString("            --run-id \"${{ github.run_id }}\" \\\n")
	sb.WriteString("            --commit-push\n")

	// Summary
	sb.WriteString("      - name: Summary\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          PROMOTION_MODE: ${{ github.event.inputs.mode }}\n")
	sb.WriteString("          SOURCE_ENV: ${{ needs.preflight.outputs.source_env }}\n")
	sb.WriteString("          TARGET_ENV: ${{ needs.preflight.outputs.target_env }}\n")
	sb.WriteString("          ENVS_UPDATED: ${{ needs.preflight.outputs.envs_to_update }}\n")
	sb.WriteString("          SOURCE_SHA: ${{ needs.preflight.outputs.source_sha }}\n")
	sb.WriteString("          DRY_RUN: ${{ github.event.inputs.dry_run }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          {\n")
	sb.WriteString("            echo \"## Promotion Complete\"\n")
	sb.WriteString("            echo \"\"\n")
	sb.WriteString("            echo \"| Property | Value |\"\n")
	sb.WriteString("            echo \"|----------|-------|\"\n")
	sb.WriteString("            echo \"| Mode | $PROMOTION_MODE |\"\n")
	sb.WriteString("            echo \"| From | $SOURCE_ENV |\"\n")
	sb.WriteString("            echo \"| To | $TARGET_ENV |\"\n")
	sb.WriteString("            echo \"| Environments Updated | $ENVS_UPDATED |\"\n")
	sb.WriteString("            echo \"| SHA | \\`$SOURCE_SHA\\` |\"\n")
	sb.WriteString("            if [[ \"$DRY_RUN\" == \"true\" ]]; then\n")
	sb.WriteString("              echo \"| **DRY RUN** | Yes |\"\n")
	sb.WriteString("            fi\n")
	sb.WriteString("          } >> \"$GITHUB_STEP_SUMMARY\"\n")
}

// writeConcurrency emits a top-level concurrency: block on the promote workflow.
// Every promote finalize pushes the same shared .github/manifest.yaml (env state)
// and writes shared release tags, so ANY two concurrent promote runs race on those
// non-fast-forward pushes regardless of mode. The group key is therefore the bare
// workflow name, which serializes all promote runs against each other. Queueing
// (cancel-in-progress: false) is safer than cancelling: promote mutates durable env
// state and tags, so abandoning a mid-flight run leaves state partially written.
func (g *PromoteGenerator) writeConcurrency(sb *strings.Builder) {
	sb.WriteString("concurrency:\n")
	if g.config.Concurrency != nil && g.config.Concurrency.Group != "" {
		fmt.Fprintf(sb, "  group: %s\n", g.config.Concurrency.Group)
	} else {
		sb.WriteString("  group: \"${{ github.workflow }}\"\n")
	}
	if g.config.Concurrency != nil {
		fmt.Fprintf(sb, "  cancel-in-progress: %t\n", g.config.Concurrency.CancelInProgress)
	} else {
		sb.WriteString("  cancel-in-progress: false\n")
	}
	sb.WriteString("\n")
}
