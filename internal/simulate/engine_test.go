package simulate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/promote"
)

// seedManifest writes a minimal manifest with dev populated and uat empty.
func seedManifest(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")

	// dev populated, uat empty, prod present so the dev->uat crossing is a
	// normal sequential deploy and not the publish boundary.
	cicd := &config.CICDFile{
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev", "uat", "prod"},
		},
		State: map[string]*config.EnvState{
			"dev": {
				SHA:         "a1b2c3d4e5f6",
				Version:     "v1.2.0-rc.1",
				CommittedAt: "2026-01-01T10:00:00Z",
				CommittedBy: "seed-user",
			},
			"uat": {},
		},
	}

	wrapper := map[string]interface{}{"ci": cicd}
	data, err := yaml.Marshal(wrapper)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

func TestEngine_Simulate_PromoteDefault(t *testing.T) {
	t.Parallel()

	path := seedManifest(t)

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	result, err := engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)

	assert.Equal(t, "promote", result.ActionName)
	assert.True(t, result.Diff.Changed(), "uat should gain dev's sha/version")

	uat, ok := result.Diff.Env("uat")
	require.True(t, ok)
	assert.True(t, uat.SHA.Changed)
	assert.Equal(t, "a1b2c3d4e5f6", uat.SHA.To)
	assert.True(t, uat.Version.Changed)
	assert.Equal(t, "v1.2.0-rc.1", uat.Version.To)

	require.GreaterOrEqual(t, len(result.Effects), 3)
	assert.Equal(t, "deploy", result.Effects[0].Action)
	assert.Equal(t, "uat", result.Effects[0].Target)
	assert.Equal(t, "write state", result.Effects[1].Action)
	assert.Equal(t, "uat", result.Effects[1].Target)
	// prod is skipped as a no-op in this single-step default promotion.
	last := result.Effects[len(result.Effects)-1]
	assert.Equal(t, DispositionSkip, last.Disposition)
	assert.Equal(t, "prod", last.Target)
}

func TestEngine_Simulate_LeavesOriginalUntouched(t *testing.T) {
	t.Parallel()

	path := seedManifest(t)

	before, err := os.ReadFile(path)
	require.NoError(t, err)

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	_, err = engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)

	after, err := os.ReadFile(path)
	require.NoError(t, err)

	assert.Equal(t, before, after, "the original manifest bytes must be unchanged")
}

func TestEngine_Simulate_PromoteCascade(t *testing.T) {
	t.Parallel()

	// dev populated, uat and prod empty: a cascade to prod carries dev's state
	// through every intermediate hop atomically.
	path := writeManifest(t, &config.CICDFile{
		Config: &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev", "uat", "prod"}},
		State: map[string]*config.EnvState{
			"dev":  {SHA: "a1b2c3d4e5f6", Version: "v1.2.0-rc.1"},
			"uat":  {},
			"prod": {},
		},
	})

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	result, err := engine.Simulate(NewPromoteAction(promote.ModeCascade, "dev-to-prod"))
	require.NoError(t, err)

	assert.True(t, result.Diff.Changed(), "cascade advances the intermediate envs")
	uat, ok := result.Diff.Env("uat")
	require.True(t, ok)
	assert.Equal(t, "a1b2c3d4e5f6", uat.SHA.To)
	require.NotEmpty(t, result.Effects)
}

func TestEngine_Simulate_NoEnvironmentManifest(t *testing.T) {
	t.Parallel()

	// Library/CLI project: no environments declared. The implicit prerelease to
	// release chain publishes, so the release marker is the headline effect.
	path := writeManifest(t, &config.CICDFile{
		Config: &config.TrunkConfig{TrunkBranch: "main"},
		State: map[string]*config.EnvState{
			"prerelease": {SHA: "a1b2c3d4e5f6", Version: "v1.0.0-rc.0"},
		},
	})

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	result, err := engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)

	_, ok := findEffect(result.Effects, "release publish")
	assert.True(t, ok, "a no-environment publish surfaces the publish marker")
}

func TestEngine_Simulate_Deterministic(t *testing.T) {
	t.Parallel()

	path := seedManifest(t)

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	first, err := engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)

	engine2, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)
	second, err := engine2.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)

	assert.Equal(t, first.Diff, second.Diff)
	assert.Equal(t, first.Effects, second.Effects)
}
