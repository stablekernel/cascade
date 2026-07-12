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

// extractJobBlock returns the YAML text for a single job, starting at the line
// "  <jobID>:\n" and ending just before the next top-level job line or EOF.
func extractJobBlock(yaml, jobID string) string {
	marker := "  " + jobID + ":\n"
	start := strings.Index(yaml, marker)
	if start < 0 {
		return ""
	}
	rest := yaml[start+len(marker):]
	// Find the next line that starts a new top-level key ("  <word>:" at column 2).
	lines := strings.Split(rest, "\n")
	var keep []string
	for _, l := range lines {
		// A new job starts with exactly two spaces followed by a non-space, non-comment char and a colon somewhere.
		if len(l) >= 3 && l[0] == ' ' && l[1] == ' ' && l[2] != ' ' && strings.Contains(l, ":") {
			break
		}
		keep = append(keep, l)
	}
	return marker + strings.Join(keep, "\n")
}

// TestMatrixRetry_RetryJobCarriesStrategy asserts that when a build callback
// has both matrix: and retries > 0, each generated retry job also emits a
// strategy: block with the same matrix dimensions, max-parallel, and
// fail-fast as the primary job. Without the strategy block the retry job runs
// in a matrix-less context and ${{ matrix.* }} expressions in the reusable
// workflow's inputs resolve to empty strings.
func TestMatrixRetry_RetryJobCarriesStrategy(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))

	// Workflow that consumes matrix dimensions as inputs.
	buildWorkflow := `on:
  workflow_call:
    inputs:
      os:
        type: string
      arch:
        type: string
`
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/build.yaml"),
		[]byte(buildWorkflow),
		0644,
	))

	ptrFalse := false
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				Retries:  2,
				Matrix: &config.MatrixConfig{
					Dimensions:  map[string][]string{"arch": {"amd64", "arm64"}, "os": {"linux", "darwin"}},
					MaxParallel: 4,
					FailFast:    &ptrFalse,
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Primary job must still have the strategy block.
	primaryBlock := extractJobBlock(result, "build-app")
	require.NotEmpty(t, primaryBlock, "primary build-app job not found")
	assert.Contains(t, primaryBlock, "    strategy:\n", "primary job must emit strategy block")

	// Retry 1 must carry strategy.
	retry1Block := extractJobBlock(result, "build-app-retry-1")
	require.NotEmpty(t, retry1Block, "retry-1 job not found in generated output")
	assert.Contains(t, retry1Block, "    strategy:\n", "retry-1 job must emit strategy block")
	assert.Contains(t, retry1Block, "      matrix:\n", "retry-1 must include matrix key")
	assert.Contains(t, retry1Block, "      max-parallel: 4", "retry-1 must propagate max-parallel")
	assert.Contains(t, retry1Block, "      fail-fast: false", "retry-1 must propagate fail-fast")
	assert.Contains(t, retry1Block, "      os: ${{ matrix.os }}", "retry-1 must forward os dimension in with:")
	assert.Contains(t, retry1Block, "      arch: ${{ matrix.arch }}", "retry-1 must forward arch dimension in with:")

	// Retry 2 must also carry strategy.
	retry2Block := extractJobBlock(result, "build-app-retry-2")
	require.NotEmpty(t, retry2Block, "retry-2 job not found in generated output")
	assert.Contains(t, retry2Block, "    strategy:\n", "retry-2 job must emit strategy block")
	assert.Contains(t, retry2Block, "      matrix:\n", "retry-2 must include matrix key")
	assert.Contains(t, retry2Block, "      max-parallel: 4", "retry-2 must propagate max-parallel")
	assert.Contains(t, retry2Block, "      fail-fast: false", "retry-2 must propagate fail-fast")
	assert.Contains(t, retry2Block, "      os: ${{ matrix.os }}", "retry-2 must forward os dimension in with:")
	assert.Contains(t, retry2Block, "      arch: ${{ matrix.arch }}", "retry-2 must forward arch dimension in with:")
}

// TestMatrixRetry_MatrixWithoutRetries asserts that a matrix build with no
// retries generates no retry jobs at all (non-breaking for the common case).
func TestMatrixRetry_MatrixWithoutRetries(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/build.yaml"),
		[]byte("on:\n  workflow_call:\n"),
		0644,
	))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				// Retries intentionally omitted (zero).
				Matrix: &config.MatrixConfig{
					Dimensions: map[string][]string{"os": {"linux", "darwin"}},
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.NotContains(t, result, "build-app-retry-1", "no retries configured → no retry job")
	primaryBlock := extractJobBlock(result, "build-app")
	assert.Contains(t, primaryBlock, "    strategy:\n", "primary job must still have strategy block")
}

// TestMatrixRetry_RetriesWithoutMatrix asserts that a build with retries but no
// matrix generates retry jobs without a strategy block (non-breaking for the
// existing retry-only path).
func TestMatrixRetry_RetriesWithoutMatrix(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/build.yaml"),
		[]byte("on:\n  workflow_call:\n"),
		0644,
	))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				Retries:  1,
				// Matrix intentionally omitted.
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "build-app-retry-1", "retry job must be generated")
	retryBlock := extractJobBlock(result, "build-app-retry-1")
	assert.NotContains(t, retryBlock, "strategy:", "no matrix → no strategy block on retry job")
}
