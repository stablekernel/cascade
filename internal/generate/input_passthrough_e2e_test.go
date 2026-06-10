package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInputExpressionPassthroughE2E exercises the full manifest -> parse ->
// state-thread -> generate path the CLI uses, asserting the generated promote
// workflow carries live ${{ vars.* }} expressions (issue #34) and resolves
// ${{ state.<env>.<field> }} prior-state references (issue #21) from the
// manifest state block.
func TestInputExpressionPassthroughE2E(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	manifest := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    deploys:
      - name: app
        workflow: .github/workflows/deploy-app.yaml
        inputs:
          environment: ${{ matrix.environment }}
          bucket: ${{ vars.DEPLOY_BUCKET }}
          previous_sha: ${{ state.prod.sha }}
          region: us-east-1
  state:
    prod:
      sha: cafef00d
      version: v3.1.0
`
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0644))

	// Mirror the CLI flow: parse config + parse full manifest for state.
	cfg, err := config.ParseWithKey(manifestPath, "ci")
	require.NoError(t, err)

	full, err := config.ParseManifestFile(manifestPath, "ci")
	require.NoError(t, err)

	gen := NewPromoteGenerator(cfg, tmpDir)
	gen.SetState(full.State)

	result, err := gen.Generate()
	require.NoError(t, err)

	// #34: vars expression survives verbatim into the deploy callback with:.
	assert.Contains(t, result, "bucket: ${{ vars.DEPLOY_BUCKET }}")
	// And is NOT trapped as a dead literal inside the matrix JSON.
	assert.NotContains(t, result, `"bucket":"${{ vars.DEPLOY_BUCKET }}"`)

	// #21: prior-state reference resolved from manifest state into the matrix.
	assert.Contains(t, result, `"prod":{"previous_sha":"cafef00d"}`)
	assert.NotContains(t, result, "state.prod.sha")

	// Existing matrix.* substitution and literal routing are preserved.
	assert.Contains(t, result, "environment: ${{ matrix.environment }}")
	assert.Contains(t, result, "region: ${{ matrix.region }}")
}
