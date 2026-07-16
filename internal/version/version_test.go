package version

import (
	"testing"

	"github.com/stablekernel/cascade/internal/changelog"
	"github.com/stablekernel/cascade/internal/taggrammar"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      *Version
		wantError bool
	}{
		{
			name:  "simple version with v prefix",
			input: "v1.2.3",
			want: &Version{
				Major:      1,
				Minor:      2,
				Patch:      3,
				PreRelease: -1,
				Hotfix:     -1,
				Prefix:     "v",
			},
		},
		{
			name:  "version with RC",
			input: "v1.2.3-rc.4",
			want: &Version{
				Major:      1,
				Minor:      2,
				Patch:      3,
				PreRelease: 4,
				Hotfix:     -1,
				Prefix:     "v",
			},
		},
		{
			name:  "version with RC 0",
			input: "v2.0.0-rc.0",
			want: &Version{
				Major:      2,
				Minor:      0,
				Patch:      0,
				PreRelease: 0,
				Hotfix:     -1,
				Prefix:     "v",
			},
		},
		{
			name:  "version without prefix",
			input: "1.2.3",
			want: &Version{
				Major:      1,
				Minor:      2,
				Patch:      3,
				PreRelease: -1,
				Hotfix:     -1,
				Prefix:     "",
			},
		},
		{
			name:  "version without prefix with RC",
			input: "1.2.3-rc.5",
			want: &Version{
				Major:      1,
				Minor:      2,
				Patch:      3,
				PreRelease: 5,
				Hotfix:     -1,
				Prefix:     "",
			},
		},
		{
			name:      "invalid format",
			input:     "not-a-version",
			wantError: true,
		},
		{
			name:      "partial version",
			input:     "v1.2",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if tt.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestVersion_String(t *testing.T) {
	tests := []struct {
		name    string
		version *Version
		want    string
	}{
		{
			name: "with RC",
			version: &Version{
				Major: 1, Minor: 2, Patch: 3, PreRelease: 4, Hotfix: -1, Prefix: "v",
			},
			want: "v1.2.3-rc.4",
		},
		{
			name: "without RC",
			version: &Version{
				Major: 1, Minor: 2, Patch: 3, PreRelease: -1, Hotfix: -1, Prefix: "v",
			},
			want: "v1.2.3",
		},
		{
			name: "RC 0",
			version: &Version{
				Major: 2, Minor: 0, Patch: 0, PreRelease: 0, Hotfix: -1, Prefix: "v",
			},
			want: "v2.0.0-rc.0",
		},
		{
			name: "with RC and hotfix",
			version: &Version{
				Major: 1, Minor: 4, Patch: 0, PreRelease: 2, Hotfix: 1, Prefix: "v",
			},
			want: "v1.4.0-rc.2.hotfix.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.version.String())
		})
	}
}

func TestVersion_Bump(t *testing.T) {
	base := &Version{Major: 1, Minor: 2, Patch: 3, PreRelease: -1, Prefix: "v"}

	t.Run("major bump", func(t *testing.T) {
		result := base.Bump(BumpMajor)
		assert.Equal(t, "v2.0.0", result.String())
	})

	t.Run("minor bump", func(t *testing.T) {
		result := base.Bump(BumpMinor)
		assert.Equal(t, "v1.3.0", result.String())
	})

	t.Run("patch bump", func(t *testing.T) {
		result := base.Bump(BumpPatch)
		assert.Equal(t, "v1.2.4", result.String())
	})

	t.Run("no bump", func(t *testing.T) {
		result := base.Bump(BumpNone)
		assert.Equal(t, "v1.2.3", result.String())
	})
}

func TestDetermineBumpType(t *testing.T) {
	tests := []struct {
		name    string
		commits []changelog.ConventionalCommit
		want    BumpType
	}{
		{
			name:    "no commits",
			commits: nil,
			want:    BumpNone,
		},
		{
			name: "fix only",
			commits: []changelog.ConventionalCommit{
				{Type: "fix", Description: "bug fix"},
			},
			want: BumpPatch,
		},
		{
			name: "feat only",
			commits: []changelog.ConventionalCommit{
				{Type: "feat", Description: "new feature"},
			},
			want: BumpMinor,
		},
		{
			name: "breaking change",
			commits: []changelog.ConventionalCommit{
				{Type: "feat", Description: "new feature", Breaking: true},
			},
			want: BumpMajor,
		},
		{
			name: "mixed - feat wins over fix",
			commits: []changelog.ConventionalCommit{
				{Type: "fix", Description: "bug fix"},
				{Type: "feat", Description: "new feature"},
				{Type: "fix", Description: "another fix"},
			},
			want: BumpMinor,
		},
		{
			name: "mixed - breaking wins over feat",
			commits: []changelog.ConventionalCommit{
				{Type: "feat", Description: "new feature"},
				{Type: "fix", Description: "bug fix", Breaking: true},
			},
			want: BumpMajor,
		},
		{
			name: "chore and docs are ignored",
			commits: []changelog.ConventionalCommit{
				{Type: "chore", Description: "update deps"},
				{Type: "docs", Description: "update readme"},
			},
			want: BumpNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetermineBumpType(tt.commits)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCalculator_CalculateNext(t *testing.T) {
	calc := NewCalculator("v")

	tests := []struct {
		name              string
		currentDevVersion string
		nextEnvVersion    string
		commits           []changelog.ConventionalCommit
		want              string
	}{
		{
			name:              "first release - feat",
			currentDevVersion: "",
			nextEnvVersion:    "",
			commits: []changelog.ConventionalCommit{
				{Type: "feat", Description: "initial feature"},
			},
			want: "v0.1.0-rc.0",
		},
		{
			name:              "after promotion - new feat",
			currentDevVersion: "v2.0.0-rc.0",
			nextEnvVersion:    "v2.0.0-rc.0", // matching = just promoted
			commits: []changelog.ConventionalCommit{
				{Type: "feat", Description: "new feature"},
			},
			want: "v2.1.0-rc.0",
		},
		{
			name:              "same version - increment RC",
			currentDevVersion: "v2.1.0-rc.0",
			nextEnvVersion:    "v2.0.0-rc.0",
			commits: []changelog.ConventionalCommit{
				{Type: "feat", Description: "feature"},
				{Type: "fix", Description: "fix"},
			},
			want: "v2.1.0-rc.1", // same base version, increment RC
		},
		{
			name:              "same version - increment RC again",
			currentDevVersion: "v2.1.0-rc.2",
			nextEnvVersion:    "v2.0.0-rc.0",
			commits: []changelog.ConventionalCommit{
				{Type: "feat", Description: "feature"},
			},
			want: "v2.1.0-rc.3",
		},
		{
			name:              "breaking change - major bump",
			currentDevVersion: "v2.1.0-rc.0",
			nextEnvVersion:    "v2.0.0-rc.0",
			commits: []changelog.ConventionalCommit{
				{Type: "feat", Description: "breaking feature", Breaking: true},
			},
			want: "v3.0.0-rc.0", // different version, RC resets
		},
		{
			name:              "fix only - patch bump",
			currentDevVersion: "v2.0.0-rc.0",
			nextEnvVersion:    "v2.0.0-rc.0",
			commits: []changelog.ConventionalCommit{
				{Type: "fix", Description: "bug fix"},
			},
			want: "v2.0.1-rc.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := calc.CalculateNext(tt.currentDevVersion, tt.nextEnvVersion, tt.commits)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
		})
	}
}

func TestVersion_WithRC(t *testing.T) {
	base := &Version{Major: 1, Minor: 2, Patch: 3, PreRelease: -1, Prefix: "v"}

	t.Run("add RC to version without RC", func(t *testing.T) {
		result := base.WithRC(0)
		assert.Equal(t, "v1.2.3-rc.0", result.String())
	})

	t.Run("change RC number", func(t *testing.T) {
		withRC := base.WithRC(5)
		result := withRC.WithRC(10)
		assert.Equal(t, "v1.2.3-rc.10", result.String())
	})

	t.Run("preserves original", func(t *testing.T) {
		_ = base.WithRC(3)
		assert.Equal(t, "v1.2.3", base.String()) // Original unchanged
	})
}

func TestVersion_Equal(t *testing.T) {
	v1 := &Version{Major: 1, Minor: 2, Patch: 3, PreRelease: -1, Prefix: "v"}
	v2 := &Version{Major: 1, Minor: 2, Patch: 3, PreRelease: 5, Prefix: "v"}
	v3 := &Version{Major: 1, Minor: 2, Patch: 4, PreRelease: -1, Prefix: "v"}

	t.Run("same version different RC", func(t *testing.T) {
		assert.True(t, v1.Equal(v2))
	})

	t.Run("different patch", func(t *testing.T) {
		assert.False(t, v1.Equal(v3))
	})

	t.Run("nil comparison", func(t *testing.T) {
		assert.False(t, v1.Equal(nil))
	})
}

func TestVersion_Base(t *testing.T) {
	v := &Version{Major: 1, Minor: 2, Patch: 3, PreRelease: 4, Prefix: "v"}
	assert.Equal(t, "v1.2.3", v.Base())
}

func TestVersion_BaseVersion(t *testing.T) {
	v := &Version{Major: 1, Minor: 2, Patch: 3, PreRelease: 4, Prefix: "v"}
	base := v.BaseVersion()

	assert.Equal(t, "v1.2.3", base.String())
	assert.Equal(t, -1, base.PreRelease)
	// Original unchanged
	assert.Equal(t, 4, v.PreRelease)
}

func TestNewCalculator_CustomPrefix(t *testing.T) {
	calc := NewCalculator("release-")

	commits := []changelog.ConventionalCommit{
		{Type: "feat", Description: "new feature"},
	}

	got, err := calc.CalculateNext("", "", commits)
	require.NoError(t, err)
	assert.Equal(t, "release-0.1.0-rc.0", got.String())
}

func TestNewCalculator_EmptyPrefixDefaultsToV(t *testing.T) {
	// Empty prefix defaults to "v"
	calc := NewCalculator("")

	commits := []changelog.ConventionalCommit{
		{Type: "feat", Description: "new feature"},
	}

	got, err := calc.CalculateNext("", "", commits)
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0-rc.0", got.String())
}

func TestParse_HotfixSegment(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  *Version
	}{
		{
			name:  "hotfix with v prefix",
			input: "v1.4.0-rc.2.hotfix.1",
			want: &Version{
				Major:      1,
				Minor:      4,
				Patch:      0,
				PreRelease: 2,
				Hotfix:     1,
				Prefix:     "v",
			},
		},
		{
			name:  "hotfix without prefix",
			input: "1.4.0-rc.2.hotfix.3",
			want: &Version{
				Major:      1,
				Minor:      4,
				Patch:      0,
				PreRelease: 2,
				Hotfix:     3,
				Prefix:     "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			// Round-trip back to the same string.
			assert.Equal(t, tt.input, got.String())
		})
	}
}

func TestParse_RejectsMalformedHotfix(t *testing.T) {
	inputs := []string{
		"v1.4.0-hotfix.1",      // hotfix without rc
		"v1.4.0-rc.2.hotfix",   // no number
		"v1.4.0-rc.2-hotfix.1", // wrong separator
		"v1.4.0-rc.2.hotfix.x", // non-numeric
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			_, err := Parse(input)
			assert.Error(t, err)
		})
	}
}

func TestVersion_Ordering_HotfixBetweenRCs(t *testing.T) {
	rc2 := mustParse(t, "v1.4.0-rc.2")
	rc2hf1 := mustParse(t, "v1.4.0-rc.2.hotfix.1")
	rc2hf2 := mustParse(t, "v1.4.0-rc.2.hotfix.2")
	rc3 := mustParse(t, "v1.4.0-rc.3")

	assert.Equal(t, -1, rc2.Compare(rc2hf1), "rc.2 < rc.2.hotfix.1")
	assert.Equal(t, -1, rc2hf1.Compare(rc2hf2), "rc.2.hotfix.1 < rc.2.hotfix.2")
	assert.Equal(t, -1, rc2hf2.Compare(rc3), "rc.2.hotfix.2 < rc.3")

	// Symmetry and equality.
	assert.Equal(t, 1, rc2hf1.Compare(rc2), "rc.2.hotfix.1 > rc.2")
	assert.Equal(t, 0, rc2hf1.Compare(mustParse(t, "v1.4.0-rc.2.hotfix.1")))
}

func TestCalculateNext_NextEnvHoldsHotfixVersion(t *testing.T) {
	calc := NewCalculator("v")

	commits := []changelog.ConventionalCommit{
		{Type: "fix", Description: "bug fix"},
	}

	got, err := calc.CalculateNext("", "v1.4.0-rc.2.hotfix.1", commits)
	require.NoError(t, err)

	// Computed from the rc base v1.4.0, ignoring the hotfix segment.
	assert.Equal(t, "v1.4.1-rc.0", got.String())
	assert.Equal(t, -1, got.Hotfix)
}

func TestParseBaseWithGrammar(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string // Version.String() of the parsed base, or "" when wantErr
		wantErr bool
	}{
		{name: "plain release", input: "v1.2.3", want: "v1.2.3"},
		{name: "rc suffix dropped", input: "v1.2.3-rc.4", want: "v1.2.3"},
		{name: "hotfix suffix dropped", input: "v1.4.0-rc.2.hotfix.1", want: "v1.4.0"},
		{name: "dryrun suffix dropped", input: "v0.6.0-dryrun.13", want: "v0.6.0"},
		{name: "opaque suffix dropped", input: "v2.0.0-beta.1", want: "v2.0.0"},
		{name: "no prefix", input: "3.4.5-dryrun.1", want: "3.4.5"},
		{name: "not a triple", input: "vnightly", wantErr: true},
		{name: "empty", input: "", wantErr: true},
		{name: "two segments", input: "v1.2-dryrun.1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBaseWithGrammar(defaultSpec, tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
			assert.Equal(t, -1, got.PreRelease)
			assert.Equal(t, -1, got.Hotfix)
		})
	}
}

func TestCalculateNext_NextEnvHoldsDryrunVersion(t *testing.T) {
	calc := NewCalculator("v")

	commits := []changelog.ConventionalCommit{
		{Type: "fix", Description: "bug fix"},
	}

	tests := []struct {
		name              string
		currentDevVersion string
		nextEnvVersion    string
		want              string
	}{
		{
			// A dry-run vector leaves a v0.6.0-dryrun.13 value in state; a
			// later real release must still compute the next version from the
			// v0.6.0 base rather than aborting the whole calculation.
			name:              "dryrun latest, fresh release",
			currentDevVersion: "",
			nextEnvVersion:    "v0.6.0-dryrun.13",
			want:              "v0.6.1-rc.0",
		},
		{
			name:              "dryrun latest, just promoted",
			currentDevVersion: "v0.6.0-dryrun.13",
			nextEnvVersion:    "v0.6.0-dryrun.13",
			want:              "v0.6.1-rc.0",
		},
		{
			name:              "opaque prerelease suffix tolerated",
			currentDevVersion: "",
			nextEnvVersion:    "v1.2.3-beta.4",
			want:              "v1.2.4-rc.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := calc.CalculateNext(tt.currentDevVersion, tt.nextEnvVersion, commits)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
			assert.Equal(t, -1, got.Hotfix)
		})
	}
}

func TestCalculateNext_CurrentDevHoldsForeignSuffix(t *testing.T) {
	calc := NewCalculator("v")

	commits := []changelog.ConventionalCommit{
		{Type: "fix", Description: "bug fix"},
	}

	tests := []struct {
		name              string
		currentDevVersion string
		nextEnvVersion    string
		want              string
	}{
		{
			// The dev version carries a real rc plus a trailing exercise
			// suffix the strict Parse rejects. The rc counter must still
			// increment off the recorded rc rather than silently restarting
			// at rc.0 and colliding with an already-published rc tag.
			name:              "rc with trailing exercise suffix increments",
			currentDevVersion: "v1.2.3-rc.4.dryrun.1",
			nextEnvVersion:    "v1.2.2",
			want:              "v1.2.3-rc.5",
		},
		{
			// A foreign prerelease with no rc segment and a base matching the
			// computed version starts a fresh rc.0 without aborting.
			name:              "foreign prerelease without rc starts fresh",
			currentDevVersion: "v1.2.3-beta.1",
			nextEnvVersion:    "v1.2.2",
			want:              "v1.2.3-rc.0",
		},
		{
			// A foreign-suffixed dev version whose base differs from the
			// computed version starts a fresh rc.0.
			name:              "foreign suffix different base starts fresh",
			currentDevVersion: "v1.1.0-dryrun.7",
			nextEnvVersion:    "v1.2.2",
			want:              "v1.2.3-rc.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := calc.CalculateNext(tt.currentDevVersion, tt.nextEnvVersion, commits)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
			assert.Equal(t, -1, got.Hotfix)
		})
	}
}

func TestVersion_WithHotfix(t *testing.T) {
	base := mustParse(t, "v1.4.0-rc.2")

	t.Run("sets hotfix preserving rc", func(t *testing.T) {
		result := base.WithHotfix(3)
		assert.Equal(t, "v1.4.0-rc.2.hotfix.3", result.String())
	})

	t.Run("preserves original", func(t *testing.T) {
		_ = base.WithHotfix(3)
		assert.Equal(t, "v1.4.0-rc.2", base.String())
	})
}

func TestVersion_NextHotfix(t *testing.T) {
	t.Run("first hotfix on plain rc is hotfix.1", func(t *testing.T) {
		base := mustParse(t, "v1.4.0-rc.2")
		assert.Equal(t, "v1.4.0-rc.2.hotfix.1", base.NextHotfix().String())
	})

	t.Run("increments existing hotfix", func(t *testing.T) {
		base := mustParse(t, "v1.4.0-rc.2.hotfix.1")
		assert.Equal(t, "v1.4.0-rc.2.hotfix.2", base.NextHotfix().String())
	})

	t.Run("preserves original", func(t *testing.T) {
		base := mustParse(t, "v1.4.0-rc.2")
		_ = base.NextHotfix()
		assert.Equal(t, "v1.4.0-rc.2", base.String())
	})
}

// TestCalculator_CustomGrammarEmitsCustomShape proves the calculator emits the
// pre-release shape from its grammar rather than a hard-wired "-rc.". A beta
// token with an empty separator must yield "-beta0", not "-rc.0".
func TestCalculator_CustomGrammarEmitsCustomShape(t *testing.T) {
	spec := taggrammar.Default()
	spec.PreReleaseToken = "beta"
	spec.PreReleaseSeparator = ""

	calc := NewCalculatorWithGrammar(spec)
	got, err := calc.CalculateNext("", "", []changelog.ConventionalCommit{
		{Type: "feat", Description: "initial feature"},
	})
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0-beta0", got.String())
}

func mustParse(t *testing.T, s string) *Version {
	t.Helper()
	v, err := Parse(s)
	require.NoError(t, err)
	return v
}
