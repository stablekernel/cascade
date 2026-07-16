package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/hotfix"
	"github.com/stablekernel/cascade/internal/taggrammar"
	"github.com/stretchr/testify/require"
)

// divergedComponentManifest writes a manifest declaring two components (api and
// web, each with its own strict tag namespace) whose flat state carries two
// diverged envs on component-namespaced integration branches: "test" on
// env/api/test and "staging" on env/web/staging. It lets a rejoin cleanup prove
// it deletes the rejoining component's branch in its own namespace, never a
// sibling's, and collects tags under the component's strict grammar.
func divergedComponentManifest(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	initialConfig := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, staging, prod]
    components:
      api:
        path: api
        tag_grammar:
          prefix: api-
      web:
        path: web
        tag_grammar:
          prefix: web-
  state:
    dev:
      sha: trunkhead
      version: api-1.4.0-rc.3
    test:
      sha: apimerge
      version: api-1.4.0-rc.2.hotfix.1
      ref: env/api/test
      base_sha: apibase
      patches: [apipatch]
    staging:
      sha: webmerge
      version: web-2.1.0-rc.5.hotfix.1
      ref: env/web/staging
      base_sha: webbase
      patches: [webpatch]
`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0644))
	return configPath
}

// TestRejoin_Component_DeletesComponentNamespacedBranch proves the rejoin cleanup
// recovers the component from the recorded ref (env/api/test) and deletes the
// integration branch in that component's namespace, not the env-only env/test.
func TestRejoin_Component_DeletesComponentNamespacedBranch(t *testing.T) {
	configPath := divergedComponentManifest(t)
	cleaner := &recordingCleaner{}

	fin, err := NewFinalizer(configPath, "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)

	// Trunk promotion into the diverged api "test" env (containment gate already
	// passed in preflight): the env rejoins trunk and its component-namespaced
	// branch must be cleaned up.
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         "trunkhead",
			Version:     "api-1.4.0-rc.3",
		}},
	})

	require.NoError(t, fin.Run())
	require.NoError(t, fin.runLifecycleCleanup())

	require.Equal(t, []string{"env/api/test"}, cleaner.deletedBranches,
		"the rejoining component's branch must be deleted in its own namespace, not env/test")
}

// TestRejoin_Component_NeverCrossDeletesSibling proves that rejoining one
// component's env never deletes a sibling component's integration branch. Only
// "test" (api) is promoted; "staging" (web) stays diverged and untouched.
func TestRejoin_Component_NeverCrossDeletesSibling(t *testing.T) {
	configPath := divergedComponentManifest(t)
	cleaner := &recordingCleaner{}

	fin, err := NewFinalizer(configPath, "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         "trunkhead",
			Version:     "api-1.4.0-rc.3",
		}},
	})

	require.NoError(t, fin.Run())
	require.NoError(t, fin.runLifecycleCleanup())

	require.Equal(t, []string{"env/api/test"}, cleaner.deletedBranches,
		"only the rejoining component's branch is deleted")
	require.NotContains(t, cleaner.deletedBranches, "env/web/staging",
		"a sibling component's branch must never be cross-deleted")
}

// TestRejoin_Component_TagCollectionScopedToComponent proves the spec threaded
// into the hotfix-release cleanup is the rejoining component's strict grammar, so
// tag collection sees only that component's hotfix tags and never a sibling's.
func TestRejoin_Component_TagCollectionScopedToComponent(t *testing.T) {
	configPath := divergedComponentManifest(t)
	cleaner := &recordingCleaner{}

	fin, err := NewFinalizer(configPath, "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         "trunkhead",
			Version:     "api-1.4.0-rc.3",
		}},
	})

	require.NoError(t, fin.Run())
	require.NoError(t, fin.runLifecycleCleanup())

	require.Len(t, cleaner.cleanedReleases, 1)
	req := cleaner.cleanedReleases[0]
	require.Equal(t, "test", req.Environment)
	require.Equal(t, "api-1.4.0-rc.2.hotfix.1", req.BaseVersion,
		"the base cleaned is the version the component env held while diverged")

	// Apply the threaded spec to a mixed tag list: only the api component's own
	// hotfix tags for that rc base may be collected. A sibling web- tag on the
	// same numeric base, and a default-grammar v-prefixed tag, must be excluded by
	// the strict api- prefix.
	mixed := []string{
		"api-1.4.0-rc.2.hotfix.1",
		"api-1.4.0-rc.2.hotfix.2",
		"web-1.4.0-rc.2.hotfix.1",
		"v1.4.0-rc.2.hotfix.1",
		"api-1.4.0-rc.3.hotfix.1", // different rc base
	}
	collected := hotfix.HotfixTagsForBase(req.Spec, req.BaseVersion, mixed)
	require.Equal(t, []string{"api-1.4.0-rc.2.hotfix.1", "api-1.4.0-rc.2.hotfix.2"}, collected,
		"tag collection must be scoped to the api component's own namespace and rc base")
}

// TestRejoin_SingleComponent_ByteIdentical proves a single-component rejoin
// (ref env/<env>, empty recovered component) deletes env/<env> and collects tags
// under the manifest's permissive default grammar exactly as before components
// existed.
func TestRejoin_SingleComponent_ByteIdentical(t *testing.T) {
	configPath := divergedManifest(t) // ref env/test, version v1.4.0-rc.2.hotfix.1
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
		"a single-component rejoin deletes env/<env> exactly as before")

	require.Len(t, cleaner.cleanedReleases, 1)
	req := cleaner.cleanedReleases[0]
	require.Equal(t, taggrammar.Default(), req.Spec,
		"single-component cleanup uses the permissive default grammar, unchanged")

	// The default grammar collects the v-prefixed hotfix tags for the rc base.
	mixed := []string{"v1.4.0-rc.2.hotfix.1", "v1.4.0-rc.2.hotfix.2", "v1.4.0-rc.3.hotfix.1"}
	collected := hotfix.HotfixTagsForBase(req.Spec, req.BaseVersion, mixed)
	require.Equal(t, []string{"v1.4.0-rc.2.hotfix.1", "v1.4.0-rc.2.hotfix.2"}, collected)
}
