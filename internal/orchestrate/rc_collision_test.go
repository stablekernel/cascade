package orchestrate

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// TestCalculateVersion_RCCollisionAdvancesPastExistingTag is the regression
// guard for the frozen-rc incident: a stale recorded state drove the rc counter
// to a value that already existed as a tag at a different commit, and the cut
// reused it instead of advancing. With state stuck at rc.0 the calculation
// recomputes rc.1; when a v1.2.0-rc.1 tag already points at an earlier commit,
// the result must advance to rc.2 rather than collide. The control subcase (no
// existing tag) proves the no-collision path is unchanged and still yields rc.1.
func TestCalculateVersion_RCCollisionAdvancesPastExistingTag(t *testing.T) {
	tests := []struct {
		name        string
		collideAt   string // git revision to tag v1.2.0-rc.1 at, or "" for no tag
		wantVersion string
	}{
		{name: "existing rc.1 at a different commit advances to rc.2", collideAt: "HEAD~1", wantVersion: "v1.2.0-rc.2"},
		{name: "no collision keeps rc.1 unchanged", collideAt: "", wantVersion: "v1.2.0-rc.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir, head := initRepo(t)

			if tt.collideAt != "" {
				// Materialize the colliding rc tag at a commit that is NOT HEAD, so
				// the guard sees it as a foreign target rather than an idempotent
				// re-cut of the same commit.
				runGit(t, repoDir, "tag", "v1.2.0-rc.1", tt.collideAt)
			}

			// calculateVersion resolves commit ranges through the process working
			// directory, so run from the repo like the other orchestrate git tests.
			orig, err := os.Getwd()
			require.NoError(t, err)
			require.NoError(t, os.Chdir(repoDir))
			t.Cleanup(func() { require.NoError(t, os.Chdir(orig)) })

			o := &Orchestrator{
				environment: "dev",
				baseDir:     repoDir,
				cicdFile: &config.CICDFile{
					Config: &config.TrunkConfig{
						Environments: config.EnvNames("dev", "prod"),
					},
					State: map[string]*config.EnvState{
						// Stuck at rc.0: the recomputed candidate is rc.1.
						"dev":  {Version: "v1.2.0-rc.0"},
						"prod": {Version: "v1.2.0", SHA: head},
					},
				},
			}

			got, err := o.calculateVersion()
			require.NoError(t, err)
			assert.Equal(t, tt.wantVersion, got)
		})
	}
}
