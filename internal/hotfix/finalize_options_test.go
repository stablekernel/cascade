package hotfix

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingTipReader is a record-only env-branch tip reader for option tests.
type recordingTipReader struct {
	sha string
}

func (r recordingTipReader) LocalBranchSHA(string) (string, error) { return r.sha, nil }

// TestWithFinalizeDryRun applies the dry-run option to the finalizer.
func TestWithFinalizeDryRun(t *testing.T) {
	f := &Finalizer{}
	WithFinalizeDryRun(true)(f)
	assert.True(t, f.dryRun)

	WithFinalizeDryRun(false)(f)
	assert.False(t, f.dryRun)
}

// TestWithTipReader injects a custom tip reader and ignores a nil one.
func TestWithTipReader(t *testing.T) {
	f := &Finalizer{tipReader: envTipReader{}}

	WithTipReader(recordingTipReader{sha: "deadbeef"})(f)
	rr, ok := f.tipReader.(recordingTipReader)
	require.True(t, ok, "injected reader should replace the default")
	assert.Equal(t, "deadbeef", rr.sha)

	// A nil reader must not clobber the existing one.
	WithTipReader(nil)(f)
	_, ok = f.tipReader.(recordingTipReader)
	assert.True(t, ok, "nil option must leave the prior reader intact")
}

// TestFinalizeOptions_NilSafety verifies each injecting option ignores nil and
// preserves the previously set dependency.
func TestFinalizeOptions_NilSafety(t *testing.T) {
	mgr := &stubReleaseManager{}
	lister := stubTagLister{tags: []string{"v1.0.0"}}
	pusher := &recordingPusher{}
	trunk := &stubTrunkReader{}

	f := &Finalizer{}
	WithReleaseManager(mgr)(f)
	WithTagLister(lister)(f)
	WithStatePusher(pusher)(f)
	WithTrunkStateReader(trunk)(f)

	assert.Same(t, mgr, f.releaseMgr)
	assert.Equal(t, lister, f.tagLister)
	assert.Same(t, pusher, f.pusher)
	assert.True(t, f.pusherInjected)
	assert.Same(t, trunk, f.trunkReader)

	// Nil options are no-ops and leave the dependencies in place.
	WithReleaseManager(nil)(f)
	WithStatePusher(nil)(f)
	WithTrunkStateReader(nil)(f)
	assert.Same(t, mgr, f.releaseMgr)
	assert.Same(t, pusher, f.pusher)
	assert.Same(t, trunk, f.trunkReader)
}

// TestGitIdentity covers the manifest-config to commit-identity mapping for the
// hotfix finalizer.
func TestGitIdentity(t *testing.T) {
	assert.Equal(t, "", gitIdentity(nil).Name)

	def := gitIdentity(&config.TrunkConfig{})
	assert.Equal(t, "github-actions[bot]", def.Name)
	assert.Equal(t, "github-actions[bot]@users.noreply.github.com", def.Email)

	custom := gitIdentity(&config.TrunkConfig{Git: &config.GitConfig{
		UserName:  "Hotfix Bot",
		UserEmail: "hotfix@example.com",
	}})
	assert.Equal(t, "Hotfix Bot", custom.Name)
	assert.Equal(t, "hotfix@example.com", custom.Email)
}

// TestIsRealGitHub covers the act/gitea vs github.com detection.
func TestIsRealGitHub(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	assert.True(t, isRealGitHub())

	t.Setenv("GITHUB_SERVER_URL", "")
	assert.True(t, isRealGitHub(), "unset defaults to real GitHub")

	t.Setenv("GITHUB_SERVER_URL", "http://gitea:3000")
	assert.False(t, isRealGitHub())
}

// TestAllocateVersion covers rc-nested allocation, published patch bump, tag
// collision skipping, and both error branches.
func TestAllocateVersion(t *testing.T) {
	t.Run("rc base allocates nested hotfix", func(t *testing.T) {
		f := &Finalizer{tagLister: stubTagLister{}}
		got, err := f.allocateVersion("v1.4.0-rc.2")
		require.NoError(t, err)
		assert.Equal(t, "v1.4.0-rc.2.hotfix.1", got)
	})

	t.Run("rc base skips existing hotfix tag", func(t *testing.T) {
		f := &Finalizer{tagLister: stubTagLister{tags: []string{"v1.4.0-rc.2.hotfix.1"}}}
		got, err := f.allocateVersion("v1.4.0-rc.2")
		require.NoError(t, err)
		assert.Equal(t, "v1.4.0-rc.2.hotfix.2", got)
	})

	t.Run("published base patch bump", func(t *testing.T) {
		f := &Finalizer{tagLister: stubTagLister{}}
		got, err := f.allocateVersion("v1.3.0")
		require.NoError(t, err)
		assert.Equal(t, "v1.3.1", got)
	})

	t.Run("published base skips taken patches", func(t *testing.T) {
		f := &Finalizer{tagLister: stubTagLister{tags: []string{"v1.3.1", "v1.3.2"}}}
		got, err := f.allocateVersion("v1.3.0")
		require.NoError(t, err)
		assert.Equal(t, "v1.3.3", got)
	})

	t.Run("empty prior version errors", func(t *testing.T) {
		f := &Finalizer{tagLister: stubTagLister{}}
		_, err := f.allocateVersion("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no recorded version")
	})

	t.Run("unparseable prior version errors", func(t *testing.T) {
		f := &Finalizer{tagLister: stubTagLister{}}
		_, err := f.allocateVersion("not-a-semver")
		require.Error(t, err)
	})
}

// TestIsPrereleaseEnv identifies the second-from-top env as the prerelease env.
func TestIsPrereleaseEnv(t *testing.T) {
	f := &Finalizer{}
	cfg := &config.TrunkConfig{Environments: config.EnvNames("dev", "test", "uat", "prod")}

	assert.True(t, f.isPrereleaseEnv(cfg, "uat"), "second-from-top is the prerelease env")
	assert.False(t, f.isPrereleaseEnv(cfg, "prod"))
	assert.False(t, f.isPrereleaseEnv(cfg, "dev"))

	single := &config.TrunkConfig{Environments: config.EnvNames("prod")}
	assert.False(t, f.isPrereleaseEnv(single, "prod"), "fewer than two envs has no prerelease env")
}

// TestApplyHotfixState_NoOpWhenSHAUnchanged returns early without mutating the
// patch list when the target already records the merge SHA.
func TestApplyHotfixState_NoOpWhenSHAUnchanged(t *testing.T) {
	f := &Finalizer{actor: "dev"}
	cicd := &config.CICDFile{State: map[string]*config.EnvState{
		"uat": {SHA: "mergesha", Patches: []string{"existing"}},
	}}

	err := f.applyHotfixState(cicd, "uat", "mergesha", "v1.0.1", "basesha", "2026-01-01T00:00:00Z", []string{"newfix"})
	require.NoError(t, err)

	state := cicd.State["uat"]
	assert.Equal(t, []string{"existing"}, state.Patches, "no-op rerun must not append patches")
	assert.Equal(t, "mergesha", state.SHA)
}

// TestApplyHotfixState_WritesDivergedState records the merge SHA, base, patches,
// ref, and substates on a fresh target env.
func TestApplyHotfixState_WritesDivergedState(t *testing.T) {
	f := &Finalizer{
		actor:         "dev",
		deployResults: map[string]string{"app": "success", "skipped-one": "skipped"},
		buildResults:  map[string]string{"build-app": "success"},
	}
	cicd := &config.CICDFile{}

	err := f.applyHotfixState(cicd, "uat", "newmerge", "v1.0.1", "basesha", "2026-01-01T00:00:00Z", []string{"fix1", "fix2"})
	require.NoError(t, err)

	state := cicd.State["uat"]
	require.NotNil(t, state)
	assert.Equal(t, "newmerge", state.SHA)
	assert.Equal(t, "v1.0.1", state.Version)
	assert.Equal(t, "basesha", state.BaseSHA)
	assert.Equal(t, "env/uat", state.Ref)
	assert.Equal(t, []string{"fix1", "fix2"}, state.Patches)
	assert.Equal(t, "dev", state.CommittedBy)

	require.NotNil(t, state.Deploys["app"], "successful deploy substate recorded")
	assert.Equal(t, "newmerge", state.Deploys["app"].SHA)
	assert.Nil(t, state.Deploys["skipped-one"], "skipped deploy must not be recorded")
	require.NotNil(t, state.Builds["build-app"], "successful build substate recorded")
}
