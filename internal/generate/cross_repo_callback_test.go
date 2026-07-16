package generate

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCrossRepoBuildCallback_GeneratesWithoutLocalRead reproduces the
// stablekernel/cascade-example-primary case: a build callback whose workflow:
// points at a reusable workflow in another repository
// (org/repo/.github/workflows/file.yaml@ref). The generator must NOT try to
// read that workflow from local disk (it does not exist locally and the path
// ends in a literal "@ref"), and it must still emit a correct caller job with a
// with: block carrying the standard callback-contract inputs.
func TestCrossRepoBuildCallback_GeneratesWithoutLocalRead(t *testing.T) {
	// baseDir contains the LOCAL build callback only. The cross-repo callback
	// has no local file on purpose: a pre-fix generator hard-fails reading it.
	baseDir := t.TempDir()
	createMockWorkflow(t, baseDir, ".github/workflows/build-app.yaml")

	const crossRepoWorkflow = "stablekernel/cascade-example-artifact-a/.github/workflows/build-shared.yaml@main"

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("staging", "prod"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build-app.yaml"},
			{Name: "sharedlib", Workflow: crossRepoWorkflow},
		},
	}

	gen := NewGenerator(cfg, baseDir)
	content, err := gen.Generate()

	// (a) Generation must succeed: no local-read error for the @ref workflow.
	require.NoError(t, err, "cross-repo callback must not trigger a local file read")

	// The cross-repo caller job must reference the external workflow verbatim.
	assert.Contains(t, content, "uses: "+crossRepoWorkflow,
		"cross-repo callback must be wired as a uses: caller to the external workflow")

	// (b) The cross-repo caller job must carry a with: block with the standard
	// contract inputs (environment + sha), matching the live fleet output.
	job := extractJobBlock(content, "build-sharedlib")
	require.NotEmpty(t, job, "build-sharedlib job not found in generated content")
	assert.Contains(t, job, "with:",
		"cross-repo caller job must emit a with: block")
	assert.Contains(t, job, "environment: ${{ github.event.inputs.environment || 'staging' }}",
		"cross-repo caller must pass the environment contract input")
	assert.Contains(t, job, "sha: ${{ needs.setup.outputs.head_sha }}",
		"cross-repo caller must pass the sha contract input")

	// (c) The cross-repo build's artifact_id output must still flow downstream
	// (state capture / finalize), matching the live fleet's committed output.
	assert.Contains(t, content, "needs.build-sharedlib.outputs.artifact_id",
		"cross-repo build's artifact_id output must be wired downstream")
}

// TestCrossRepoCallback_OperatorInputsPassThrough verifies that operator-declared
// manifest inputs on a cross-repo callback are still emitted in the caller's
// with: block, even though the external workflow cannot be parsed locally.
func TestCrossRepoCallback_OperatorInputsPassThrough(t *testing.T) {
	baseDir := t.TempDir()

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("staging"),
		Builds: []config.BuildConfig{
			{
				Name:     "sharedlib",
				Workflow: "stablekernel/cascade-example-artifact-a/.github/workflows/build-shared.yaml@main",
				Inputs:   map[string]interface{}{"version": "v1.2.3"},
			},
		},
	}

	gen := NewGenerator(cfg, baseDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	job := extractJobBlock(content, "build-sharedlib")
	require.NotEmpty(t, job, "build-sharedlib job not found")
	assert.Contains(t, job, "version: 'v1.2.3'",
		"operator-declared manifest input must be passed to the cross-repo caller")
}
