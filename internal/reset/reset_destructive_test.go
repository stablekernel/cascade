package reset

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGH installs a scripted gh CLI on PATH that records every invocation (one
// line of argv per call) and serves canned release data, so the destructive
// delete-all-releases path can be proven hermetically. The reset command drives
// the real gh binary against the GitHub API, which the act+gitea e2e harness
// has no compatible host for, so this recorded-runner level is where the
// deletion loop is verified.
//
// Behavior is driven by environment variables:
//   - RESET_GH_LOG:       file the stub appends each invocation's argv to
//   - RESET_GH_RELEASES:  file whose contents "gh release list" prints
//   - RESET_GH_LIST_FAIL: when non-empty, "gh release list" exits 1
//   - RESET_GH_FAIL_TAG:  "gh release delete <tag>" exits 1 for this tag
func fakeGH(t *testing.T, releases string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the scripted gh stub needs a POSIX shell")
	}

	binDir := t.TempDir()
	dataDir := t.TempDir()

	logPath := filepath.Join(dataDir, "invocations.log")
	releasesPath := filepath.Join(dataDir, "releases.txt")
	require.NoError(t, os.WriteFile(releasesPath, []byte(releases), 0o600))

	script := `#!/bin/sh
printf '%s\n' "$*" >> "$RESET_GH_LOG"
case "$*" in
  *"release list"*)
    if [ -n "$RESET_GH_LIST_FAIL" ]; then exit 1; fi
    cat "$RESET_GH_RELEASES"
    ;;
  *"release delete ${RESET_GH_FAIL_TAG:-<unset>} "*)
    exit 1
    ;;
esac
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755))

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RESET_GH_LOG", logPath)
	t.Setenv("RESET_GH_RELEASES", releasesPath)
	return logPath
}

// ghInvocations reads the stub's recorded argv lines.
func ghInvocations(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// deleteInvocations filters the recorded gh calls down to release deletions.
func deleteInvocations(calls []string) []string {
	var deletes []string
	for _, call := range calls {
		if strings.Contains(call, "release delete") {
			deletes = append(deletes, call)
		}
	}
	return deletes
}

func TestDeleteAllReleases_NoReleases(t *testing.T) {
	logPath := fakeGH(t, "")
	dir := newResetRepo(t)

	r, err := New(Options{RepoPath: dir})
	require.NoError(t, err)

	deleted, err := r.deleteAllReleases()
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)

	calls := ghInvocations(t, logPath)
	require.Len(t, calls, 1, "an empty repo needs exactly one gh call (the list)")
	assert.Contains(t, calls[0], "release list")
	assert.Empty(t, deleteInvocations(calls))
}

func TestDeleteAllReleases_DryRunDeletesNothing(t *testing.T) {
	logPath := fakeGH(t, "v1.0.0\nv1.1.0\n")
	dir := newResetRepo(t)

	r, err := New(Options{RepoPath: dir, DryRun: true})
	require.NoError(t, err)

	deleted, err := r.deleteAllReleases()
	require.NoError(t, err)
	assert.Equal(t, 2, deleted, "dry-run reports the count it would delete")

	assert.Empty(t, deleteInvocations(ghInvocations(t, logPath)),
		"dry-run must never invoke gh release delete")
}

func TestDeleteAllReleases_DeletesEveryReleaseInResolvedRepo(t *testing.T) {
	logPath := fakeGH(t, "v1.0.0\nv1.1.0\nv2.0.0-rc.1\n")
	dir := newResetRepo(t)

	r, err := New(Options{RepoPath: dir})
	require.NoError(t, err)

	deleted, err := r.deleteAllReleases()
	require.NoError(t, err)
	assert.Equal(t, 3, deleted)

	// Every gh call must be pinned to the repo resolved from the origin remote
	// (-R owner/name), so the wipe can never land on a default/ambient repo.
	calls := ghInvocations(t, logPath)
	for _, call := range calls {
		assert.True(t, strings.HasPrefix(call, "-R test/repo "),
			"gh call not pinned to the resolved repo: %q", call)
	}

	deletes := deleteInvocations(calls)
	require.Len(t, deletes, 3)
	assert.Equal(t, "-R test/repo release delete v1.0.0 --yes", deletes[0])
	assert.Equal(t, "-R test/repo release delete v1.1.0 --yes", deletes[1])
	assert.Equal(t, "-R test/repo release delete v2.0.0-rc.1 --yes", deletes[2])
}

func TestDeleteAllReleases_ContinuesPastSingleFailure(t *testing.T) {
	logPath := fakeGH(t, "v1.0.0\nv1.1.0\nv2.0.0\n")
	t.Setenv("RESET_GH_FAIL_TAG", "v1.1.0")
	dir := newResetRepo(t)

	r, err := New(Options{RepoPath: dir})
	require.NoError(t, err)

	deleted, err := r.deleteAllReleases()
	require.NoError(t, err, "one failed deletion must not abort the wipe")
	assert.Equal(t, 2, deleted, "the failed tag is not counted as deleted")

	assert.Len(t, deleteInvocations(ghInvocations(t, logPath)), 3,
		"every release must still be attempted")
}

func TestDeleteAllReleases_ListErrorSurfaces(t *testing.T) {
	fakeGH(t, "")
	t.Setenv("RESET_GH_LIST_FAIL", "1")
	dir := newResetRepo(t)

	r, err := New(Options{RepoPath: dir})
	require.NoError(t, err)

	deleted, err := r.deleteAllReleases()
	require.Error(t, err, "a failed release listing must surface, not read as empty")
	assert.Equal(t, 0, deleted)
}

// blockNetworkGit forces every git transport attempt to fail fast so tests that
// run the full Reset flow against a github-style origin URL stay hermetic: the
// remote-side steps (fetch --tags, push --delete) fail immediately and reset's
// warn-and-continue handling is what gets exercised.
func blockNetworkGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_SSH_COMMAND", "false")
}

func TestReset_DryRunMutatesNothing(t *testing.T) {
	logPath := fakeGH(t, "v1.0.0\n")
	blockNetworkGit(t)
	dir := newResetRepo(t)
	gitInDir(t, dir, "tag", "v1.0.0")
	gitInDir(t, dir, "tag", "v1.1.0")

	r, err := New(Options{RepoPath: dir, DryRun: true})
	require.NoError(t, err)

	result, err := r.Reset()
	require.NoError(t, err)
	assert.Equal(t, 1, result.ReleasesDeleted)
	assert.Equal(t, 2, result.TagsDeleted)
	assert.False(t, result.StateReset)
	assert.False(t, result.Pushed)

	assert.Empty(t, deleteInvocations(ghInvocations(t, logPath)),
		"dry-run must not delete releases")

	out, err := exec.Command("git", "-C", dir, "tag", "-l").Output()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"v1.0.0", "v1.1.0"},
		strings.Fields(string(out)), "dry-run must leave every local tag in place")
}

func TestReset_DeletesReleasesAndLocalTags(t *testing.T) {
	logPath := fakeGH(t, "v1.0.0\n")
	blockNetworkGit(t)
	dir := newResetRepo(t)
	gitInDir(t, dir, "tag", "v1.0.0")
	gitInDir(t, dir, "tag", "v1.1.0")

	r, err := New(Options{RepoPath: dir})
	require.NoError(t, err)

	result, err := r.Reset()
	require.NoError(t, err)
	assert.Equal(t, 1, result.ReleasesDeleted)
	assert.Equal(t, 2, result.TagsDeleted)
	assert.False(t, result.StateReset, "state reset was not requested")

	deletes := deleteInvocations(ghInvocations(t, logPath))
	require.Len(t, deletes, 1)
	assert.Equal(t, "-R test/repo release delete v1.0.0 --yes", deletes[0])

	out, err := exec.Command("git", "-C", dir, "tag", "-l").Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)), "every local tag must be gone")
}
