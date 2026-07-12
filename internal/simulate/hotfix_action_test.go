package simulate

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// seedHotfixManifest writes a manifest whose uat env (the prerelease env in the
// dev->uat->prod chain) holds a parseable rc version and a recorded state SHA to
// diverge from.
func seedHotfixManifest(t *testing.T) string {
	t.Helper()

	return writeManifest(t, &config.CICDFile{
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: config.EnvNames("dev", "uat", "prod"),
		},
		State: map[string]*config.EnvState{
			"uat": {
				SHA:         "basesha000000",
				Version:     "v1.0.0-rc.1",
				CommittedAt: "2026-01-01T10:00:00Z",
				CommittedBy: "seed-user",
			},
		},
	})
}

func TestHotfixAction_DivergesEnvAndRecordsPatch(t *testing.T) {
	t.Parallel()

	path := seedHotfixManifest(t)

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	result, err := engine.Simulate(NewHotfixAction("uat", []string{"fixaaa1110000"}, ""))
	require.NoError(t, err)

	assert.Equal(t, "hotfix", result.ActionName)

	uat, ok := result.Diff.Env("uat")
	require.True(t, ok)
	assert.Equal(t, "basesha000000", uat.SHA.From)
	assert.Equal(t, "fixaaa1110000", uat.SHA.To)
	assert.True(t, uat.Version.Changed)
	assert.Equal(t, "v1.0.0-rc.1.hotfix.1", uat.Version.To)
	assert.True(t, uat.Divergence.Changed, "the env must go diverged")
	assert.Equal(t, "yes", uat.Divergence.To)
	assert.True(t, uat.PreviousRing.Changed, "the prior state is snapshotted into the ring")

	// apply patch -> write state -> release create -> release prerelease.
	require.GreaterOrEqual(t, len(result.Effects), 4)
	assert.Equal(t, "apply patch", result.Effects[0].Action)
	assert.Equal(t, "uat", result.Effects[0].Target)
	assert.Equal(t, "write state", result.Effects[1].Action)
	assert.Equal(t, "release create", result.Effects[2].Action)
	assert.Equal(t, "release prerelease", result.Effects[3].Action,
		"hotfix on the prerelease env promotes the release object to prerelease")
}

func TestHotfixAction_MultiCommitRecordsEveryPatch(t *testing.T) {
	t.Parallel()

	path := seedHotfixManifest(t)

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	result, err := engine.Simulate(
		NewHotfixAction("uat", []string{"fixaaa1110000", "fixbbb2220000"}, "mergesha00000"))
	require.NoError(t, err)

	var applyPatch int
	for _, e := range result.Effects {
		if e.Action == "apply patch" {
			applyPatch++
		}
	}
	assert.Equal(t, 2, applyPatch, "every carried commit yields an apply-patch effect")

	uat, ok := result.Diff.Env("uat")
	require.True(t, ok)
	assert.Equal(t, "mergesha00000", uat.SHA.To, "the env advances to the merge SHA")
}

func TestHotfixAction_LeavesOriginalUntouched(t *testing.T) {
	t.Parallel()

	path := seedHotfixManifest(t)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)
	_, err = engine.Simulate(NewHotfixAction("uat", []string{"fixaaa1110000"}, ""))
	require.NoError(t, err)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the original manifest bytes must be unchanged")
}

func TestHotfixAction_NoStateErrors(t *testing.T) {
	t.Parallel()

	path := seedHotfixManifest(t)
	engine, err := NewEngine(path)
	require.NoError(t, err)

	_, err = engine.Simulate(NewHotfixAction("prod", []string{"fixaaa1110000"}, ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no recorded state SHA")
}
