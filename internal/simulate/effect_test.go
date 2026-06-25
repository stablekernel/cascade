package simulate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/promote"
)

func TestEffectsFromResult_DeployAndWriteState(t *testing.T) {
	t.Parallel()

	result := &promote.PromotionResult{
		Success: true,
		Mode:    promote.ModeDefault,
		Promotions: []promote.EnvPromotion{
			{Environment: "uat", SourceEnv: "dev", SHA: "a1b2c3d", Version: "v1.2.0", NeedsDeploy: true},
		},
	}

	effects := effectsFromResult(result, nil)
	require.Len(t, effects, 2)

	assert.Equal(t, DispositionRun, effects[0].Disposition)
	assert.Equal(t, "deploy", effects[0].Action)
	assert.Equal(t, "uat", effects[0].Target)

	assert.Equal(t, DispositionRun, effects[1].Disposition)
	assert.Equal(t, "write state", effects[1].Action)
	assert.Equal(t, "uat", effects[1].Target)
}

func TestEffectsFromResult_ReleaseMarkerAdvanceIsWriteStateNotDeploy(t *testing.T) {
	t.Parallel()

	result := &promote.PromotionResult{
		Success: true,
		Mode:    promote.ModeDefault,
		Promotions: []promote.EnvPromotion{
			{Environment: "prod", SourceEnv: "uat", SHA: "a1b2c3d", Version: "v1.2.0", NeedsDeploy: false},
		},
	}

	effects := effectsFromResult(result, nil)
	require.Len(t, effects, 1)
	assert.Equal(t, DispositionRun, effects[0].Disposition)
	assert.Equal(t, "write state", effects[0].Action)
	assert.Equal(t, "prod", effects[0].Target)
}

func TestReleaseMarkerEffect_PrereleaseAndPublish(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		action     string
		wantAction string
	}{
		{name: "prerelease", action: "prerelease", wantAction: "release prerelease"},
		{name: "publish", action: "publish", wantAction: "release publish"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := &promote.PromotionResult{
				Success:       true,
				Mode:          promote.ModeDefault,
				ReleaseAction: tc.action,
				ReleaseData:   &promote.ReleaseData{SHA: "a1b2c3d4e5f6", RCVersion: "v1.0.0-rc.3", SemVersion: "v1.0.0"},
			}

			marker, ok := releaseMarkerEffect(result)
			require.True(t, ok)
			assert.Equal(t, tc.wantAction, marker.Action)
			assert.Equal(t, "v1.0.0", marker.Target, "the target is the semver being marked")
			assert.Equal(t, DispositionRun, marker.Disposition)
			assert.Contains(t, marker.Detail, "v1.0.0-rc.3")
		})
	}
}

func TestReleaseMarkerEffect_NoMarkerWhenEmpty(t *testing.T) {
	t.Parallel()

	_, ok := releaseMarkerEffect(&promote.PromotionResult{Success: true})
	assert.False(t, ok)

	_, ok = releaseMarkerEffect(nil)
	assert.False(t, ok)
}

func TestEffectsFromResult_AppendsReleaseMarkerAfterPromotions(t *testing.T) {
	t.Parallel()

	result := &promote.PromotionResult{
		Success: true,
		Mode:    promote.ModeDefault,
		Promotions: []promote.EnvPromotion{
			{Environment: "uat", SourceEnv: "dev", SHA: "a1b2c3d", Version: "v1.0.0-rc.0", NeedsDeploy: true},
		},
		ReleaseAction: "prerelease",
		ReleaseData:   &promote.ReleaseData{SHA: "a1b2c3d", RCVersion: "v1.0.0-rc.0", SemVersion: "v1.0.0"},
		SkippedEnvs:   []string{"prod"},
	}

	effects := effectsFromResult(result, nil)
	require.Len(t, effects, 4)
	assert.Equal(t, "deploy", effects[0].Action)
	assert.Equal(t, "write state", effects[1].Action)
	assert.Equal(t, "release prerelease", effects[2].Action)
	assert.Equal(t, DispositionSkip, effects[3].Disposition)
	assert.Equal(t, "prod", effects[3].Target)
}

func TestEffectsFromResult_SkippedEnvs(t *testing.T) {
	t.Parallel()

	result := &promote.PromotionResult{
		Success:     true,
		Mode:        promote.ModeDefault,
		SkippedEnvs: []string{"prod"},
	}

	effects := effectsFromResult(result, nil)
	require.Len(t, effects, 1)
	assert.Equal(t, DispositionSkip, effects[0].Disposition)
	assert.Equal(t, "prod", effects[0].Target)
}

func TestEffectsFromResult_OrderedPromotionsThenSkips(t *testing.T) {
	t.Parallel()

	result := &promote.PromotionResult{
		Success: true,
		Mode:    promote.ModeCascade,
		Promotions: []promote.EnvPromotion{
			{Environment: "uat", SourceEnv: "dev", SHA: "sha1", Version: "v1", NeedsDeploy: true},
			{Environment: "prod", SourceEnv: "uat", SHA: "sha1", Version: "v1", NeedsDeploy: true},
		},
		SkippedEnvs: []string{"sandbox"},
	}

	effects := effectsFromResult(result, nil)
	require.Len(t, effects, 5)

	assert.Equal(t, "deploy", effects[0].Action)
	assert.Equal(t, "uat", effects[0].Target)
	assert.Equal(t, "write state", effects[1].Action)
	assert.Equal(t, "uat", effects[1].Target)
	assert.Equal(t, "deploy", effects[2].Action)
	assert.Equal(t, "prod", effects[2].Target)
	assert.Equal(t, "write state", effects[3].Action)
	assert.Equal(t, "prod", effects[3].Target)
	assert.Equal(t, DispositionSkip, effects[4].Disposition)
	assert.Equal(t, "sandbox", effects[4].Target)
}
