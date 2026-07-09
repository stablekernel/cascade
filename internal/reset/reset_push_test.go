package reset

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// TestCommitAndPush_StateCommitCarriesSkipCIMarker drives the runtime reset commit
// path and asserts the landed commit subject carries the [skip ci] marker. The
// marker suppresses CI on the state commit; a regression dropping it would let
// every reset re-trigger the pipeline. The runtime emission was previously
// asserted nowhere.
func TestCommitAndPush_StateCommitCarriesSkipCIMarker(t *testing.T) {
	const relPath = "manifest.yaml"

	origin := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	const seed = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
  state:
    dev:
      sha: seedsha
      version: v1.0.0
`
	bootstrap := t.TempDir()
	gitInDir(t, "", "clone", origin, bootstrap)
	configureGitIdentity(t, bootstrap)
	require.NoError(t, os.WriteFile(filepath.Join(bootstrap, relPath), []byte(seed), 0o600))
	gitInDir(t, bootstrap, "add", relPath)
	gitInDir(t, bootstrap, "commit", "-m", "seed manifest")
	gitInDir(t, bootstrap, "push", "origin", "HEAD:main")

	resetRepo := t.TempDir()
	gitInDir(t, "", "clone", origin, resetRepo)
	configureGitIdentity(t, resetRepo)

	configPath := filepath.Join(resetRepo, relPath)
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	r := &Resetter{
		opts:        Options{RepoPath: resetRepo, ResetState: true, Push: true},
		repoPath:    resetRepo,
		repoOwner:   "test",
		repoName:    "repo",
		configPath:  configPath,
		manifestKey: config.DefaultManifestKey,
		cicdFile:    cicdFile,
	}

	// The reset clears state on disk, producing a diff for commitAndPush to land.
	require.NoError(t, r.resetState())
	require.NoError(t, r.commitAndPush())

	out, err := exec.Command("git", "-C", origin, "log", "-1", "--format=%B", "main").Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "[skip ci]",
		"runtime reset state commit must carry [skip ci] to suppress CI on the state write")
	assert.Equal(t, resetCommitMessage, strings.TrimSpace(string(out)),
		"reset commit subject must be the skip-ci state message")
}

// gitInDir runs a git command scoped to dir (matching how the Resetter scopes
// every git invocation to r.repoPath) and fails the test on error.
func gitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// configureGitIdentity sets a committer identity and disables signing so commits
// succeed in a bare CI sandbox without a configured user or GPG key.
func configureGitIdentity(t *testing.T, dir string) {
	t.Helper()
	gitInDir(t, dir, "config", "user.email", "tester@example.com")
	gitInDir(t, dir, "config", "user.name", "Reset Tester")
	gitInDir(t, dir, "config", "commit.gpgsign", "false")
}

// originHeadManifest returns the manifest contents at the origin's main tip.
func originHeadManifest(t *testing.T, origin, relPath string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", origin, "show", "main:"+relPath).Output()
	if err != nil {
		t.Fatalf("git show main:%s: %v", relPath, err)
	}
	return string(out)
}

// TestCommitAndPush_RecoversFromNonFastForward proves the seeding race that broke
// the multi-environment lifecycle job: a concurrent writer advances origin/main
// between the reset's read and its push, so a plain push is rejected
// non-fast-forward. commitAndPush must fetch, re-apply the baseline reset on top
// of the updated trunk, and push. The baseline reset must WIN, leaving the
// concurrent writer's state overwritten rather than merged back in.
func TestCommitAndPush_RecoversFromNonFastForward(t *testing.T) {
	const relPath = "manifest.yaml"

	// Bare origin shared by the reset checkout and the concurrent writer.
	origin := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	// Seed origin with a manifest that already carries state. This is the value
	// the reset checkout reads before the race.
	const seed = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
  state:
    dev:
      sha: seedsha
      version: v1.0.0
`
	bootstrap := t.TempDir()
	gitInDir(t, "", "clone", origin, bootstrap)
	configureGitIdentity(t, bootstrap)
	require.NoError(t, os.WriteFile(filepath.Join(bootstrap, relPath), []byte(seed), 0o600))
	gitInDir(t, bootstrap, "add", relPath)
	gitInDir(t, bootstrap, "commit", "-m", "seed manifest")
	gitInDir(t, bootstrap, "push", "origin", "HEAD:main")

	// The reset checkout: this is the repo under test, scoped via repoPath.
	resetRepo := t.TempDir()
	gitInDir(t, "", "clone", origin, resetRepo)
	configureGitIdentity(t, resetRepo)

	configPath := filepath.Join(resetRepo, relPath)
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	r := &Resetter{
		opts: Options{
			RepoPath:   resetRepo,
			ResetState: true,
			Push:       true,
		},
		repoPath:    resetRepo,
		repoOwner:   "test",
		repoName:    "repo",
		configPath:  configPath,
		manifestKey: config.DefaultManifestKey,
		cicdFile:    cicdFile,
	}

	// The reset clears state on disk, mirroring Reset() calling resetState()
	// before commitAndPush().
	require.NoError(t, r.resetState())

	// A concurrent writer advances origin/main AFTER the reset read its copy,
	// writing a fresh promotion into the same state section. The reset checkout's
	// parent is now stale, so its next push is rejected non-fast-forward.
	writer := t.TempDir()
	gitInDir(t, "", "clone", origin, writer)
	configureGitIdentity(t, writer)
	const writerManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
  state:
    dev:
      sha: writersha
      version: v2.0.0
`
	require.NoError(t, os.WriteFile(filepath.Join(writer, relPath), []byte(writerManifest), 0o600))
	gitInDir(t, writer, "add", relPath)
	gitInDir(t, writer, "commit", "-m", "concurrent promote")
	gitInDir(t, writer, "push", "origin", "HEAD:main")

	// commitAndPush must self-heal across the rejected push.
	if err := r.commitAndPush(); err != nil {
		t.Fatalf("commitAndPush() error = %v, want nil (must recover from non-fast-forward)", err)
	}

	// The baseline reset must win: origin's tip carries neither the seed state nor
	// the concurrent writer's state.
	got := originHeadManifest(t, origin, relPath)
	if strings.Contains(got, "writersha") {
		t.Errorf("reset did not win: origin HEAD still carries concurrent writer state; got:\n%s", got)
	}
	if strings.Contains(got, "seedsha") {
		t.Errorf("origin HEAD still carries the pre-reset seed state; got:\n%s", got)
	}
	if strings.Contains(got, "state:") {
		t.Errorf("state section was not reset to baseline; got:\n%s", got)
	}
	// The non-state config the writer left in place must survive the rebase.
	if !strings.Contains(got, "trunk_branch: main") {
		t.Errorf("origin HEAD lost config section after recovery; got:\n%s", got)
	}

	_ = bootstrap
}
