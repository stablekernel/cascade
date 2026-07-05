package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initClonesWithSharedRemote builds a bare remote plus two working clones that
// track it. cloneA holds a committed state file; cloneB then advances the shared
// remote with an unrelated commit, so a subsequent push from cloneA is rejected
// as non-fast-forward until it rebases onto the advanced upstream.
func initClonesWithSharedRemote(t *testing.T) (cloneA, cloneB, statePath string) {
	t.Helper()

	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", "-b", "main")

	cloneA = t.TempDir()
	runGit(t, cloneA, "clone", remote, ".")
	runGit(t, cloneA, "checkout", "-b", "main")
	writeFile(t, cloneA, ".github/manifest.yaml", "ci:\n  state:\n    prerelease:\n      version: v0.1.0-rc.0\n")
	runGit(t, cloneA, "add", ".github/manifest.yaml")
	runGit(t, cloneA, "commit", "-m", "chore: seed state")
	runGit(t, cloneA, "push", "-u", "origin", "main")

	cloneB = t.TempDir()
	runGit(t, cloneB, "clone", remote, ".")
	// cloneB advances the shared remote with an unrelated file so the rebase in
	// cloneA replays cleanly (no conflict on the state file).
	writeFile(t, cloneB, "OTHER.md", "concurrent writer\n")
	runGit(t, cloneB, "add", "OTHER.md")
	runGit(t, cloneB, "commit", "-m", "chore: concurrent write")
	runGit(t, cloneB, "push", "origin", "main")

	statePath = filepath.Join(cloneA, ".github", "manifest.yaml")
	return cloneA, cloneB, statePath
}

// TestCommitAndPush_RetriesNonFastForward reproduces F05: when the shared trunk
// advances between checkout and push, a plain "git push" is rejected
// non-fast-forward. commitAndPush must rebase onto the advanced upstream and
// retry rather than fail the orchestrate run outright.
func TestCommitAndPush_RetriesNonFastForward(t *testing.T) {
	cloneA, _, statePath := initClonesWithSharedRemote(t)

	// Local, uncommitted state change in cloneA whose base is now behind trunk.
	writeFile(t, cloneA, ".github/manifest.yaml",
		"ci:\n  state:\n    prerelease:\n      version: v0.1.0-rc.1\n")

	o := &Orchestrator{
		configPath:  statePath,
		environment: "prerelease",
		baseDir:     cloneA,
		pushBackoff: time.Millisecond,
	}

	if err := o.commitAndPush("v0.1.0-rc.1"); err != nil {
		t.Fatalf("commitAndPush should rebase and retry a non-fast-forward push, got error: %v", err)
	}

	// The state commit landed on the shared remote on top of the concurrent
	// writer's commit (a successful rebase-and-push).
	remoteLog := runGit(t, cloneA, "log", "--oneline", "origin/main")
	for _, want := range []string{"seed state", "concurrent write"} {
		if !strings.Contains(remoteLog, want) {
			t.Fatalf("expected origin/main history to contain %q after retry, got:\n%s", want, remoteLog)
		}
	}
}

// TestCommitAndPush_NoChangesIsNoOp guards the early return: with a clean tree
// there is nothing to commit, so commitAndPush succeeds without pushing.
func TestCommitAndPush_NoChangesIsNoOp(t *testing.T) {
	cloneA, _, statePath := initClonesWithSharedRemote(t)

	o := &Orchestrator{
		configPath:  statePath,
		environment: "prerelease",
		baseDir:     cloneA,
		pushBackoff: time.Millisecond,
	}

	if err := o.commitAndPush("v0.1.0-rc.0"); err != nil {
		t.Fatalf("commitAndPush with no changes should be a no-op, got error: %v", err)
	}
}

// initClonesWithConflictingRemote builds a bare remote plus two working clones
// that track it, where cloneB advances the shared remote with a change to the
// exact same line of the same state file that the caller will then also edit
// in cloneA. That sets up a genuine rebase conflict (rather than a clean
// replay) once cloneA commits its own edit and pushStateWithRetry rebases
// onto the advanced upstream.
func initClonesWithConflictingRemote(t *testing.T) (cloneA, cloneB, statePath string) {
	t.Helper()

	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", "-b", "main")

	cloneA = t.TempDir()
	runGit(t, cloneA, "clone", remote, ".")
	runGit(t, cloneA, "checkout", "-b", "main")
	writeFile(t, cloneA, ".github/manifest.yaml", "ci:\n  state:\n    prerelease:\n      version: v0.1.0-rc.0\n")
	runGit(t, cloneA, "add", ".github/manifest.yaml")
	runGit(t, cloneA, "commit", "-m", "chore: seed state")
	runGit(t, cloneA, "push", "-u", "origin", "main")

	cloneB = t.TempDir()
	runGit(t, cloneB, "clone", remote, ".")
	// cloneB advances the shared remote with a conflicting edit to the same
	// version line that cloneA will independently change.
	writeFile(t, cloneB, ".github/manifest.yaml", "ci:\n  state:\n    prerelease:\n      version: v0.1.0-rc.9\n")
	runGit(t, cloneB, "add", ".github/manifest.yaml")
	runGit(t, cloneB, "commit", "-m", "chore: concurrent state write")
	runGit(t, cloneB, "push", "origin", "main")

	statePath = filepath.Join(cloneA, ".github", "manifest.yaml")
	return cloneA, cloneB, statePath
}

// TestCommitAndPush_AbortsRebaseOnConflict reproduces the mid-rebase defect in
// pushStateWithRetry: when "git pull --rebase" hits a genuine conflict (a
// concurrent writer changed the same state file line cloneA is committing),
// the old code only logged a warning and fell through to the next push
// attempt, leaving the repository stuck mid-rebase after the retries were
// exhausted. The fix aborts the rebase and returns the wrapped error
// immediately. Both properties are asserted: an error is returned, and the
// clone is left in a clean, non-rebasing state.
func TestCommitAndPush_AbortsRebaseOnConflict(t *testing.T) {
	cloneA, _, statePath := initClonesWithConflictingRemote(t)

	// Local, uncommitted change to the same line cloneB already advanced on
	// the shared remote, so the rebase replay conflicts.
	writeFile(t, cloneA, ".github/manifest.yaml",
		"ci:\n  state:\n    prerelease:\n      version: v0.1.0-rc.1\n")

	o := &Orchestrator{
		configPath:  statePath,
		environment: "prerelease",
		baseDir:     cloneA,
		pushBackoff: time.Millisecond,
	}

	if err := o.commitAndPush("v0.1.0-rc.1"); err == nil {
		t.Fatal("commitAndPush with a conflicting remote: expected error, got nil")
	}

	// The clone must not be left mid-rebase.
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, statErr := os.Stat(filepath.Join(cloneA, ".git", name)); statErr == nil {
			t.Fatalf("repository left mid-rebase: .git/%s still present", name)
		}
	}

	status := runGit(t, cloneA, "status", "--porcelain=v1", "--branch")
	if strings.Contains(status, "rebasing") {
		t.Fatalf("git status reports a rebase in progress:\n%s", status)
	}
}
