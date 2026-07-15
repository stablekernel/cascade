// Package orchestrate provides CLI commands for CI/CD orchestration.
package orchestrate

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/globals"
	"github.com/stablekernel/cascade/internal/log"
	"github.com/stablekernel/cascade/internal/output"
)

// Common flags shared across subcommands
var (
	configPath  string
	manifestKey string
	environment string
	headSHA     string
	component   string
	ghaOutput   bool
)

// NewCommand creates the orchestrate command with subcommands.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "orchestrate",
		Short: "Orchestrate CI/CD workflow steps",
		Long: `Orchestrate CI/CD workflow steps for trunk-based development.

This command provides subcommands for each phase of the CI/CD orchestration:

  setup     - Detect changes, calculate version, determine what to run
  finalize  - Generate changelog, manage release, update state

Each subcommand can be run independently within a GitHub Actions workflow,
or the entire orchestration can be run locally for testing.

Examples:
  # Run setup phase (outputs JSON for workflow consumption)
  cascade orchestrate setup --environment dev --json

  # Run finalize phase with deploy results
  cascade orchestrate finalize --environment dev --version v1.0.0-rc.1 \
    --deploy-results "infra:success,app:success"

  # Preview what would happen (dry-run mode)
  cascade orchestrate setup --environment dev --dry-run`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// This hook shadows the root's PersistentPreRun, so the global
			// flags (--dry-run, --json, --trace) must be applied here.
			globals.ApplyFlags(cmd)
			// Auto-detect config file if not specified
			if configPath == "" {
				configPath = config.FindConfigFile("")
			}
			return nil
		},
	}

	// Add persistent flags shared by all subcommands
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "Path to CI/CD config file (auto-detects .github/manifest.yaml or .github/cicd.yaml)")
	cmd.PersistentFlags().StringVar(&manifestKey, "manifest-key", config.DefaultManifestKey, "Key in manifest file containing CI config")
	cmd.PersistentFlags().StringVar(&environment, "environment", "", "Target environment (empty for no-environment setup)")
	cmd.PersistentFlags().StringVar(&component, "component", "", "Declared component to scope this orchestration to (multi-component manifests)")
	cmd.PersistentFlags().BoolVar(&ghaOutput, "gha-output", false, "Write outputs to $GITHUB_OUTPUT for workflow consumption")

	// Add subcommands
	cmd.AddCommand(newSetupCommand())
	cmd.AddCommand(newFinalizeCommand())

	return cmd
}

// newSetupCommand creates the 'orchestrate setup' subcommand.
func newSetupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Detect changes, calculate version, determine what to run",
		Long: `Run the setup phase of orchestration.

This command:
  1. Reads the CI/CD config and current state
  2. Determines head SHA and per-deployable base SHAs
  3. Detects which builds/deploys need to run based on file changes
  4. Calculates the next version
  5. Determines changelog base SHA for release notes

Output (JSON):
  {
    "head_sha": "abc123...",
    "version": "v1.0.0-rc.1",
    "run_builds": {"app": true},
    "run_deploys": {"infra": "true", "app": "pending"},
    "changelog_base_sha": "def456...",
    "previous_tag": "v0.9.0"
  }`,
		RunE: runSetup,
	}

	cmd.Flags().StringVar(&headSHA, "sha", "", "Head SHA (default: current HEAD)")

	return cmd
}

// newFinalizeCommand creates the 'orchestrate finalize' subcommand.
func newFinalizeCommand() *cobra.Command {
	var (
		version       string
		deployResults string
		buildResults  string
	)

	cmd := &cobra.Command{
		Use:   "finalize",
		Short: "Generate changelog, manage release, update state",
		Long: `Run the finalize phase of orchestration.

This command:
  1. Generates changelog from commits
  2. Creates/updates draft release with changelog
  3. Uploads release artifacts
  4. Updates manifest state for successful deploys
  5. Commits and pushes state changes

The --deploy-results flag specifies which deploys succeeded:
  --deploy-results "infra:success,app:success"
  --deploy-results "infra:success,app:failure"

Only successful deploys will have their state updated in the manifest.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFinalize(version, deployResults, buildResults)
		},
	}

	cmd.Flags().StringVar(&headSHA, "sha", "", "Head SHA")
	cmd.Flags().StringVar(&version, "version", "", "Calculated version")
	cmd.Flags().StringVar(&deployResults, "deploy-results", "", "Deploy results (e.g., 'infra:success,app:failure')")
	cmd.Flags().StringVar(&buildResults, "build-results", "", "Build results (e.g., 'app:success')")

	return cmd
}

// runSetup executes the setup phase.
func runSetup(cmd *cobra.Command, args []string) error {
	log.Section("Orchestrate Setup")
	log.Info("Environment: %s", environment)
	log.Debug("Config path: %s", configPath)

	if globals.DryRun() {
		log.Info("%sRunning in dry-run mode", log.DryRunPrefix())
	}

	// Create orchestrator. WithComponent("") is a no-op, so the single-component
	// path is unchanged; a per-component generated workflow passes --component to
	// scope version derivation to that component's path and tag namespace.
	orch, err := NewOrchestrator(configPath, manifestKey, environment, WithComponent(component))
	if err != nil {
		return fmt.Errorf("initializing orchestrator: %w", err)
	}

	// Run setup
	result, err := orch.Setup(headSHA)
	if err != nil {
		return fmt.Errorf("setup failed: %w", err)
	}

	// Output result based on mode
	if ghaOutput {
		// Write to $GITHUB_OUTPUT for workflow consumption
		if err := result.WriteGHAOutput(); err != nil {
			return fmt.Errorf("writing GHA output: %w", err)
		}
		// Also log summary for visibility
		log.Info("Setup complete - wrote outputs to $GITHUB_OUTPUT")
		log.Info("Version: %s", result.Version)
		return nil
	}

	return output.Result(result, func() {
		log.Info("Version: %s", result.Version)
		log.Info("Head SHA: %s", result.HeadSHA)
		for name, run := range result.RunBuilds {
			log.Info("Build %s: %v", name, run)
		}
		for name, run := range result.RunDeploys {
			log.Info("Deploy %s: %s", name, run)
		}
	})
}

// runFinalize executes the finalize phase.
func runFinalize(version, deployResults, buildResults string) error {
	log.Section("Orchestrate Finalize")
	log.Info("Environment: %s", environment)
	log.Debug("Config path: %s", configPath)

	if globals.DryRun() {
		log.Info("%sRunning in dry-run mode", log.DryRunPrefix())
	}

	// Create orchestrator. WithComponent("") is a no-op, so the single-component
	// path is unchanged; a per-component generated workflow passes --component so
	// the seeded state is recorded under state.components.<component>.<env> rather
	// than the shared flat state.<env>, keeping two components from overwriting
	// each other's seed row.
	orch, err := NewOrchestrator(configPath, manifestKey, environment, WithComponent(component))
	if err != nil {
		return fmt.Errorf("initializing orchestrator: %w", err)
	}

	// Parse deploy results
	deploys := parseResults(deployResults)
	builds := parseResults(buildResults)

	log.Debug("Deploy results: %v", deploys)
	log.Debug("Build results: %v", builds)

	// Run finalize
	if err := orch.Finalize(headSHA, version, deploys, builds); err != nil {
		return fmt.Errorf("finalize failed: %w", err)
	}

	log.Info("Finalize complete")
	return nil
}

// parseResults parses a comma-separated result string like "infra:success,app:failure"
func parseResults(s string) map[string]string {
	results := make(map[string]string)
	if s == "" {
		return results
	}

	// Simple parsing - split by comma, then by colon
	for _, part := range splitTrim(s, ",") {
		kv := splitTrim(part, ":")
		if len(kv) == 2 {
			results[kv[0]] = kv[1]
		}
	}

	return results
}

// splitTrim splits a string and trims whitespace from each part.
func splitTrim(s, sep string) []string {
	parts := make([]string, 0)
	for _, p := range split(s, sep) {
		p = trim(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// split is a simple string split.
func split(s, sep string) []string {
	if s == "" {
		return nil
	}
	result := make([]string, 0)
	start := 0
	for i := 0; i < len(s); i++ {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			result = append(result, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	result = append(result, s[start:])
	return result
}

// trim removes leading and trailing whitespace.
func trim(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
