package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dispatchOnlyTestConfig builds a minimal orchestrate config with a single
// build callback and a triggers paths filter, optionally dispatch-only.
func dispatchOnlyTestConfig(t *testing.T, dispatchOnly bool) (*config.TrunkConfig, string) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}
	if dispatchOnly {
		cfg.ReleaseTrigger = config.ReleaseTriggerDispatch
	}
	return cfg, tmpDir
}

// TestGenerator_DefaultEmitsPushTrigger asserts the generated orchestrate
// workflow keeps its push: trigger (and paths filter) by default, so example
// repos that do not opt in continue to run orchestrate on every trunk merge.
func TestGenerator_DefaultEmitsPushTrigger(t *testing.T) {
	cfg, tmpDir := dispatchOnlyTestConfig(t, false)

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "  push:\n", "default orchestrate must keep the push: trigger")
	assert.Contains(t, result, "    branches: [main]\n", "default orchestrate must push on the trunk branch")
	assert.Contains(t, result, "    paths:\n", "default orchestrate must keep its paths filter")
	assert.Contains(t, result, "  workflow_dispatch:\n", "default orchestrate must keep workflow_dispatch")
}

// TestGenerator_DispatchOnlyOmitsPushTrigger asserts that when
// release_trigger: dispatch is set, the generated orchestrate workflow omits
// its push: trigger entirely (becoming workflow_dispatch-driven) while keeping
// the dispatch trigger and its dry_run input intact.
func TestGenerator_DispatchOnlyOmitsPushTrigger(t *testing.T) {
	cfg, tmpDir := dispatchOnlyTestConfig(t, true)

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.NotContains(t, result, "  push:\n", "dispatch-only orchestrate must omit the push: trigger")
	assert.NotContains(t, result, "    branches: [main]\n", "dispatch-only orchestrate must not declare push branches")
	assert.NotContains(t, result, "    paths:\n", "dispatch-only orchestrate must not declare a push paths filter")
	// The dispatch trigger and its dry_run input survive.
	assert.Contains(t, result, "  workflow_dispatch:\n", "dispatch-only orchestrate must keep workflow_dispatch")
	assert.Contains(t, result, "      dry_run:\n", "dispatch-only orchestrate must keep the dry_run input")
}
