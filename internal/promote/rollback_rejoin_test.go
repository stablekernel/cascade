package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// rollbackDivergedManifest writes a manifest where "prod" is diverged via a
// manual rollback (ref carries the RollbackRefPrefix and no patches), so a
// forward promotion into it rejoins trunk without any integration branch or
// hotfix release objects to clean up.
func rollbackDivergedManifest(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	initialConfig := `ci:
  config:
    environments: [dev, prod]
  state:
    dev:
      sha: trunkhead
      version: v2.0.0
    prod:
      sha: oldgoodsha
      version: v1.9.0
      ref: rollback/prod
      base_sha: priorprodsha
`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0644))
	return configPath
}

func TestFinalize_RollbackRejoin_SkipsBranchDeletion(t *testing.T) {
	configPath := rollbackDivergedManifest(t)
	cleaner := &recordingCleaner{}

	fin, err := NewFinalizer(configPath, "prod", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)

	// Forward promotion into the rollback-diverged "prod" env.
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "prod",
			SourceEnv:   "dev",
			SHA:         "trunkhead",
			Version:     "v2.0.0",
		}},
	})

	require.NoError(t, fin.Run())

	// A rollback-origin rejoin must not touch any integration branch or
	// hotfix release objects (none exist for a manual rollback).
	require.Empty(t, cleaner.deletedBranches, "rollback rejoin must delete no env branch")
	require.Empty(t, cleaner.cleanedReleases, "rollback rejoin must clean no hotfix releases")

	cicd, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)
	st := cicd.State["prod"]
	require.NotNil(t, st)
	require.Empty(t, st.Ref, "ref must be cleared on rejoin")
	require.Empty(t, st.BaseSHA, "base_sha must be cleared on rejoin")
	require.Empty(t, st.Patches, "patches must be cleared on rejoin")
	require.False(t, st.IsDiverged(), "env must no longer be diverged")
}

func TestFinalize_HotfixRejoin_DeletesBranch(t *testing.T) {
	configPath := divergedManifest(t)
	cleaner := &recordingCleaner{}

	fin, err := NewFinalizer(configPath, "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         "trunkhead",
			Version:     "v1.4.0-rc.3",
		}},
	})

	require.NoError(t, fin.Run())

	// A hotfix-origin rejoin still deletes the integration branch and cleans
	// its hotfix releases exactly as before.
	require.Equal(t, []string{"env/test"}, cleaner.deletedBranches,
		"hotfix rejoin must delete the integration branch exactly once")
	require.Len(t, cleaner.cleanedReleases, 1,
		"hotfix rejoin must clean its hotfix releases")
}
