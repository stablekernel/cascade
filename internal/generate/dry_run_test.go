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
// receives dry_run: ${{ github.event.inputs.dry_run == 'true' }} in its with: block.
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

	// dry_run must be forwarded in the with: block, coerced to a real boolean so
	// the reusable callback's boolean dry_run input accepts it on every trigger.
	assert.Contains(t, content,
		"dry_run: ${{ github.event.inputs.dry_run == 'true' }}",
		"supports_dry_run deploy must receive coerced dry_run input")

	// The bare (uncoerced) form must not appear in the with: block; it renders as
	// an empty string on push/schedule/workflow_run and fails boolean validation.
	jobSection := extractJobSection(t, content, "deploy-app:")
	assert.NotContains(t, jobSection,
		"dry_run: ${{ github.event.inputs.dry_run }}\n",
		"supports_dry_run deploy must not forward the uncoerced dry_run value")
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
		"dry_run: ${{",
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
// orchestrate generator passes a coerced dry_run value to a deploy callback that
// declares supports_dry_run: true, given the workflow file declares a dry_run
// boolean input.
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
	// null-safe github.event.inputs accessor, coerced to a real boolean. The bare
	// (uncoerced) form renders empty on push/schedule/workflow_run, which is
	// invalid for a boolean callback input and fails the reusable-workflow dispatch.
	assert.Contains(t, content,
		"dry_run: ${{ github.event.inputs.dry_run == 'true' }}",
		"orchestrate generator must pass coerced dry_run to a supports_dry_run callback")

	// The uncoerced form must not appear in the with: block.
	assert.NotContains(t, content,
		"dry_run: ${{ github.event.inputs.dry_run }}\n",
		"orchestrate generator must not forward the uncoerced dry_run value")
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
		"dry_run: ${{",
		"non-supports_dry_run callback must not receive dry_run in orchestrate")
}

// TestOrchestrate_SupportsDryRun_CoercedNotBare verifies the orchestrate
// generator emits the boolean-coerced dry_run passthrough and never the bare
// uncoerced expression inside a with: block.
func TestOrchestrate_SupportsDryRun_CoercedNotBare(t *testing.T) {
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

	assert.Contains(t, content,
		"dry_run: ${{ github.event.inputs.dry_run == 'true' }}",
		"orchestrate must forward the coerced dry_run value")
	assert.NotContains(t, content,
		"dry_run: ${{ github.event.inputs.dry_run }}\n",
		"orchestrate must not forward the bare dry_run value in a with: block")
}

// TestOrchestrate_DispatchInputs_BooleanCoercion verifies that a boolean
// dispatch_input forwarded into a callback with: block is coerced
// (NAME: ${{ inputs.NAME == 'true' }}) while a string dispatch_input stays bare
// (NAME: ${{ inputs.NAME }}).
func TestOrchestrate_DispatchInputs_BooleanCoercion(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))

	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    inputs:
      environment:
        type: string
      verbose:
        type: boolean
      region:
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
			},
		},
		DispatchInputs: map[string]config.DispatchInput{
			"verbose": {Type: config.DispatchInputTypeBoolean},
			"region":  {Type: config.DispatchInputTypeString},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Boolean dispatch input must be coerced to a real boolean.
	assert.Contains(t, content,
		"verbose: ${{ inputs.verbose == 'true' }}",
		"boolean dispatch_input must be coerced in the callback with: block")
	assert.NotContains(t, content,
		"verbose: ${{ inputs.verbose }}\n",
		"boolean dispatch_input must not be forwarded uncoerced")

	// String dispatch input stays bare.
	assert.Contains(t, content,
		"region: ${{ inputs.region }}",
		"string dispatch_input must be forwarded verbatim")
	assert.NotContains(t, content,
		"region: ${{ inputs.region == 'true' }}",
		"string dispatch_input must not be coerced")
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
