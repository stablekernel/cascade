package promote

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/ghaoutput"
)

var (
	promotionMode     string
	promotionForce    bool
	allowBreaking     bool
	deploysFilter     string
	rollbackOnFailure bool
	allowDowngrade    bool
	deployCheckFlags  map[string]bool
)

// newPreflightCommand creates the 'promote preflight' subcommand.
func newPreflightCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Validate and plan promotion",
		Long: `Run pre-flight checks for a promotion.

This command:
  1. Validates the promotion mode and configuration
  2. Calculates which environments will be updated
  3. Detects which deploys need to run based on file changes
  4. Checks for breaking changes
  5. Outputs all values needed by the workflow

Modes:
  - default: Sequential promotion (each env to immediate next)
  - <source>-to-<target>: Atomic cascade (e.g., dev-to-prod)

With --gha-output, writes directly to $GITHUB_OUTPUT for use in workflows.`,
		RunE: runPreflight,
	}

	cmd.Flags().StringVar(&promotionMode, "mode", "default", "Promotion mode: 'default' or cascade target (e.g., dev-to-prod)")
	cmd.Flags().BoolVar(&promotionForce, "force", false, "Continue on failure (default mode only)")
	cmd.Flags().BoolVar(&allowBreaking, "allow-breaking", false, "Allow breaking changes")
	cmd.Flags().StringVar(&deploysFilter, "deploys", "all", "Deploys to promote (comma-separated names or 'all')")
	cmd.Flags().BoolVar(&rollbackOnFailure, "rollback-on-failure", true, "Revert successful deploys if any fails")
	cmd.Flags().BoolVar(&allowDowngrade, "allow-downgrade", false, "Permit promoting an older version onto an env (downgrade); prod always requires this even if a lower env allowed it")

	return cmd
}

func runPreflight(cmd *cobra.Command, args []string) error {
	// Load config using ParseManifestFile to handle nested ci: key
	cicdFile, err := config.ParseManifestFile(configPath, "ci")
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// A component-scoped preflight reads its source and target env state from
	// state.components.<component>.<env>; overlay those rows into the working flat
	// state map so the source-env deployment check and version comparison resolve
	// the component's seed. A no-op for a single-component (empty) preflight.
	if err := overlayComponentState(cicdFile, configPath, componentName); err != nil {
		return err
	}

	// Parse and validate mode
	// Mode can be "default" for sequential promotion, or a cascade target like "dev-to-prod"
	var mode PromotionMode
	var cascadeTarget string
	switch {
	case promotionMode == "default" || promotionMode == "":
		mode = ModeDefault
	case strings.Contains(promotionMode, "-to-"):
		// Mode is a cascade target (e.g., "dev-to-prod")
		mode = ModeCascade
		cascadeTarget = promotionMode
	default:
		return fmt.Errorf("invalid mode: %s (must be 'default' or a cascade target like 'dev-to-prod')", promotionMode)
	}

	// Initialize deploy check flags from environment if available
	deployCheckFlags = make(map[string]bool)
	for _, d := range cicdFile.Config.Deploys {
		envKey := fmt.Sprintf("DEPLOY_%s", strings.ToUpper(d.Name))
		if val := os.Getenv(envKey); val != "" {
			deployCheckFlags[d.Name] = val == "true"
		}
	}

	// Parse deploys filter
	var deploysFilterList []string
	if deploysFilter != "" && deploysFilter != "all" {
		for _, d := range strings.Split(deploysFilter, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				deploysFilterList = append(deploysFilterList, d)
			}
		}
	}

	// Run preflight
	pf := NewPreflighter(PreflighterOptions{
		Config:            cicdFile,
		Mode:              mode,
		Target:            cascadeTarget,
		Force:             promotionForce,
		BaseDir:           "",
		DeploysFilter:     deploysFilterList,
		RollbackOnFailure: rollbackOnFailure,
		AllowDowngrade:    allowDowngrade,
	})
	for name, include := range deployCheckFlags {
		pf.SetDeployCheck(name, include)
	}

	result, err := pf.Run()
	if err != nil {
		return fmt.Errorf("preflight failed: %w", err)
	}

	// Override breaking check if allowed
	if allowBreaking {
		result.HasBreaking = false
		result.CanProceed = true
		result.BreakingBlockedAt = ""
	}

	// Output results
	if ghaOutput {
		return writePreflightGHAOutput(result)
	}

	if jsonOutput {
		return outputJSON(result)
	}

	// Human-readable output
	fmt.Printf("Mode: %s\n", result.Mode)
	if result.Target != "" {
		fmt.Printf("Target: %s\n", result.Target)
	}
	fmt.Printf("Source: %s (%s)\n", result.SourceEnv, truncateSHA(result.SourceSHA))
	fmt.Printf("Final Env: %s\n", result.TargetEnv)
	fmt.Printf("Deploys to run: %v\n", result.DeploysToRun)
	if len(result.ExternalDeploysToRun) > 0 {
		fmt.Printf("External deploys to run: %v\n", result.ExternalDeploysToRun)
	}
	fmt.Printf("Can proceed: %v\n", result.CanProceed)
	if result.BreakingBlockedAt != "" {
		fmt.Printf("Breaking change blocked at: %s\n", result.BreakingBlockedAt)
	}
	return nil
}

func writePreflightGHAOutput(result *PreflightResult) error {
	w := ghaoutput.New()

	// Mode info
	w.Set("mode", string(result.Mode))
	w.Set("target", result.Target)
	w.SetBool("force", result.Force)
	w.SetBool("rollback_on_failure", result.RollbackOnFailure)

	// Basic info
	w.Set("source_env", result.SourceEnv)
	w.Set("target_env", result.TargetEnv)
	w.Set("source_sha", result.SourceSHA)
	w.Set("source_version", result.SourceVersion)
	// The promoted artifact version is the canonical image tag for the promotion.
	// Deploy jobs consume this as their image_tag input.
	w.Set("source_image_tag", result.SourceVersion)
	// source_image_digest is the immutable artifact id threaded alongside the
	// mutable tag. Emit it only when present so deploys without a digest are
	// unaffected (Writer.Set always writes the key, even when empty).
	if result.SourceImageDigest != "" {
		w.Set("source_image_digest", result.SourceImageDigest)
	}
	w.Set("changelog_base_sha", result.ChangelogBaseSHA)
	w.Set("rollback_sha", result.RollbackSHA)

	// Environments
	w.Set("envs_to_update", strings.Join(result.EnvsToUpdate, ","))
	w.Set("skipped_envs", strings.Join(result.SkippedEnvs, ","))

	// Deploys as JSON array
	if err := w.SetJSON("deploys_to_run", result.DeploysToRun); err != nil {
		return err
	}

	// External deploys as JSON array
	if err := w.SetJSON("external_deploys_to_run", result.ExternalDeploysToRun); err != nil {
		return err
	}

	// Release info
	w.SetBool("is_prerelease_env", result.IsPrereleaseEnv)
	w.SetBool("is_final_env", result.IsFinalEnv)
	w.SetBool("is_cascade", result.IsCascade)
	w.Set("release_action", result.ReleaseAction)

	// Prod deployment
	w.SetBool("has_prod_deployment", result.HasProdDeployment)
	w.Set("prod_sha", result.ProdSHA)
	w.Set("prod_version", result.ProdVersion)

	// Breaking changes
	w.SetBool("has_breaking", result.HasBreaking)
	w.SetBool("can_proceed", result.CanProceed)
	w.Set("breaking_blocked_at", result.BreakingBlockedAt)

	// Full promotion result as JSON
	if err := w.SetJSON("promotion_result", result.PromotionResult); err != nil {
		return err
	}

	return w.Flush()
}

func outputJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
