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
			Environments: []string{"dev", "test", "uat", "prod"},
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
