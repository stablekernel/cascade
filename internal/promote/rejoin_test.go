package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/hotfix"
	"github.com/stretchr/testify/require"
)

// recordingCleaner is a test LifecycleCleaner that records the rejoin cleanups it
// is asked to perform, so tests can assert which side effects fired (and that
// non-diverged promotions fire none).
type recordingCleaner struct {
	deletedBranches []string
	cleanedReleases []CleanReleasesRequest
}

// DeleteEnvBranch records the fully composed integration branch name
// (hotfix.EnvBranchName(component, env)) rather than the bare env, so tests can
// assert the component was threaded through and the branch targeted its own
// namespace: env/<env> for the default component, env/<component>/<env> for a
// named one.
func (c *recordingCleaner) DeleteEnvBranch(component, env string) error {
	c.deletedBranches = append(c.deletedBranches, hotfix.EnvBranchName(component, env))
	return nil
}

func (c *recordingCleaner) CleanHotfixReleases(req CleanReleasesRequest) error {
	c.cleanedReleases = append(c.cleanedReleases, req)
	return nil
}

// divergedManifest writes a manifest where "test" is diverged on env/test with a
// single patch on top of a trunk base, and "uat" is also diverged independently.
func divergedManifest(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	initialConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
  state:
    dev:
      sha: trunkhead
      version: v1.4.0-rc.3
    test:
      sha: mergesha
      version: v1.4.0-rc.2.hotfix.1
      ref: env/test
      base_sha: basesha
      patches: [patchX]
    uat:
      sha: uatmerge
      version: v1.3.0-rc.5.hotfix.1
      ref: env/uat
      base_sha: uatbase
      patches: [patchY]
`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0644))
	return configPath
}

func TestRejoin_ClearsDivergenceFields(t *testing.T) {
	configPath := divergedManifest(t)
	cleaner := &recordingCleaner{}

	fin, err := NewFinalizer(configPath, "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)

	// Normal trunk promotion into the diverged "test" env: incoming SHA contains
	// the recorded patch (containment gate already passed in preflight).
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         "trunkhead",
			Version:     "v1.4.0-rc.3",
		}},
	})

	require.NoError(t, fin.Run())
	require.NoError(t, fin.runLifecycleCleanup())

	cicd, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	st := cicd.State["test"]
	require.NotNil(t, st)
	require.Equal(t, "trunkhead", st.SHA)
	require.Equal(t, "v1.4.0-rc.3", st.Version)
	require.Empty(t, st.Ref, "ref must be cleared on rejoin")
	require.Empty(t, st.BaseSHA, "base_sha must be cleared on rejoin")
	require.Empty(t, st.Patches, "patches must be cleared on rejoin")
	require.False(t, st.IsDiverged(), "env must no longer be diverged")
}

func TestRejoin_DeletesEnvBranch(t *testing.T) {
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
	require.NoError(t, fin.runLifecycleCleanup())

	require.Equal(t, []string{"env/test"}, cleaner.deletedBranches,
		"the rejoined env's integration branch must be deleted exactly once")
}

func TestRejoin_CleansHotfixTagsAndDrafts(t *testing.T) {
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
	require.NoError(t, fin.runLifecycleCleanup())

	require.Len(t, cleaner.cleanedReleases, 1)
	req := cleaner.cleanedReleases[0]
	require.Equal(t, "test", req.Environment)
	// The base version cleaned is the version the env held while diverged.
	require.Equal(t, "v1.4.0-rc.2.hotfix.1", req.BaseVersion)
}

func TestRejoin_PreservesOtherEnvsDivergence(t *testing.T) {
	configPath := divergedManifest(t)
	cleaner := &recordingCleaner{}

	fin, err := NewFinalizer(configPath, "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)

	// Only "test" is being promoted into; "uat" stays diverged and untouched.
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         "trunkhead",
			Version:     "v1.4.0-rc.3",
		}},
	})

	require.NoError(t, fin.Run())
	require.NoError(t, fin.runLifecycleCleanup())

	cicd, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	uat := cicd.State["uat"]
	require.NotNil(t, uat)
	require.True(t, uat.IsDiverged(), "uat divergence must be preserved")
	require.Equal(t, "env/uat", uat.Ref)
	require.Equal(t, "uatbase", uat.BaseSHA)
	require.Equal(t, []string{"patchY"}, uat.Patches)

	// Cleanup must only have touched the rejoined env.
	require.Equal(t, []string{"env/test"}, cleaner.deletedBranches)
	require.Len(t, cleaner.cleanedReleases, 1)
	require.Equal(t, "test", cleaner.cleanedReleases[0].Environment)
}

func TestNormalPromotion_NonDiverged_TouchesNoLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	initialConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.1
    test:
      sha: oldsha
      version: v1.0.0-rc.0
`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0644))

	cleaner := &recordingCleaner{}
	fin, err := NewFinalizer(configPath, "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         "abc123",
			Version:     "v1.0.0-rc.1",
		}},
	})

	require.NoError(t, fin.Run())
	require.NoError(t, fin.runLifecycleCleanup())

	require.Empty(t, cleaner.deletedBranches, "non-diverged promotion must delete no branch")
	require.Empty(t, cleaner.cleanedReleases, "non-diverged promotion must clean no releases")

	cicd, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)
	st := cicd.State["test"]
	require.Equal(t, "abc123", st.SHA)
	require.False(t, st.IsDiverged())
}
