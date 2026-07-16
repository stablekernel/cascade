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

// TestOrchestrateInputExpressionPassthrough verifies that operator-authored
// inputs survive into the orchestrate callback with: block. Vars.*
// passthrough expressions emit verbatim, literals emit as-is, and matrix.*
// placeholders are not treated as passthrough.
func TestOrchestrateInputExpressionPassthrough(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))

	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    inputs:
      bucket:
        type: string
      region:
        type: string
      environment:
        type: string
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/deploy.yaml"), []byte(deployWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy.yaml",
				Triggers: []string{"app/**"},
				Inputs: map[string]interface{}{
					"bucket": "${{ vars.DEPLOY_BUCKET }}",
					"region": "us-east-1",
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// The vars expression survives into the deploy callback with:, wrapped in
	// single quotes (transparent to GHA expression evaluation).
	assert.Contains(t, result, "bucket: '${{ vars.DEPLOY_BUCKET }}'")
	// Literal survives unchanged, single-quoted.
	assert.Contains(t, result, "region: 'us-east-1'")
}

// TestOrchestrateStateReferenceResolves verifies a ${{ state.<env>.<field> }}
// input reference resolves at generation time from the manifest state on the
// orchestrate path.
func TestOrchestrateStateReferenceResolves(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))

	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    inputs:
      previous_sha:
        type: string
      environment:
        type: string
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/deploy.yaml"), []byte(deployWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy.yaml",
				Triggers: []string{"app/**"},
				Inputs: map[string]interface{}{
					"previous_sha": "${{ state.prod.sha }}",
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	gen.SetState(map[string]*config.EnvState{
		"prod": {SHA: "abc123def"},
	})
	result, err := gen.Generate()
	require.NoError(t, err)

	// State ref resolved to the literal prior SHA, not passed through.
	assert.Contains(t, result, "previous_sha: 'abc123def'")
	assert.NotContains(t, result, "state.prod.sha")
}

// TestPromoteVarsExpressionPassthrough verifies a vars.* input is emitted
// verbatim in the promote deploy job with: block and is NOT trapped as a dead
// literal inside the matrix JSON.
func TestPromoteVarsExpressionPassthrough(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
				Inputs: map[string]interface{}{
					"environment": "${{ matrix.environment }}",
					"bucket":      "${{ vars.DEPLOY_BUCKET }}",
					"region":      "us-east-1",
				},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	// The vars expression is emitted directly in the deploy job with: block.
	assert.Contains(t, result, "bucket: ${{ vars.DEPLOY_BUCKET }}")

	// The vars expression must NOT be serialized into the matrix JSON payload
	// (where it would become a dead literal).
	assert.NotContains(t, result, `"bucket":"${{ vars.DEPLOY_BUCKET }}"`)

	// matrix.* substitution and literals still flow through the matrix.
	assert.Contains(t, result, "environment: ${{ matrix.environment }}")
	assert.Contains(t, result, "region: ${{ matrix.region }}")
}

// TestPromoteStateReferenceResolves verifies a ${{ state.<env>.<field> }} input
// resolves at generation time and is baked into the per-env matrix payload.
func TestPromoteStateReferenceResolves(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
				Inputs: map[string]interface{}{
					"environment":  "${{ matrix.environment }}",
					"previous_sha": "${{ state.prod.sha }}",
				},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	gen.SetState(map[string]*config.EnvState{
		"prod": {SHA: "priorsha9"},
	})
	result, err := gen.Generate()
	require.NoError(t, err)

	// State ref resolved into the matrix env_inputs payload for prod.
	assert.Contains(t, result, `"prod":{"previous_sha":"priorsha9"}`)
	// The unresolved state expression must not survive anywhere.
	assert.NotContains(t, result, "state.prod.sha")
}

// TestPromoteLiteralInputsUnchanged guards that plain literal inputs keep their
// existing matrix-routed behavior.
func TestPromoteLiteralInputsUnchanged(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
				Inputs: map[string]interface{}{
					"cluster": "dev-eks",
				},
				EnvInputs: map[string]map[string]interface{}{
					"prod": {"cluster": "prod-eks"},
				},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, `DEFAULT_INPUTS_APP: '{"cluster":"dev-eks"}'`)
	assert.Contains(t, result, `"prod":{"cluster":"prod-eks"}`)
	assert.Contains(t, result, "cluster: ${{ matrix.cluster }}")
}

// TestValidateWarnsUnwrappedExpression verifies cascade validate flags a bare
// vars.X value the operator likely meant to wrap in ${{ }}.
func TestValidateWarnsUnwrappedExpression(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/deploy.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy.yaml",
				Triggers: []string{"app/**"},
				Inputs: map[string]interface{}{
					"bucket": "vars.DEPLOY_BUCKET", // missing ${{ }} wrapper
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	warnings := gen.Validate()

	found := false
	for _, w := range warnings {
		if strings.Contains(w, "vars.DEPLOY_BUCKET") && strings.Contains(w, "${{ }}") {
			found = true
		}
	}
	assert.True(t, found, "expected an unwrapped-expression warning, got: %v", warnings)
}
