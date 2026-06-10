package generate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// PromoteGenerator handles promote workflow generation
type PromoteGenerator struct {
	config         *config.TrunkConfig
	baseDir        string
	inputs         map[string][]string // deploy name -> input names
	requiredInputs map[string][]string // deploy name -> required input names
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

// getCLIRef returns the Git ref to use for the cascade actions.
// Supported values:
//   - "latest" → uses the "latest" tag (updated with each stable release)
//   - "beta" → uses "master" branch (bleeding edge, may be unstable)
//   - "vX.Y.Z" → uses a specific version tag
func (g *PromoteGenerator) getCLIRef() string {
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
func (g *PromoteGenerator) getReleaseTokenRef() string {
	return g.config.GetReleaseToken()
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
		// Inline run: deploys have no reusable-workflow file to parse inputs from;
		// their declared-input set comes from the manifest inputs: keys instead.
		if d.Run != "" {
			g.inputs[d.Name] = inputKeys(d.Inputs)
			continue
		}
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
		"environment": "preflight.outputs.target_env",
		"sha":         "preflight.outputs.source_sha",
		"image_tag":   "preflight.outputs.source_image_tag",
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
					fmt.Sprintf("deploy-%s requires input '%s' but it cannot be provided in promote workflow (available: environment, sha, image_tag)",
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
		if strVal, ok := v.(string); ok {
			for placeholder, replacement := range substitutions {
				strVal = strings.ReplaceAll(strVal, placeholder, replacement)
			}
			result[k] = strVal
		}
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

// writeMatrixBuildingLogic generates the bash logic to build a matrix for a single deploy.
// It iterates through promotions and builds matrix entries with resolved inputs.
func (g *PromoteGenerator) writeMatrixBuildingLogic(sb *strings.Builder, deploy *config.DeployConfig, outputName string) {
	// Serialize default inputs
	defaultInputsJSON, err := json.Marshal(deploy.Inputs)
	if err != nil {
		defaultInputsJSON = []byte("{}")
	}

	// Serialize env_inputs
	envInputsJSON, err := json.Marshal(deploy.EnvInputs)
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

	// Permissions needed for release management, state commits, and job queries.
	// actions:write is required to dispatch the Release workflow from the
	// finalize job when a final release is published.
	sb.WriteString("permissions:\n")
	sb.WriteString("  contents: write\n")
	sb.WriteString("  actions: write\n")
	sb.WriteString("\n")
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
	sb.WriteString("      - uses: actions/checkout@v4\n")
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
	sb.WriteString("      - uses: actions/checkout@v4\n")
	sb.WriteString("      - name: Setup CLI\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getReleaseTokenRef())
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())
	sb.WriteString("      - name: Validate Promotion\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          echo \"Promotion validated by preflight job\"\n")
	sb.WriteString("          echo \"Mode: ${{ github.event.inputs.mode }}\"\n")
	sb.WriteString("          echo \"Source: ${{ needs.preflight.outputs.source_env }}\"\n")
	sb.WriteString("          echo \"Final Env: ${{ needs.preflight.outputs.target_env }}\"\n")
	sb.WriteString("          echo \"::notice::Promotion validation completed successfully\"\n\n")
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

		if d.Run != "" {
			// Inline run: deploy callback — cascade-owned job with an inline run:
			// step. Inline callbacks declare their inputs via the manifest inputs:
			// keys (no reusable-workflow with: matrix); the standard environment/
			// sha/image_tag inputs reach the step as env: vars.
			fmt.Fprintf(sb, "    name: Deploy %s\n", d.Name)
			sb.WriteString("    needs: [preflight, promote]\n")
			fmt.Fprintf(sb, "    if: ${{ github.event.inputs.dry_run != 'true' && contains(fromJSON(needs.preflight.outputs.deploys_to_run), '%s') }}\n", d.Name)
			g.writeInlineDeployBody(sb, d,
				"${{ needs.preflight.outputs.target_env }}",
				"${{ needs.preflight.outputs.source_sha }}",
				"${{ needs.preflight.outputs.source_image_tag }}")
			continue
		}

		if hasInputs {
			// Matrix-based deploy job
			fmt.Fprintf(sb, "    name: Deploy %s (${{ matrix.environment }})\n", d.Name)
			sb.WriteString("    needs: [preflight, promote]\n")
			fmt.Fprintf(sb, "    if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.deploy_%s_matrix != '[]' }}\n", outputName)
			sb.WriteString("    strategy:\n")
			sb.WriteString("      fail-fast: false\n")
			sb.WriteString("      matrix:\n")
			fmt.Fprintf(sb, "        include: ${{ fromJSON(needs.preflight.outputs.deploy_%s_matrix) }}\n", outputName)
			fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(d.Workflow))
			sb.WriteString("    with:\n")

			// Pass all inputs from matrix
			// We need to pass each input that the deploy workflow accepts
			for inputName := range d.Inputs {
				fmt.Fprintf(sb, "      %s: ${{ matrix.%s }}\n", inputName, inputName)
			}
		} else {
			// Single deploy job (backwards compatibility)
			fmt.Fprintf(sb, "    name: Deploy %s\n", d.Name)
			sb.WriteString("    needs: [preflight, promote]\n")
			fmt.Fprintf(sb, "    if: ${{ github.event.inputs.dry_run != 'true' && contains(fromJSON(needs.preflight.outputs.deploys_to_run), '%s') }}\n", d.Name)
			fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(d.Workflow))
			sb.WriteString("    with:\n")
			sb.WriteString("      environment: ${{ needs.preflight.outputs.target_env }}\n")
			sb.WriteString("      sha: ${{ needs.preflight.outputs.source_sha }}\n")
			// Pass image_tag if the deploy workflow accepts it
			if g.deployHasInput(d.Name, "image_tag") {
				sb.WriteString("      image_tag: ${{ needs.preflight.outputs.source_image_tag }}\n")
			}
		}
		sb.WriteString("    secrets: inherit\n\n")
	}

	// Add prod deploy jobs for cascade mode (separate from intermediate promotions)
	// These run in parallel with other deploy jobs when has_prod_deployment == 'true'
	for _, d := range g.config.Deploys {
		fmt.Fprintf(sb, "  deploy-%s-prod:\n", d.Name)
		fmt.Fprintf(sb, "    name: Deploy %s (%s)\n", d.Name, finalEnv)
		sb.WriteString("    needs: [preflight, promote]\n")
		sb.WriteString("    if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.has_prod_deployment == 'true' }}\n")
		if d.Run != "" {
			g.writeInlineDeployBody(sb, d,
				finalEnv,
				"${{ needs.preflight.outputs.prod_sha }}",
				"${{ needs.preflight.outputs.prod_version }}")
			continue
		}
		fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(d.Workflow))
		sb.WriteString("    with:\n")
		fmt.Fprintf(sb, "      environment: %s\n", finalEnv)
		sb.WriteString("      sha: ${{ needs.preflight.outputs.prod_sha }}\n")
		// Pass image_tag if the deploy workflow accepts it
		if g.deployHasInput(d.Name, "image_tag") {
			sb.WriteString("      image_tag: ${{ needs.preflight.outputs.prod_version }}\n")
		}
		sb.WriteString("    secrets: inherit\n\n")
	}

	// Write external deploy jobs (for multi-repo orchestration)
	g.writeExternalDeployJobs(sb, finalEnv)
}

// writeInlineDeployBody emits the runs-on / steps body of a cascade-owned inline
// run: deploy callback in a promote workflow. The standard inputs a reusable
// deploy callback would receive via with: (environment, sha, and image_tag when
// the callback declares it) are surfaced to the inline step as env: variables.
func (g *PromoteGenerator) writeInlineDeployBody(sb *strings.Builder, d config.DeployConfig, environment, sha, imageTag string) {
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    steps:\n")
	fmt.Fprintf(sb, "      - name: Deploy %s\n", d.Name)

	sb.WriteString("        env:\n")
	fmt.Fprintf(sb, "          ENVIRONMENT: %s\n", environment)
	fmt.Fprintf(sb, "          SHA: %s\n", sha)
	if g.deployHasInput(d.Name, "image_tag") {
		fmt.Fprintf(sb, "          IMAGE_TAG: %s\n", imageTag)
	}

	shell := d.Shell
	if shell == "" {
		shell = "bash"
	}
	fmt.Fprintf(sb, "        shell: %s\n", shell)

	sb.WriteString("        run: |\n")
	for _, line := range strings.Split(strings.TrimRight(d.Run, "\n"), "\n") {
		fmt.Fprintf(sb, "          %s\n", line)
	}
	sb.WriteString("\n")
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
			fmt.Fprintf(sb, "    uses: %s\n", g.resolveExternalWorkflow(ext, d.Workflow))
			sb.WriteString("    with:\n")
			sb.WriteString("      environment: ${{ needs.preflight.outputs.target_env }}\n")
			// For external deploys, we pass the external deploy's SHA from state
			// The workflow will receive this via inputs
			sb.WriteString("      sha: ${{ needs.preflight.outputs.source_sha }}\n")
			sb.WriteString("    secrets: inherit\n\n")

			// Prod deploy job for cascade mode
			fmt.Fprintf(sb, "  deploy-%s-prod:\n", d.Name)
			fmt.Fprintf(sb, "    name: Deploy %s (%s, external)\n", d.Name, finalEnv)
			sb.WriteString("    needs: [preflight, promote]\n")
			sb.WriteString("    if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.has_prod_deployment == 'true' }}\n")
			fmt.Fprintf(sb, "    uses: %s\n", g.resolveExternalWorkflow(ext, d.Workflow))
			sb.WriteString("    with:\n")
			fmt.Fprintf(sb, "      environment: %s\n", finalEnv)
			sb.WriteString("      sha: ${{ needs.preflight.outputs.prod_sha }}\n")
			sb.WriteString("    secrets: inherit\n\n")
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
		fmt.Fprintf(sb, "    uses: %s\n", normalizeWorkflowPath(d.Workflow))
		sb.WriteString("    with:\n")
		sb.WriteString("      environment: ${{ needs.preflight.outputs.target_env }}\n")
		sb.WriteString("      sha: ${{ needs.preflight.outputs.rollback_sha }}\n")
		// Pass image_tag if the deploy workflow accepts it
		if g.deployHasInput(d.Name, "image_tag") {
			sb.WriteString("      image_tag: ${{ needs.preflight.outputs.changelog_base_sha }}\n")
		}
		sb.WriteString("    secrets: inherit\n\n")
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
			fmt.Fprintf(sb, "    uses: %s\n", g.resolveExternalWorkflow(ext, d.Workflow))
			sb.WriteString("    with:\n")
			sb.WriteString("      environment: ${{ needs.preflight.outputs.target_env }}\n")
			sb.WriteString("      sha: ${{ needs.preflight.outputs.rollback_sha }}\n")
			sb.WriteString("    secrets: inherit\n\n")
		}
	}
}

func (g *PromoteGenerator) writeFinalizeJob(sb *strings.Builder) {
	// Build needs list: [preflight, promote, deploy-<name1>, deploy-<name2>, deploy-<name1>-prod, ...]
	needs := []string{"preflight", "promote"}
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

	sb.WriteString("  finalize:\n")
	sb.WriteString("    name: Finalize\n")
	fmt.Fprintf(sb, "    needs: [%s]\n", strings.Join(needs, ", "))
	sb.WriteString("    if: always() && needs.preflight.result == 'success'\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    steps:\n")
	sb.WriteString("      - uses: actions/checkout@v4\n")
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

	// Trigger the Release workflow to build and attach binaries. GitHub does
	// not reliably fire release event webhooks when a draft release is
	// PATCHed to non-draft via API calls inside a workflow run (cf. #86).
	// An explicit workflow_dispatch is the only reliably-triggered path.
	// Only runs on publish (final release creation), not on prerelease
	// or env-to-env promotions.
	sb.WriteString("      - name: Trigger Release Build\n")
	sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.is_final_env == 'true' }}\n")
	sb.WriteString("        env:\n")
	fmt.Fprintf(sb, "          GITHUB_TOKEN: %s\n", g.getReleaseTokenRef())
	sb.WriteString("          TAG: ${{ steps.release-data.outputs.sem_version }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          # Only dispatch on real GitHub — in act/gitea e2e environments\n")
	sb.WriteString("          # GITHUB_SERVER_URL is http://gitea:3000 and the Release workflow\n")
	sb.WriteString("          # doesn't exist, so skip silently.\n")
	sb.WriteString("          if [[ \"$GITHUB_SERVER_URL\" != \"https://github.com\" ]]; then\n")
	sb.WriteString("            echo \"Skipping Release dispatch (not running on github.com: $GITHUB_SERVER_URL)\"\n")
	sb.WriteString("            exit 0\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          gh workflow run Release \\\n")
	sb.WriteString("            --repo \"${{ github.repository }}\" \\\n")
	sb.WriteString("            --ref \"$TAG\"\n\n")

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
	// which deploys succeeded — more reliable than the legacy
	// `gh api ... /jobs` query, which can't reach the GitHub API in act/Gitea
	// test environments.
	sb.WriteString("      - name: Finalize Promotion\n")
	sb.WriteString("        if: ${{ github.event.inputs.dry_run != 'true' }}\n")
	sb.WriteString("        env:\n")
	fmt.Fprintf(sb, "          GITHUB_TOKEN: %s\n", g.getReleaseTokenRef())
	sb.WriteString("          PROMOTION_RESULT: ${{ needs.preflight.outputs.promotion_result }}\n")
	for _, d := range g.config.Deploys {
		envKey := "DEPLOY_RESULT_" + strings.ToUpper(strings.ReplaceAll(d.Name, "-", "_"))
		fmt.Fprintf(sb, "          %s: ${{ needs.deploy-%s.result }}\n", envKey, d.Name)
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
