package promote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordedComponentManifest seeds a component leaf the way a real promotion
// history leaves it: a deploy-history ring, per-deploy rows for two apps, and a
// sibling component. It is the fixture for proving a component-scoped finalize
// carries the recorded leaf forward instead of rebuilding it from empty.
const recordedComponentManifest = `ci:
  config:
    environments: [dev, prod]
  state:
    components:
      billing:
        prod:
          sha: billsha
          version: v2.0.0
      checkout:
        prod:
          sha: oldsha
          version: v1.3.0
          committed_at: "2026-07-01T00:00:00Z"
          committed_by: earlier
          previous:
            - sha: oldersha
              version: v1.2.0
          deploys:
            api:
              sha: oldsha
              version: v1.3.0
            web:
              sha: oldsha
              version: v1.3.0
`

// TestFinalizer_Component_CarriesForwardRecordedLeaf is the regression test for
// the component finalize state loss: a promotion of checkout's prod where only
// the web deploy ran must mutate the RECORDED leaf, so the deploy-history ring
// grows (rather than vanishing) and the api row it did not touch survives.
func TestFinalizer_Component_CarriesForwardRecordedLeaf(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(recordedComponentManifest), 0644))

	fin, err := NewFinalizer(path, "prod", WithComponent("checkout"), WithClock(fixedClock()))
	require.NoError(t, err)
	fin.SetActor("promoter")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{Environment: "prod", SourceEnv: "dev", SHA: "newsha", Version: "v1.4.0"}},
	})
	fin.SetDeployResult("web", "success")
	fin.SetDeployResult("api", "skipped")

	require.NoError(t, fin.Run())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	m := readManifestNode(t, out)
	leaf := componentEnv(t, m, "checkout", "prod")

	// The env pointer advanced.
	require.Equal(t, "newsha", leaf["sha"])
	require.Equal(t, "v1.4.0", leaf["version"])

	// The outgoing state was pushed onto the recorded ring: newest first, with
	// the previously recorded entry still behind it.
	prev, ok := leaf["previous"].([]any)
	require.True(t, ok, "previous ring must survive the component finalize, got: %#v", leaf["previous"])
	require.Len(t, prev, 2, "ring must carry the pushed snapshot plus the recorded entry")
	head := prev[0].(map[string]any)
	require.Equal(t, "oldsha", head["sha"], "outgoing state must be snapshotted onto the ring")
	require.Equal(t, "v1.3.0", head["version"])
	tail := prev[1].(map[string]any)
	require.Equal(t, "oldersha", tail["sha"], "the recorded ring entry must survive")

	// The deploy that ran was updated; the one that did not still carries its
	// recorded row instead of being dropped by a whole-leaf rebuild.
	deploys, ok := leaf["deploys"].(map[string]any)
	require.True(t, ok, "deploys map must survive")
	web := deploys["web"].(map[string]any)
	require.Equal(t, "newsha", web["sha"])
	require.Equal(t, "v1.4.0", web["version"])
	api, ok := deploys["api"].(map[string]any)
	require.True(t, ok, "the non-promoted api deploy row must survive a web-only promotion")
	require.Equal(t, "oldsha", api["sha"])
	require.Equal(t, "v1.3.0", api["version"])

	// The sibling component is untouched.
	sib := componentEnv(t, m, "billing", "prod")
	require.Equal(t, "billsha", sib["sha"])
}

// divergedRecordedComponentManifest seeds checkout's prod as diverged on its
// component-namespaced integration branch, the state a hotfix leaves behind.
const divergedRecordedComponentManifest = `ci:
  config:
    environments: [dev, prod]
    components:
      checkout:
        path: checkout
        tag_grammar:
          prefix: checkout-
  state:
    components:
      checkout:
        prod:
          sha: mergesha
          version: checkout-1.3.0-rc.2.hotfix.1
          ref: env/checkout/prod
          base_sha: basesha
          patches: [patchX]
`

// TestFinalizer_Component_RejoinFromRecordedDivergence proves a component
// promotion into a diverged env actually SEES the recorded divergence: the
// rejoin cleanup is scheduled against the component-namespaced integration
// branch and the prior hotfix version, and the divergence fields are cleared
// deliberately rather than silently dropped with no cleanup.
func TestFinalizer_Component_RejoinFromRecordedDivergence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(divergedRecordedComponentManifest), 0644))

	cleaner := &recordingCleaner{}
	fin, err := NewFinalizer(path, "prod",
		WithComponent("checkout"),
		WithClock(fixedClock()),
		WithLifecycleCleaner(cleaner),
	)
	require.NoError(t, err)
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{Environment: "prod", SourceEnv: "dev", SHA: "trunkhead", Version: "checkout-1.4.0-rc.3"}},
	})

	require.NoError(t, fin.Run())
	require.NoError(t, fin.runLifecycleCleanup())

	// The rejoin was scheduled in the component's own namespace with the version
	// the env held while diverged.
	require.Equal(t, []string{"env/checkout/prod"}, cleaner.deletedBranches,
		"the recorded divergence must schedule cleanup of the component's integration branch")
	require.Len(t, cleaner.cleanedReleases, 1)
	require.Equal(t, "checkout-1.3.0-rc.2.hotfix.1", cleaner.cleanedReleases[0].BaseVersion,
		"the base cleaned is the version the component env held while diverged")

	// The persisted leaf rejoined trunk: pointer advanced, divergence cleared.
	out, err := os.ReadFile(path)
	require.NoError(t, err)
	leaf := componentEnv(t, readManifestNode(t, out), "checkout", "prod")
	require.Equal(t, "trunkhead", leaf["sha"])
	require.Equal(t, "checkout-1.4.0-rc.3", leaf["version"])
	_, hasRef := leaf["ref"]
	require.False(t, hasRef, "ref must be cleared on rejoin")
	_, hasBase := leaf["base_sha"]
	require.False(t, hasBase, "base_sha must be cleared on rejoin")
	_, hasPatches := leaf["patches"]
	require.False(t, hasPatches, "patches must be cleared on rejoin")
}

// TestFinalizeCommand_Component_PreservesRecordedState drives the full
// `promote finalize --component` command path against the recorded fixture,
// proving the command wires the component lift, not just the Finalizer type.
func TestFinalizeCommand_Component_PreservesRecordedState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(recordedComponentManifest), 0644))

	promotionResult := PromotionResult{
		Success:  true,
		FinalEnv: "prod",
		Promotions: []EnvPromotion{
			{Environment: "prod", SourceEnv: "dev", SHA: "newsha", Version: "v1.4.0"},
		},
	}
	promotionJSON, err := json.Marshal(promotionResult)
	require.NoError(t, err)

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"finalize",
		"--config", path,
		"--component", "checkout",
		"--promotion-result", string(promotionJSON),
	})
	require.NoError(t, cmd.Execute())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	m := readManifestNode(t, out)
	leaf := componentEnv(t, m, "checkout", "prod")

	require.Equal(t, "newsha", leaf["sha"])
	prev, ok := leaf["previous"].([]any)
	require.True(t, ok, "previous ring must survive the command-level component finalize")
	require.Len(t, prev, 2)
	deploys, ok := leaf["deploys"].(map[string]any)
	require.True(t, ok, "recorded deploy rows must survive the command-level component finalize")
	_, hasAPI := deploys["api"]
	require.True(t, hasAPI, "the api row must survive a promotion that ran no deploys")

	sib := componentEnv(t, m, "billing", "prod")
	require.Equal(t, "billsha", sib["sha"])
}
