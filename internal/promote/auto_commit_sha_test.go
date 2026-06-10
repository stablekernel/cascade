package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// TestFinalizer_AutoCommits_RefreshesEnvSHA verifies that when SetHeadSHA is
// called with a post-callback SHA, state.<env>.sha records the override rather
// than the SHA carried in the promotion result.
func TestFinalizer_AutoCommits_RefreshesEnvSHA(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initial := `ci:
  config:
    environments: [dev, test]
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
  state:
    dev:
      sha: aaa000
      version: v1.0.0-1
    test: {}
`
	require.NoError(t, os.WriteFile(configPath, []byte(initial), 0644))

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	triggeringSHA := "bbb111"
	postCallbackSHA := "ccc222"

	fin.SetDeployResult("app", "success")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         triggeringSHA,
			Version:     "v1.0.0-2",
		}},
	})
	// Simulate an auto-committing callback having advanced HEAD.
	fin.SetHeadSHA(postCallbackSHA)

	require.NoError(t, fin.Run())

	updated, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	testState := updated.State["test"]
	require.NotNil(t, testState)
	require.Equal(t, postCallbackSHA, testState.SHA,
		"env state sha must use the post-callback HEAD, not the triggering SHA")

	// Per-deploy state must also carry the override SHA.
	require.NotNil(t, testState.Deploys)
	require.NotNil(t, testState.Deploys["app"])
	require.Equal(t, postCallbackSHA, testState.Deploys["app"].SHA,
		"per-deploy sha must also use the post-callback HEAD")
}

// TestFinalizer_NoAutoCommits_KeepsPromotionSHA verifies that when SetHeadSHA
// is not called the existing behavior is unchanged: state.<env>.sha comes
// directly from the promotion result's SHA field.
func TestFinalizer_NoAutoCommits_KeepsPromotionSHA(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initial := `ci:
  config:
    environments: [dev, test]
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
  state:
    dev:
      sha: aaa000
      version: v1.0.0-1
    test: {}
`
	require.NoError(t, os.WriteFile(configPath, []byte(initial), 0644))

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	promotionSHA := "ddd333"

	fin.SetDeployResult("app", "success")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         promotionSHA,
			Version:     "v1.0.0-2",
		}},
	})
	// No SetHeadSHA call — standard path.

	require.NoError(t, fin.Run())

	updated, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	testState := updated.State["test"]
	require.NotNil(t, testState)
	require.Equal(t, promotionSHA, testState.SHA,
		"without auto-commits override, env sha must come from the promotion result")

	require.NotNil(t, testState.Deploys)
	require.NotNil(t, testState.Deploys["app"])
	require.Equal(t, promotionSHA, testState.Deploys["app"].SHA,
		"without auto-commits override, per-deploy sha must come from the promotion result")
}

// TestFinalizer_AutoCommits_EmptySHA_FallsBackToPromotion verifies that an
// empty string passed to SetHeadSHA is treated as "not set" and the promotion
// SHA is used unchanged, providing a safe zero-value default.
func TestFinalizer_AutoCommits_EmptySHA_FallsBackToPromotion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initial := `ci:
  config:
    environments: [dev, test]
  state:
    dev:
      sha: aaa000
    test: {}
`
	require.NoError(t, os.WriteFile(configPath, []byte(initial), 0644))

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	promotionSHA := "eee444"
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         promotionSHA,
			Version:     "v1.0.0-3",
		}},
	})
	fin.SetHeadSHA("") // explicit empty — must behave like unset

	require.NoError(t, fin.Run())

	updated, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	testState := updated.State["test"]
	require.NotNil(t, testState)
	require.Equal(t, promotionSHA, testState.SHA,
		"empty override must fall back to the promotion result SHA")
}
