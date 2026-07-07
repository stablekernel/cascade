package promote

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }

// betaConfig returns a manifest whose tag grammar names a "beta" pre-release
// token instead of the historical "rc".
func betaConfig(state map[string]*config.EnvState) *config.CICDFile {
	return &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: []string{"dev", "test", "uat", "prod"},
			Deploys:      []config.DeployConfig{{Name: "app"}},
			TagGrammar: &config.TagGrammarConfig{
				PreReleaseToken: strPtr("beta"),
			},
		},
		State: state,
	}
}

// TestStripPreRelease_CustomToken proves the promote strip honors the configured
// pre-release token: a beta pre-release is reduced to its base, where the
// hardcoded "-rc." cut would have published the pre-release shape unchanged.
func TestStripPreRelease_CustomToken(t *testing.T) {
	p := &Promoter{cicdFile: betaConfig(nil)}
	if got := p.stripPreRelease("1.4.0-beta.2"); got != "1.4.0" {
		t.Errorf("stripPreRelease(%q) = %q, want %q", "1.4.0-beta.2", got, "1.4.0")
	}
}

// TestPreflight_MonotonicityEnforcedUnderCustomToken proves the downgrade guard
// stays active under a custom token: a 1.4.0-beta.2 promotion onto a
// 1.5.0-beta.1 env is a downgrade and must be BLOCKED, not fail-open warned.
func TestPreflight_MonotonicityEnforcedUnderCustomToken(t *testing.T) {
	cfg := betaConfig(map[string]*config.EnvState{
		"test": {Version: "1.5.0-beta.1"},
	})
	p := NewPreflighter(PreflighterOptions{
		Config: cfg,
		Mode:   ModeDefault,
	})
	result := &PreflightResult{}
	promotions := []EnvPromotion{{Environment: "test", Version: "1.4.0-beta.2"}}

	err := p.checkDowngrade(promotions, result, "prod")
	require.Error(t, err)
	require.Contains(t, err.Error(), "test")
	require.Empty(t, result.Warnings)
}
