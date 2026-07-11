package generate

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGeneratedWorkflow_PreflightCommandValid validates the preflight command
// uses valid flags.
// This test would have caught the bug where the promote generator was calling
// `cascade promote --mode` but --mode only exists on the preflight subcommand.
func TestGeneratedWorkflow_PreflightCommandValid(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// The preflight command should use the preflight subcommand
	assert.Contains(t, content, "cascade promote preflight",
		"preflight should use 'cascade promote preflight' subcommand")

	// Preflight should have --mode flag on the preflight subcommand
	preflightRegex := regexp.MustCompile(`cascade promote preflight[^|]*--mode`)
	assert.True(t, preflightRegex.MatchString(content),
		"preflight command should have --mode flag")

	// Should NOT have --mode on root promote command
	// This was the bug: `cascade promote --mode` instead of `cascade promote preflight --mode`
	invalidPattern := regexp.MustCompile(`cascade promote\s+\\?\s*\n\s*--mode`)
	assert.False(t, invalidPattern.MatchString(content),
		"should NOT call 'cascade promote --mode' directly (--mode is on preflight subcommand)")
}

// TestGeneratedWorkflow_FinalizeCommandValid validates the finalize command
// uses valid flags.
func TestGeneratedWorkflow_FinalizeCommandValid(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// The finalize command should use the finalize subcommand
	assert.Contains(t, content, "cascade promote finalize",
		"finalize should use 'cascade promote finalize' subcommand")

	// Finalize should have --promotion-result flag
	assert.Contains(t, content, "--promotion-result",
		"finalize command should have --promotion-result flag")

	// Finalize should have --commit-push flag
	assert.Contains(t, content, "--commit-push",
		"finalize command should have --commit-push flag")
}

// TestGeneratedWorkflow_PromoteJobNoDirectCLICall validates that the Promote job
// does NOT try to call a non-existent CLI command. The Promote job should just
// do validation/echo since preflight already does the planning and finalize does
// the state update.
func TestGeneratedWorkflow_PromoteJobNoDirectCLICall(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "uat", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Extract the promote job section
	promoteJobMatch := regexp.MustCompile(`(?s)  promote:\s*\n.*?(?:\n  \w+:|$)`).FindString(content)
	require.NotEmpty(t, promoteJobMatch, "should find promote job in workflow")

	// The promote job should NOT contain a broken CLI call like `cascade promote --mode`
	// It should either call a valid subcommand or just echo validation info
	invalidPromoteCall := regexp.MustCompile(`cascade promote\s+\\?\s*\n\s+--mode`)
	assert.False(t, invalidPromoteCall.MatchString(promoteJobMatch),
		"promote job should NOT call 'cascade promote --mode' (invalid command)")

	// The promote job can call setup-cli but the actual step should be validation-only
	// or use a valid subcommand
	if strings.Contains(promoteJobMatch, "cascade promote") {
		// If it does call cascade promote, it should be a valid subcommand
		validSubcommands := regexp.MustCompile(`cascade promote (preflight|finalize|run)`)
		assert.True(t, validSubcommands.MatchString(promoteJobMatch),
			"if promote job calls CLI, it must use a valid subcommand (preflight, finalize, or run)")
	}
}

// TestGeneratedWorkflow_ValidCLISubcommands validates that only valid CLI
// subcommands are used in generated workflows.
func TestGeneratedWorkflow_ValidCLISubcommands(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "uat", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Valid top-level subcommands
	validSubcommands := []string{
		"promote",
		"orchestrate",
		"generate-workflow",
		"generate-changelog",
		"version",
		"manage-release", // This is used in scripts, not the CLI
	}

	// Extract CLI invocations: `cascade <subcommand>`.
	// Only match actual CLI calls, not prose. "cascade" is also a domain term
	// (the cascade promotion mode), so comment lines and YAML description text
	// mention it as a noun (e.g. "cascade from source to target", "cascade
	// target"). Skip comment/description lines and only inspect shell command
	// invocations.
	cliPattern := regexp.MustCompile(`cascade\s+(\w[\w-]*)`)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip YAML/shell comments and human-readable description fields, which
		// reference the cascade promotion mode as a noun rather than the CLI.
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "description:") {
			continue
		}
		for _, match := range cliPattern.FindAllStringSubmatch(line, -1) {
			if len(match) < 2 {
				continue
			}
			subcommand := match[1]
			found := false
			for _, valid := range validSubcommands {
				if subcommand == valid {
					found = true
					break
				}
			}
			// Note: manage-release is used in the workflow but it's a script/action, not CLI
			if !found && subcommand != "manage-release" {
				t.Errorf("potentially invalid CLI subcommand: %s", subcommand)
			}
		}
	}
}

// TestPromoteJobStepName verifies the promote job step has the correct name
func TestPromoteJobStepName(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// The step should be named "Validate Promotion" not "Execute Promotion"
	// because we're not executing a CLI command anymore
	assert.Contains(t, content, "name: Validate Promotion",
		"promote job step should be named 'Validate Promotion'")
	assert.NotContains(t, content, "name: Execute Promotion",
		"promote job should NOT have 'Execute Promotion' step")
}

// TestGeneratedWorkflow_NoOrphanedFlags ensures no flags are used without
// their parent command.
func TestGeneratedWorkflow_NoOrphanedFlags(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "uat", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Look for orphaned --mode flag (not preceded by preflight)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(line, "--mode") && !strings.Contains(line, "promotion_mode") {
			// Check previous lines to ensure it's part of a preflight command
			foundPreflight := false
			for j := max(0, i-5); j < i; j++ {
				if strings.Contains(lines[j], "preflight") {
					foundPreflight = true
					break
				}
			}
			// --mode in workflow_dispatch input is okay
			if !foundPreflight && !strings.Contains(line, "mode:") && !strings.Contains(line, "\"$PROMOTION_MODE\"") {
				assert.True(t, foundPreflight,
					"--mode flag at line %d should be part of a preflight command: %s", i+1, line)
			}
		}
	}
}

// TestGeneratedWorkflow_PromotePreflightStructure validates the structure of
// the generated preflight command matches what we expect.
func TestGeneratedWorkflow_PromotePreflightStructure(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "uat", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Verify the preflight step structure
	assert.Contains(t, content, "name: Run Preflight",
		"should have 'Run Preflight' step")
	assert.Contains(t, content, "id: preflight",
		"preflight step should have id: preflight")

	// Verify the preflight job contains the CLI command
	assert.Contains(t, content, "cascade promote preflight",
		"preflight job should call cascade promote preflight")

	// Extract the preflight job section and verify flags are present
	// The workflow uses multiline commands with backslash continuations
	preflightJobRegex := regexp.MustCompile(`(?s)preflight:\s*\n.*?(?:\n  \w+:|$)`)
	preflightJob := preflightJobRegex.FindString(content)
	require.NotEmpty(t, preflightJob, "should find preflight job")

	// Check flags appear in the preflight job section
	assert.Contains(t, preflightJob, "--mode",
		"preflight job should have --mode flag")
	assert.Contains(t, preflightJob, "--config",
		"preflight job should have --config flag")
	assert.Contains(t, preflightJob, "--gha-output",
		"preflight job should have --gha-output flag")
}

// TestGeneratedWorkflow_PromoteFinalizeStructure validates the structure of
// the generated finalize command matches what we expect.
func TestGeneratedWorkflow_PromoteFinalizeStructure(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "uat", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Verify the finalize step exists
	assert.Contains(t, content, "name: Finalize Promotion",
		"should have 'Finalize Promotion' step")

	// Verify the finalize job contains the CLI command
	assert.Contains(t, content, "cascade promote finalize",
		"finalize job should call cascade promote finalize")

	// Extract the finalize job section and verify flags are present
	// The workflow uses multiline commands with backslash continuations
	finalizeJobRegex := regexp.MustCompile(`(?s)finalize:\s*\n.*$`)
	finalizeJob := finalizeJobRegex.FindString(content)
	require.NotEmpty(t, finalizeJob, "should find finalize job")

	// Check flags appear in the finalize job section
	assert.Contains(t, finalizeJob, "--config",
		"finalize job should have --config flag")
	assert.Contains(t, finalizeJob, "--promotion-result",
		"finalize job should have --promotion-result flag")
	assert.Contains(t, finalizeJob, "--commit-push",
		"finalize job should have --commit-push flag")
	assert.Contains(t, finalizeJob, "--repo",
		"finalize job should have --repo flag")
	assert.Contains(t, finalizeJob, "--run-id",
		"finalize job should have --run-id flag")
}

// TestGeneratedWorkflow_PreflightNoInvalidFlags validates the preflight command
// does NOT use flags that don't exist on the CLI.
// This test catches bugs like using --target when the CLI only has --mode.
func TestGeneratedWorkflow_PreflightNoInvalidFlags(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "uat", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Extract the preflight run block (the actual CLI command)
	// This regex captures the run: block for the preflight step
	preflightRunRegex := regexp.MustCompile(`(?s)id: preflight\s*\n\s*env:.*?run: \|\s*\n(.*?)(?:\n\s+-\s+name:|\n\s+[a-z]+:)`)
	match := preflightRunRegex.FindStringSubmatch(content)
	require.NotEmpty(t, match, "should find preflight run block")

	preflightCmd := match[1]

	// List of flags that DO NOT exist on cascade promote preflight
	// This is the key test - catches the --target bug
	invalidFlags := []string{
		"--target",  // This flag does not exist - mode handles both default and cascade targets
		"--cascade", // Not a valid flag
		"--env",     // Not a valid flag on preflight
	}

	for _, invalidFlag := range invalidFlags {
		assert.NotContains(t, preflightCmd, invalidFlag,
			"preflight command should NOT use %s flag (doesn't exist on CLI)", invalidFlag)
	}

	// Also verify the environment variables don't reference non-existent inputs
	assert.NotContains(t, content, "github.event.inputs.target",
		"workflow should not reference inputs.target (no such input)")
}

// TestGeneratedWorkflow_ChangelogCommandValid validates the changelog command
// uses valid flags.
func TestGeneratedWorkflow_ChangelogCommandValid(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "uat", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
		Changelog: &config.ChangelogConfig{
			Contributors: boolPtr(true),
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Verify changelog step exists
	assert.Contains(t, content, "name: Generate Changelog",
		"should have 'Generate Changelog' step")

	// Verify the changelog command with expected flags
	assert.Contains(t, content, "cascade generate-changelog",
		"should call cascade generate-changelog")

	// Check that changelog command has expected flags
	// Match multiline command (with backslash continuations)
	changelogCmdRegex := regexp.MustCompile(`cascade generate-changelog[^\n]*(?:\\\n[^\n]*)*`)
	changelogMatch := changelogCmdRegex.FindString(content)
	require.NotEmpty(t, changelogMatch, "should find generate-changelog command")

	assert.Contains(t, changelogMatch, "--base-sha",
		"changelog command should have --base-sha flag")
	assert.Contains(t, changelogMatch, "--head-sha",
		"changelog command should have --head-sha flag")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
