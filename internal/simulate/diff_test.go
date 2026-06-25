package simulate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

func TestDiffState_FieldChanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		before    map[string]*config.EnvState
		after     map[string]*config.EnvState
		wantEnv   string
		assertion func(t *testing.T, d EnvDiff)
	}{
		{
			name: "version bump",
			before: map[string]*config.EnvState{
				"uat": {Version: "v1.0.0", SHA: "abc"},
			},
			after: map[string]*config.EnvState{
				"uat": {Version: "v1.1.0", SHA: "abc"},
			},
			wantEnv: "uat",
			assertion: func(t *testing.T, d EnvDiff) {
				t.Helper()
				assert.True(t, d.Version.Changed)
				assert.Equal(t, "v1.0.0", d.Version.From)
				assert.Equal(t, "v1.1.0", d.Version.To)
				assert.False(t, d.SHA.Changed)
			},
		},
		{
			name: "sha set from empty",
			before: map[string]*config.EnvState{
				"uat": {},
			},
			after: map[string]*config.EnvState{
				"uat": {SHA: "a1b2c3d"},
			},
			wantEnv: "uat",
			assertion: func(t *testing.T, d EnvDiff) {
				t.Helper()
				assert.True(t, d.SHA.Changed)
				assert.Equal(t, "(none)", d.SHA.From)
				assert.Equal(t, "a1b2c3d", d.SHA.To)
			},
		},
		{
			name: "per-deploy sha change",
			before: map[string]*config.EnvState{
				"uat": {Deploys: map[string]*config.DeployState{"api": {SHA: "old"}}},
			},
			after: map[string]*config.EnvState{
				"uat": {Deploys: map[string]*config.DeployState{"api": {SHA: "new", Version: "v2"}}},
			},
			wantEnv: "uat",
			assertion: func(t *testing.T, d EnvDiff) {
				t.Helper()
				require.Len(t, d.Deploys, 1)
				dd := d.Deploys[0]
				assert.Equal(t, "api", dd.Name)
				assert.True(t, dd.SHA.Changed)
				assert.Equal(t, "old", dd.SHA.From)
				assert.Equal(t, "new", dd.SHA.To)
				assert.True(t, dd.Version.Changed)
			},
		},
		{
			name: "divergence onset via ref",
			before: map[string]*config.EnvState{
				"prod": {SHA: "abc"},
			},
			after: map[string]*config.EnvState{
				"prod": {SHA: "abc", Ref: "hotfix/x"},
			},
			wantEnv: "prod",
			assertion: func(t *testing.T, d EnvDiff) {
				t.Helper()
				assert.True(t, d.Divergence.Changed)
				assert.Equal(t, "no", d.Divergence.From)
				assert.Equal(t, "yes", d.Divergence.To)
			},
		},
		{
			name: "divergence onset via patches",
			before: map[string]*config.EnvState{
				"prod": {SHA: "abc"},
			},
			after: map[string]*config.EnvState{
				"prod": {SHA: "abc", Patches: []string{"p1"}},
			},
			wantEnv: "prod",
			assertion: func(t *testing.T, d EnvDiff) {
				t.Helper()
				assert.True(t, d.Divergence.Changed)
			},
		},
		{
			name: "previous-ring growth",
			before: map[string]*config.EnvState{
				"uat": {Previous: []config.EnvStateSnapshot{{SHA: "x"}}},
			},
			after: map[string]*config.EnvState{
				"uat": {Previous: []config.EnvStateSnapshot{{SHA: "x"}, {SHA: "y"}}},
			},
			wantEnv: "uat",
			assertion: func(t *testing.T, d EnvDiff) {
				t.Helper()
				assert.True(t, d.PreviousRing.Changed)
				assert.Equal(t, "1", d.PreviousRing.From)
				assert.Equal(t, "2", d.PreviousRing.To)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			diff := DiffState(tt.before, tt.after)
			assert.True(t, diff.Changed(), "expected diff to report a change")
			d, ok := diff.Env(tt.wantEnv)
			require.True(t, ok, "expected env %q in diff", tt.wantEnv)
			tt.assertion(t, d)
		})
	}
}

func TestDiffState_IgnoresRunStampedTimestamps(t *testing.T) {
	t.Parallel()

	before := map[string]*config.EnvState{
		"uat": {SHA: "abc", Version: "v1", CommittedAt: "2026-01-01T00:00:00Z", CommittedBy: "alice"},
	}
	after := map[string]*config.EnvState{
		"uat": {SHA: "abc", Version: "v1", CommittedAt: "2026-06-25T12:00:00Z", CommittedBy: "bob"},
	}

	diff := DiffState(before, after)
	assert.False(t, diff.Changed(), "timestamp-only changes must not register as a diff")
}

func TestDiffState_NoChange(t *testing.T) {
	t.Parallel()

	before := map[string]*config.EnvState{"uat": {SHA: "abc", Version: "v1"}}
	after := map[string]*config.EnvState{"uat": {SHA: "abc", Version: "v1"}}

	diff := DiffState(before, after)
	assert.False(t, diff.Changed())
}
