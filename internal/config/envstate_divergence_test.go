package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEnvState_DivergenceFields_RoundTrip(t *testing.T) {
	original := &EnvState{
		SHA:     "abc123",
		Version: "v1.2.3",
		Ref:     "hotfix/v1.2.3-integration",
		BaseSHA: "base000",
		Patches: []string{"patch1sha", "patch2sha"},
	}

	t.Run("yaml", func(t *testing.T) {
		data, err := yaml.Marshal(original)
		require.NoError(t, err)

		var got EnvState
		require.NoError(t, yaml.Unmarshal(data, &got))

		assert.Equal(t, original.Ref, got.Ref)
		assert.Equal(t, original.BaseSHA, got.BaseSHA)
		assert.Equal(t, original.Patches, got.Patches)
		assert.True(t, got.IsDiverged())
	})

	t.Run("json", func(t *testing.T) {
		data, err := json.Marshal(original)
		require.NoError(t, err)

		var got EnvState
		require.NoError(t, json.Unmarshal(data, &got))

		assert.Equal(t, original.Ref, got.Ref)
		assert.Equal(t, original.BaseSHA, got.BaseSHA)
		assert.Equal(t, original.Patches, got.Patches)
		assert.True(t, got.IsDiverged())
	})
}

func TestEnvState_AbsentFieldsMeanTrackingTrunk(t *testing.T) {
	// A manifest that predates the divergence fields parses unchanged and is
	// not considered diverged.
	src := `
sha: abc123def456
version: v1.2.3-rc.1
committed_at: "2026-01-01T10:00:00Z"
committed_by: alice
`
	var state EnvState
	require.NoError(t, yaml.Unmarshal([]byte(src), &state))

	assert.Empty(t, state.Ref)
	assert.Empty(t, state.BaseSHA)
	assert.Empty(t, state.Patches)
	assert.False(t, state.IsDiverged())

	// omitempty: re-marshalling does not introduce the new keys.
	data, err := yaml.Marshal(&state)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ref:")
	assert.NotContains(t, string(data), "base_sha:")
	assert.NotContains(t, string(data), "patches:")
}

func TestEnvState_IsDiverged(t *testing.T) {
	tests := []struct {
		name  string
		state EnvState
		want  bool
	}{
		{name: "empty", state: EnvState{}, want: false},
		{name: "ref only", state: EnvState{Ref: "hotfix/x"}, want: true},
		{name: "patches only", state: EnvState{Patches: []string{"sha"}}, want: true},
		{name: "both", state: EnvState{Ref: "hotfix/x", Patches: []string{"sha"}}, want: true},
		{name: "base_sha only does not diverge", state: EnvState{BaseSHA: "base"}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.state.IsDiverged())
		})
	}
}
