package promote

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// TestPreflighter_ComponentSubset_TreatsLastSubsetEnvAsFinal proves the preflight
// planner honors a component's narrowed ladder end to end: for "api" (ladder [dev,
// staging]) a default-mode preflight advances dev->staging and marks staging as the
// final environment, never planning an advance into the global-only prod env.
func TestPreflighter_ComponentSubset_TreatsLastSubsetEnvAsFinal(t *testing.T) {
	path := writeSubsetManifest(t)
	cicdFile, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	require.NoError(t, err)
	require.NoError(t, overlayComponentState(cicdFile, path, "api"))
	require.NoError(t, applyComponentLadder(cicdFile, "api"))

	pf := NewPreflighter(PreflighterOptions{
		Config: cicdFile,
		Mode:   ModeDefault,
	})

	result, err := pf.Run()
	require.NoError(t, err)
	for _, env := range result.EnvsToUpdate {
		require.NotEqual(t, "prod", env, "api preflight must not plan an advance into prod")
	}
	// staging is api's last env, so crossing into it is the terminal publish
	// boundary: the plan advances to the "release" marker and marks the crossing
	// final, rather than trying to advance to the global-only prod env.
	require.True(t, result.IsFinalEnv, "api's last-env crossing must be treated as final")
	require.Equal(t, "release", result.TargetEnv, "the terminal crossing lands on the release marker, never prod")
}

// TestPreflighter_ComponentSubset_SiblingReachesProd proves the sibling "web",
// inheriting the full ladder, still plans a cascade all the way to prod.
func TestPreflighter_ComponentSubset_SiblingReachesProd(t *testing.T) {
	path := writeSubsetManifest(t)
	cicdFile, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	require.NoError(t, err)
	require.NoError(t, overlayComponentState(cicdFile, path, "web"))
	require.NoError(t, applyComponentLadder(cicdFile, "web"))

	pf := NewPreflighter(PreflighterOptions{
		Config: cicdFile,
		Mode:   ModeCascade,
		Target: "dev-to-prod",
	})

	result, err := pf.Run()
	require.NoError(t, err)
	require.Equal(t, "prod", result.TargetEnv, "web reaches prod on the full ladder")
}
