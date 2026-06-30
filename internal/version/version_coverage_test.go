package version

import (
	"testing"

	"github.com/stablekernel/cascade/internal/changelog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculateNext_InvalidNextEnvVersion(t *testing.T) {
	calc := NewCalculator("v")
	_, err := calc.CalculateNext("", "not-a-version", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing next env version")
}

func TestCalculateNext_ChoreOnlyCommits(t *testing.T) {
	// Chore commits yield BumpNone; when commits are present BumpNone is promoted
	// to BumpPatch so that real work always produces a version increment.
	calc := NewCalculator("v")
	commits := []changelog.ConventionalCommit{
		{Type: "chore", Description: "update dependencies"},
	}
	got, err := calc.CalculateNext("", "v1.0.0", commits)
	require.NoError(t, err)
	assert.Equal(t, "v1.0.1-rc.0", got.String())
}

func TestCalculateNext_UnparseableCurrentDevVersion(t *testing.T) {
	// If currentDevVersion is non-empty but cannot be parsed, the RC counter
	// resets to 0 rather than erroring.
	calc := NewCalculator("v")
	commits := []changelog.ConventionalCommit{
		{Type: "feat", Description: "new feature"},
	}
	got, err := calc.CalculateNext("not-a-version", "v1.0.0", commits)
	require.NoError(t, err)
	assert.Equal(t, 0, got.PreRelease)
}

func TestCalculateNext_NoCommitsNoNextEnv(t *testing.T) {
	// Zero commits and no next env version: no bump, but minimum v0.1.0 rule applies.
	calc := NewCalculator("v")
	got, err := calc.CalculateNext("", "", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, got.Major)
	assert.Equal(t, 1, got.Minor)
	assert.Equal(t, 0, got.PreRelease)
}

func TestVersion_Compare_ReleaseVsPreRelease(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{
			name: "pre-release sorts before its release",
			a:    "v1.0.0-rc.0",
			b:    "v1.0.0",
			want: -1,
		},
		{
			name: "release sorts after any pre-release",
			a:    "v1.0.0",
			b:    "v1.0.0-rc.0",
			want: 1,
		},
		{
			name: "two equal releases",
			a:    "v1.0.0",
			b:    "v1.0.0",
			want: 0,
		},
		{
			name: "same rc without hotfix is equal to itself",
			a:    "v1.0.0-rc.2",
			b:    "v1.0.0-rc.2",
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			va := mustParse(t, tt.a)
			vb := mustParse(t, tt.b)
			assert.Equal(t, tt.want, va.Compare(vb))
		})
	}
}

func TestVersion_Compare_MajorMinorPatch(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{"major greater", "v2.0.0", "v1.0.0", 1},
		{"major lesser", "v1.0.0", "v2.0.0", -1},
		{"minor greater", "v1.2.0", "v1.1.0", 1},
		{"minor lesser", "v1.1.0", "v1.2.0", -1},
		{"patch greater", "v1.0.2", "v1.0.1", 1},
		{"patch lesser", "v1.0.1", "v1.0.2", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			va := mustParse(t, tt.a)
			vb := mustParse(t, tt.b)
			assert.Equal(t, tt.want, va.Compare(vb))
		})
	}
}
