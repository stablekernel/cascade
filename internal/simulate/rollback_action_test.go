package simulate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/rollback"
)

// writeManifest marshals a CICDFile under the default manifest key and returns
// its path in a fresh temp dir.
func writeManifest(t *testing.T, cicd *config.CICDFile) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	data, err := yaml.Marshal(map[string]interface{}{"ci": cicd})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

// seedRollbackManifest writes a manifest whose prod env holds a current state
// and a single distinct prior snapshot in the deploy-history ring.
func seedRollbackManifest(t *testing.T) string {
	t.Helper()

	return writeManifest(t, &config.CICDFile{
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev", "uat", "prod"},
		},
		State: map[string]*config.EnvState{
			"prod": {
				SHA:         "newsha0000000",
				Version:     "v2.0.0",
				CommittedAt: "2026-02-01T10:00:00Z",
				CommittedBy: "seed-user",
				Previous: []config.EnvStateSnapshot{
					{SHA: "oldsha0000000", Version: "v1.0.0", CommittedAt: "2026-01-01T10:00:00Z", CommittedBy: "seed-user"},
				},
			},
		},
	})
}

func TestRollbackAction_RevertsToPreviousRingSnapshot(t *testing.T) {
	t.Parallel()

	path := seedRollbackManifest(t)

	// Compute the real package's plan independently so the assertion compares
	// against what rollback itself resolves, not a hand-rolled expectation.
	planner, err := rollback.New(rollback.Options{ConfigPath: path, HistoryReader: emptyHistory{}})
	require.NoError(t, err)
	plan, err := planner.Plan("prod", "", "")
	require.NoError(t, err)
	require.Equal(t, "oldsha0000000", plan.Target.SHA)
	require.Equal(t, "previous-ring", plan.Target.Source)

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	result, err := engine.Simulate(NewRollbackAction("prod", "", ""))
	require.NoError(t, err)

	assert.Equal(t, "rollback", result.ActionName)

	prod, ok := result.Diff.Env("prod")
	require.True(t, ok)
	assert.Equal(t, "newsha0000000", prod.SHA.From)
	assert.Equal(t, "oldsha0000000", prod.SHA.To)
	assert.Equal(t, "v2.0.0", prod.Version.From)
	assert.Equal(t, "v1.0.0", prod.Version.To)
	assert.True(t, prod.Divergence.Changed, "a rollback marks the env diverged off trunk")
	assert.Equal(t, "yes", prod.Divergence.To)

	require.Len(t, result.Effects, 2)
	assert.Equal(t, DispositionRun, result.Effects[0].Disposition)
	assert.Equal(t, "revert", result.Effects[0].Action)
	assert.Equal(t, "prod", result.Effects[0].Target)
	assert.Equal(t, "write state", result.Effects[1].Action)
}

func TestRollbackAction_NoOpWhenAlreadyAtTarget(t *testing.T) {
	t.Parallel()

	// No distinct prior: the only snapshot equals the current SHA, so the
	// resolved target is the current state and the rollback is a no-op.
	path := writeManifest(t, &config.CICDFile{
		Config: &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev", "prod"}},
		State: map[string]*config.EnvState{
			"prod": {
				SHA:     "samesha000000",
				Version: "v1.0.0",
				Previous: []config.EnvStateSnapshot{
					{SHA: "samesha000000", Version: "v1.0.0"},
				},
			},
		},
	})

	engine, err := NewEngine(path)
	require.NoError(t, err)

	// With no distinct prior the default resolution errors; target an explicit
	// SHA equal to current to exercise the no-op path deterministically.
	result, err := engine.Simulate(NewRollbackAction("prod", "samesha000000", ""))
	require.NoError(t, err)

	assert.False(t, result.Diff.Changed(), "a no-op rollback changes nothing")
	require.Len(t, result.Effects, 1)
	assert.Equal(t, DispositionSkip, result.Effects[0].Disposition)
	assert.Equal(t, "prod", result.Effects[0].Target)
}

func TestRollbackAction_LeavesOriginalUntouched(t *testing.T) {
	t.Parallel()

	path := seedRollbackManifest(t)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)
	_, err = engine.Simulate(NewRollbackAction("prod", "", ""))
	require.NoError(t, err)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the original manifest bytes must be unchanged")
}

func TestRollbackAction_UnknownEnvErrors(t *testing.T) {
	t.Parallel()

	path := seedRollbackManifest(t)
	engine, err := NewEngine(path)
	require.NoError(t, err)

	_, err = engine.Simulate(NewRollbackAction("nope", "", ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown environment")
}
