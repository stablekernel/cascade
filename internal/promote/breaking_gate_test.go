package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// TestCheckBreakingChangesForMode_ManifestGate proves the manifest field
// allow_breaking_changes disables the breaking-change gate for the whole
// repository. With the field unset (the default) a feat!: commit crossing the
// pre-release to release boundary still blocks; with the field set the same
// crossing proceeds, matching the per-run --allow-breaking override applied
// once for the repo.
func TestCheckBreakingChangesForMode_ManifestGate(t *testing.T) {
	// Real git repo: a base commit, then a breaking commit as HEAD. The env
	// state SHA points at the base so the range (base, head] carries the
	// breaking commit.
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one"), 0o644))
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-m", "chore: base")
	baseSHA := gitOut(t, dir, "rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two"), 0o644))
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-m", "feat!: drop legacy API")
	headSHA := gitOut(t, dir, "rev-parse", "HEAD")

	// git.GetCommits runs in the process working directory.
	t.Chdir(dir)

	newPreflighter := func(allow bool) *Preflighter {
		return NewPreflighter(PreflighterOptions{
			Config: &config.CICDFile{
				Config: &config.TrunkConfig{
					Environments:         []string{"staging"},
					AllowBreakingChanges: allow,
				},
				State: map[string]*config.EnvState{
					"release": {SHA: baseSHA},
				},
			},
			Mode: ModeDefault,
		})
	}

	promotions := []EnvPromotion{{Environment: "release", SHA: headSHA}}

	t.Run("gate enabled by default blocks a breaking crossing", func(t *testing.T) {
		pf := newPreflighter(false)
		hasBreaking, blockedAt := pf.checkBreakingChangesForMode(promotions, "staging", "prod", true)
		require.True(t, hasBreaking, "a feat!: commit must block the pre-release to release crossing")
		require.Equal(t, "staging → release", blockedAt)
	})

	t.Run("manifest field disables the gate", func(t *testing.T) {
		pf := newPreflighter(true)
		hasBreaking, blockedAt := pf.checkBreakingChangesForMode(promotions, "staging", "prod", true)
		require.False(t, hasBreaking, "allow_breaking_changes must let the crossing proceed")
		require.Empty(t, blockedAt)
	})
}
