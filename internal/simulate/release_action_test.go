package simulate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

func findEffect(effects []Effect, action string) (Effect, bool) {
	for _, e := range effects {
		if e.Action == action {
			return e, true
		}
	}
	return Effect{}, false
}

func TestReleaseAction_SurfacesPrereleaseMarker(t *testing.T) {
	t.Parallel()

	// dev populated, uat (the prerelease env) empty: the default crossing into
	// uat sets ReleaseAction=prerelease.
	path := writeManifest(t, &config.CICDFile{
		Config: &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev", "uat", "prod"}},
		State: map[string]*config.EnvState{
			"dev": {SHA: "a1b2c3d4e5f6", Version: "v1.0.0-rc.0"},
			"uat": {},
		},
	})

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	result, err := engine.Simulate(NewReleaseAction())
	require.NoError(t, err)

	assert.Equal(t, "release", result.ActionName)

	marker, ok := findEffect(result.Effects, "release prerelease")
	require.True(t, ok, "the prerelease marker must appear as its own effect")
	assert.Equal(t, "v1.0.0", marker.Target, "the target is the semver being marked")
	assert.Equal(t, DispositionRun, marker.Disposition)
}

func TestReleaseAction_ErrorsWhenNoCrossing(t *testing.T) {
	t.Parallel()

	// A normal early hop that does not reach the prerelease env: dev advances to
	// staging, but staging is not the release boundary, so no marker is emitted.
	path := writeManifest(t, &config.CICDFile{
		Config: &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev", "staging", "uat", "prod"}},
		State: map[string]*config.EnvState{
			"dev":     {SHA: "a1b2c3d4e5f6", Version: "v1.0.0-rc.0"},
			"staging": {},
		},
	})

	engine, err := NewEngine(path)
	require.NoError(t, err)

	_, err = engine.Simulate(NewReleaseAction())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no release crossing")
}
