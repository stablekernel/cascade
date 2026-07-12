package promote

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// newDowngradePreflighter builds a Preflighter wired with the given env state and
// the allow-downgrade flag, so checkDowngrade can be exercised in isolation.
func newDowngradePreflighter(t *testing.T, state map[string]*config.EnvState, allow bool) *Preflighter {
	t.Helper()
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys:      []config.DeployConfig{{Name: "app"}},
		},
		State: state,
	}
	return NewPreflighter(PreflighterOptions{
		Config:         cfg,
		Mode:           ModeDefault,
		BaseDir:        "",
		AllowDowngrade: allow,
	})
}

func TestCheckDowngrade_ForwardPromotion_Passes(t *testing.T) {
	p := newDowngradePreflighter(t, map[string]*config.EnvState{
		"test": {Version: "v1.0.0"},
	}, false)
	result := &PreflightResult{SourceVersion: "v1.1.0"}
	promotions := []EnvPromotion{{Environment: "test"}}

	err := p.checkDowngrade(promotions, result, "prod")
	require.NoError(t, err)
	require.Empty(t, result.Warnings)
}

func TestCheckDowngrade_EqualVersion_Passes(t *testing.T) {
	p := newDowngradePreflighter(t, map[string]*config.EnvState{
		"test": {Version: "v1.0.0"},
	}, false)
	result := &PreflightResult{SourceVersion: "v1.0.0"}
	promotions := []EnvPromotion{{Environment: "test"}}

	err := p.checkDowngrade(promotions, result, "prod")
	require.NoError(t, err)
	require.Empty(t, result.Warnings)
}

func TestCheckDowngrade_Downgrade_BlockedWithoutFlag(t *testing.T) {
	p := newDowngradePreflighter(t, map[string]*config.EnvState{
		"test": {Version: "v1.2.0"},
	}, false)
	result := &PreflightResult{SourceVersion: "v1.1.0"}
	promotions := []EnvPromotion{{Environment: "test"}}

	err := p.checkDowngrade(promotions, result, "prod")
	require.Error(t, err)
	require.Contains(t, err.Error(), "test")
	require.Contains(t, err.Error(), "v1.2.0")
	require.Contains(t, err.Error(), "v1.1.0")
	require.Contains(t, err.Error(), "--allow-downgrade")
}

func TestCheckDowngrade_Downgrade_AllowedWithFlag(t *testing.T) {
	p := newDowngradePreflighter(t, map[string]*config.EnvState{
		"test": {Version: "v1.2.0"},
	}, true)
	result := &PreflightResult{SourceVersion: "v1.1.0"}
	promotions := []EnvPromotion{{Environment: "test"}}

	err := p.checkDowngrade(promotions, result, "prod")
	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	require.Contains(t, result.Warnings[0], "test")
	require.Contains(t, result.Warnings[0], "v1.2.0")
	require.Contains(t, result.Warnings[0], "v1.1.0")
}

func TestCheckDowngrade_ProdDowngrade_AlwaysRequiresFlag(t *testing.T) {
	// Without the flag, a prod downgrade is blocked.
	p := newDowngradePreflighter(t, map[string]*config.EnvState{
		"prod": {Version: "v2.0.0"},
	}, false)
	result := &PreflightResult{SourceVersion: "v1.9.0"}
	promotions := []EnvPromotion{{Environment: "prod"}}

	err := p.checkDowngrade(promotions, result, "prod")
	require.Error(t, err)
	require.Contains(t, err.Error(), "prod")
	require.Contains(t, err.Error(), "v2.0.0")
	require.Contains(t, err.Error(), "v1.9.0")
	require.Contains(t, err.Error(), "--allow-downgrade")

	// With the flag, a prod downgrade is permitted but warned.
	pAllow := newDowngradePreflighter(t, map[string]*config.EnvState{
		"prod": {Version: "v2.0.0"},
	}, true)
	resultAllow := &PreflightResult{SourceVersion: "v1.9.0"}
	err = pAllow.checkDowngrade(promotions, resultAllow, "prod")
	require.NoError(t, err)
	require.Len(t, resultAllow.Warnings, 1)
	require.Contains(t, resultAllow.Warnings[0], "prod")
	require.Contains(t, resultAllow.Warnings[0], "v2.0.0")
	require.Contains(t, resultAllow.Warnings[0], "v1.9.0")
}

func TestCheckDowngrade_NonSemver_WarnsAndAllows(t *testing.T) {
	// Current version is non-semver: fail-open with a warning naming both
	// versions and the env, and proceed.
	p := newDowngradePreflighter(t, map[string]*config.EnvState{
		"test": {Version: "not-a-version"},
	}, false)
	result := &PreflightResult{SourceVersion: "v1.1.0"}
	promotions := []EnvPromotion{{Environment: "test"}}

	err := p.checkDowngrade(promotions, result, "prod")
	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	require.Contains(t, result.Warnings[0], "test")
	require.Contains(t, result.Warnings[0], "not-a-version")
	require.Contains(t, result.Warnings[0], "v1.1.0")

	// Incoming version is non-semver: also fail-open with a warning.
	p2 := newDowngradePreflighter(t, map[string]*config.EnvState{
		"test": {Version: "v1.1.0"},
	}, false)
	result2 := &PreflightResult{SourceVersion: "garbage"}
	err = p2.checkDowngrade(promotions, result2, "prod")
	require.NoError(t, err)
	require.Len(t, result2.Warnings, 1)
	require.Contains(t, result2.Warnings[0], "test")
	require.Contains(t, result2.Warnings[0], "v1.1.0")
	require.Contains(t, result2.Warnings[0], "garbage")
}

func TestCheckDowngrade_EmptyVersions_Skipped(t *testing.T) {
	// Empty current version: skipped, no error, no warning.
	p := newDowngradePreflighter(t, map[string]*config.EnvState{
		"test": {Version: ""},
	}, false)
	result := &PreflightResult{SourceVersion: "v1.1.0"}
	promotions := []EnvPromotion{{Environment: "test"}}

	err := p.checkDowngrade(promotions, result, "prod")
	require.NoError(t, err)
	require.Empty(t, result.Warnings)

	// Empty source version: skipped too.
	p2 := newDowngradePreflighter(t, map[string]*config.EnvState{
		"test": {Version: "v1.1.0"},
	}, false)
	result2 := &PreflightResult{SourceVersion: ""}
	err = p2.checkDowngrade(promotions, result2, "prod")
	require.NoError(t, err)
	require.Empty(t, result2.Warnings)
}

// TestCheckDowngrade_PublishBoundary_RCToReleaseNotDowngrade reproduces the
// cascade publish-boundary false positive: a cascade from a prerelease env at
// v1.0.0-rc.0 to prod materializes a "release" promotion carrying the stripped
// semver v1.0.0. The release env currently holds the previously published
// v1.0.0. The version that LANDS on release is v1.0.0 (promo.Version), which is
// equal, not a downgrade. The gate must compare against promo.Version, not the
// source env's raw rc version, otherwise it wrongly blocks the finalization.
func TestCheckDowngrade_PublishBoundary_RCToReleaseNotDowngrade(t *testing.T) {
	p := newDowngradePreflighter(t, map[string]*config.EnvState{
		"release": {Version: "v1.0.0"},
		"prod":    {Version: "v0.3.0"},
	}, false)
	// SourceVersion is the source env's raw rc; promo.Version is what lands.
	result := &PreflightResult{SourceVersion: "v1.0.0-rc.0"}
	promotions := []EnvPromotion{
		{Environment: "release", Version: "v1.0.0"},
		{Environment: "prod", Version: "v1.0.0"},
	}

	err := p.checkDowngrade(promotions, result, "prod")
	require.NoError(t, err)
	require.Empty(t, result.Warnings)
}

// TestCheckDowngrade_RealDowngrade_StillBlockedViaPromoVersion proves the fix
// does not weaken protection: when the version that actually lands (promo.Version)
// is older than the env's current version, the gate still blocks.
func TestCheckDowngrade_RealDowngrade_StillBlockedViaPromoVersion(t *testing.T) {
	p := newDowngradePreflighter(t, map[string]*config.EnvState{
		"release": {Version: "v2.0.0"},
	}, false)
	// Source raw version looks newer, but the version landing on release is older.
	result := &PreflightResult{SourceVersion: "v2.1.0-rc.0"}
	promotions := []EnvPromotion{{Environment: "release", Version: "v1.5.0"}}

	err := p.checkDowngrade(promotions, result, "prod")
	require.Error(t, err)
	require.Contains(t, err.Error(), "release")
	require.Contains(t, err.Error(), "v2.0.0")
	require.Contains(t, err.Error(), "v1.5.0")
	require.Contains(t, err.Error(), "--allow-downgrade")
}

// TestCheckDowngrade_PrereleaseEnv_RCProgressesForward confirms a cascade that
// carries an rc forward into prerelease envs is allowed when the rc is newer,
// using promo.Version (the rc) rather than a stripped value.
func TestCheckDowngrade_PrereleaseEnv_RCProgressesForward(t *testing.T) {
	p := newDowngradePreflighter(t, map[string]*config.EnvState{
		"uat": {Version: "v0.3.0-rc.0"},
	}, false)
	result := &PreflightResult{SourceVersion: "v1.0.0-rc.0"}
	promotions := []EnvPromotion{{Environment: "uat", Version: "v1.0.0-rc.0"}}

	err := p.checkDowngrade(promotions, result, "prod")
	require.NoError(t, err)
	require.Empty(t, result.Warnings)
}
