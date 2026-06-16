package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPromote_SupportsDryRun_SingleDeploy verifies that a deploy callback with
// supports_dry_run: true is invoked (not skipped) during a dry-run promote and
// receives dry_run: ${{ github.event.inputs.dry_run }} in its with: block.
func TestPromote_SupportsDryRun_SingleDeploy(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:           "app",
				Workflow:       ".github/workflows/deploy.yaml",
				SupportsDryRun: true,
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// The job must NOT gate on dry_run != 'true'; the callback runs regardless.
	assert.NotContains(t, content,
		"github.event.inputs.dry_run != 'true' && contains(fromJSON(needs.preflight.outputs.deploys_to_run), 'app')",
		"supports_dry_run deploy should not be skipped by dry_run guard")

	// The job's if: condition should only check deploys_to_run (no dry_run gate).
	assert.Contains(t, content,
		"contains(fromJSON(needs.preflight.outputs.deploys_to_run), 'app')",
		"supports_dry_run deploy must still be gated on deploys_to_run")

	// dry_run must be forwarded in the with: block.
	assert.Contains(t, content,
		"dry_run: ${{ github.event.inputs.dry_run }}",
		"supports_dry_run deploy must receive dry_run input")
}

// TestPromote_NoSupportsDryRun_SingleDeploy verifies that a deploy callback
// without supports_dry_run keeps the existing skip behavior (dry_run != 'true').
func TestPromote_NoSupportsDryRun_SingleDeploy(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy.yaml",
				// SupportsDryRun intentionally omitted (false).
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Standard deploy must still be guarded by the dry_run condition.
	assert.Contains(t, content,
		"github.event.inputs.dry_run != 'true' && contains(fromJSON(needs.preflight.outputs.deploys_to_run), 'app')",
		"non-supports_dry_run deploy must keep the dry_run skip guard")

	// dry_run must NOT appear in the with: block for a non-opting callback.
	// (Check within the deploy-app job block only to avoid false matches.)
	jobSection := extractJobSection(t, content, "deploy-app:")
	assert.NotContains(t, jobSection,
		"dry_run: ${{ github.event.inputs.dry_run }}",
		"non-supports_dry_run deploy must not receive dry_run input")
}

// TestPromote_SupportsDryRun_ProdDeploy verifies the same semantics for the
// cascade-mode prod deploy job (deploy-<name>-prod).
func TestPromote_SupportsDryRun_ProdDeploy(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:           "app",
				Workflow:       ".github/workflows/deploy.yaml",
				SupportsDryRun: true,
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Prod deploy job must not gate on dry_run != 'true'.
	assert.NotContains(t, content,
		"github.event.inputs.dry_run != 'true' && needs.preflight.outputs.has_prod_deployment == 'true'",
		"supports_dry_run prod deploy should not be skipped by dry_run guard")

	assert.Contains(t, content,
		"needs.preflight.outputs.has_prod_deployment == 'true'",
		"prod deploy must still gate on has_prod_deployment")
}

// TestPromote_SupportsDryRun_NormalRunUnaffected verifies that in a non-dry-run
// promote, a supports_dry_run callback is still invoked (its if: doesn't exclude
// normal runs) and that dry_run: false would be forwarded.
func TestPromote_SupportsDryRun_NormalRunUnaffected(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:           "app",
				Workflow:       ".github/workflows/deploy.yaml",
				SupportsDryRun: true,
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// The if: condition must not contain 'false' or any expression that would
	// block a normal (non-dry-run) promote. Specifically, it must not add extra
	// conditions that exclude runs when dry_run is '' or 'false'.
	jobSection := extractJobSection(t, content, "deploy-app:")
	assert.NotContains(t, jobSection, "dry_run != 'true'",
		"supports_dry_run deploy if: must not block normal (non-dry-run) runs")
}

// TestOrchestrate_SupportsDryRun_WithInputPassthrough verifies that the
// orchestrate generator passes dry_run: ${{ inputs.dry_run }} to a deploy
// callback that declares supports_dry_run: true, given the workflow file
// declares a dry_run input.
func TestOrchestrate_SupportsDryRun_WithInputPassthrough(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))

	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    inputs:
      environment:
        type: string
      dry_run:
        type: boolean
`
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/deploy.yaml"),
		[]byte(deployWorkflow), 0644,
	))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Deploys: []config.DeployConfig{
			{
				Name:           "app",
				Workflow:       ".github/workflows/deploy.yaml",
				SupportsDryRun: true,
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// dry_run must be forwarded in the with: block to the deploy job using the
	// null-safe github.event.inputs accessor. The bare inputs.dry_run form renders
	// empty on push/schedule/workflow_run, which is invalid for a boolean callback
	// input and fails the reusable-workflow dispatch.
	assert.Contains(t, content,
		"dry_run: ${{ github.event.inputs.dry_run }}",
		"orchestrate generator must pass dry_run to a supports_dry_run callback")
}

// TestOrchestrate_NoSupportsDryRun_NoDryRunInput verifies that a deploy
// callback without supports_dry_run does NOT receive a dry_run input in the
// orchestrate workflow, even if dry_run is a dispatch input.
func TestOrchestrate_NoSupportsDryRun_NoDryRunInput(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))

	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    inputs:
      environment:
        type: string
`
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/deploy.yaml"),
		[]byte(deployWorkflow), 0644,
	))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy.yaml",
				// SupportsDryRun intentionally omitted.
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	deploySection := extractJobSection(t, content, "deploy-app:")
	assert.NotContains(t, deploySection,
		"dry_run: ${{ github.event.inputs.dry_run }}",
		"non-supports_dry_run callback must not receive dry_run in orchestrate")
}

// extractJobSection returns the YAML lines for a named job block, stopping at
// the next top-level job or end of file. Used to scope assertions to one job.
func extractJobSection(t *testing.T, content, jobKey string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Top-level job keys are indented with exactly two spaces.
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
			key := strings.TrimSpace(strings.SplitN(line, ":", 2)[0]) + ":"
			if key == jobKey {
				start = i
			} else if start >= 0 {
				return strings.Join(lines[start:i], "\n")
			}
		}
	}
	if start >= 0 {
		return strings.Join(lines[start:], "\n")
	}
	return ""
}
