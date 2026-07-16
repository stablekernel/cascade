package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitNullSHA is the all-zero base SHA GitHub delivers for a branch's first push.
const gitNullSHA = "0000000000000000000000000000000000000000"

// newCloneWithRemote creates a bare origin plus a working clone, changes the
// working directory into the clone for the duration of the test (the functions
// under test operate on the process working directory), and returns both paths.
func newCloneWithRemote(t *testing.T) (bare, clone string) {
	t.Helper()

	root := t.TempDir()
	bare = filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	clone = filepath.Join(root, "clone")

	gitAt(t, "", "init", "--bare", bare)
	// Pin the bare HEAD so clones check out main regardless of the host's
	// init.defaultBranch setting.
	gitAt(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	gitAt(t, "", "init", "-b", "main", seed)
	configRepo(t, seed)
	writeFileAt(t, seed, "README.md", "seed\n")
	gitAt(t, seed, "add", "README.md")
	gitAt(t, seed, "commit", "-m", "chore: seed")
	gitAt(t, seed, "remote", "add", "origin", bare)
	gitAt(t, seed, "push", "-u", "origin", "main")

	gitAt(t, "", "clone", bare, clone)
	configRepo(t, clone)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(clone); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	return bare, clone
}

func gitOutAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func writeFileAt(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestGetChangedFiles_DiffBetweenCommits(t *testing.T) {
	newScratchRepo(t)

	base := commitFile(t, "a.txt", "one", "chore: base")
	if err := os.Mkdir("sub", 0o750); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	commitFile(t, "sub/b.txt", "two", "feat: add b")
	head := commitFile(t, "a.txt", "one-changed", "fix: change a")

	got, err := GetChangedFiles(base, head)
	if err != nil {
		t.Fatalf("GetChangedFiles: %v", err)
	}
	want := map[string]bool{"a.txt": true, "sub/b.txt": true}
	if len(got) != len(want) {
		t.Fatalf("GetChangedFiles = %v, want files %v", got, want)
	}
	for _, f := range got {
		if !want[f] {
			t.Errorf("unexpected changed file %q", f)
		}
	}
}

func TestGetChangedFiles_NullBaseReturnsWholeTree(t *testing.T) {
	newScratchRepo(t)

	commitFile(t, "a.txt", "one", "chore: base")
	if err := os.Mkdir("sub", 0o750); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	head := commitFile(t, "sub/b.txt", "two", "feat: add b")

	got, err := GetChangedFiles(gitNullSHA, head)
	if err != nil {
		t.Fatalf("GetChangedFiles: %v", err)
	}
	want := map[string]bool{"a.txt": true, "sub/b.txt": true}
	if len(got) != len(want) {
		t.Fatalf("GetChangedFiles = %v, want the whole tree %v", got, want)
	}
	for _, f := range got {
		if !want[f] {
			t.Errorf("unexpected file %q in tree listing", f)
		}
	}
}

func TestGetChangedFiles_UnknownSHAErrors(t *testing.T) {
	newScratchRepo(t)
	head := commitFile(t, "a.txt", "one", "chore: base")

	if _, err := GetChangedFiles("b8d6a54e6fbbf2a4a89ae0f0b1a76c9bb1e13a00", head); err == nil {
		t.Fatal("GetChangedFiles with an unresolvable base must error, not report no changes")
	}
}

func TestGetInitialCommit(t *testing.T) {
	newScratchRepo(t)

	first := commitFile(t, "a.txt", "one", "chore: first")
	commitFile(t, "b.txt", "two", "chore: second")

	got, err := GetInitialCommit()
	if err != nil {
		t.Fatalf("GetInitialCommit: %v", err)
	}
	if got != first {
		t.Errorf("GetInitialCommit = %q, want %q", got, first)
	}
}

func TestCurrentBranch(t *testing.T) {
	newScratchRepo(t)
	commitFile(t, "a.txt", "one", "chore: base")
	runGit(t, "checkout", "-b", "feature/current-branch")

	got, err := CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if got != "feature/current-branch" {
		t.Errorf("CurrentBranch = %q, want feature/current-branch", got)
	}
}

func TestCurrentBranch_DetachedHeadErrors(t *testing.T) {
	newScratchRepo(t)
	sha := commitFile(t, "a.txt", "one", "chore: base")
	runGit(t, "checkout", "--detach", sha)

	if _, err := CurrentBranch(); err == nil {
		t.Fatal("CurrentBranch on a detached HEAD must error")
	}
}

func TestRefetchAndReset_DropsLocalCommitAndAdoptsUpstream(t *testing.T) {
	bare, clone := newCloneWithRemote(t)

	// A second clone advances trunk.
	other := filepath.Join(t.TempDir(), "other")
	gitAt(t, "", "clone", bare, other)
	configRepo(t, other)
	writeFileAt(t, other, "upstream.txt", "from other\n")
	gitAt(t, other, "add", "upstream.txt")
	gitAt(t, other, "commit", "-m", "feat: upstream change")
	gitAt(t, other, "push")

	// The local clone commits on the stale base without pushing.
	writeFileAt(t, clone, "local.txt", "stale local\n")
	gitAt(t, clone, "add", "local.txt")
	gitAt(t, clone, "commit", "-m", "chore: stale local commit")

	if err := RefetchAndReset(clone); err != nil {
		t.Fatalf("RefetchAndReset: %v", err)
	}

	localTip := gitOutAt(t, clone, "rev-parse", "HEAD")
	remoteTip := gitOutAt(t, bare, "rev-parse", "main")
	if localTip != remoteTip {
		t.Errorf("HEAD = %s, want the upstream tip %s", localTip, remoteTip)
	}
	if _, err := os.Stat(filepath.Join(clone, "local.txt")); !os.IsNotExist(err) {
		t.Error("the stale local commit's file must be gone after the hard reset")
	}
	if _, err := os.Stat(filepath.Join(clone, "upstream.txt")); err != nil {
		t.Error("the upstream change must be present after the reset")
	}
}

// TestPushWithRebaseRetry_ReapplyConvergesRejectedPush proves the WithReapply
// path: when a concurrent writer advances trunk between checkout and push, the
// retry loop re-fetches, hard-resets onto the new tip, invokes the re-apply
// callback to re-derive the owned change, and pushes successfully, so both
// writers' changes land.
func TestPushWithRebaseRetry_ReapplyConvergesRejectedPush(t *testing.T) {
	bare, clone := newCloneWithRemote(t)

	// The owned change, committed against the soon-to-be-stale trunk tip.
	writeFileAt(t, clone, "owned.txt", "owned leaf\n")
	gitAt(t, clone, "add", "owned.txt")
	gitAt(t, clone, "commit", "-m", "chore: owned leaf")

	// A concurrent sibling lands on trunk first, so the push is rejected.
	other := filepath.Join(t.TempDir(), "other")
	gitAt(t, "", "clone", bare, other)
	configRepo(t, other)
	writeFileAt(t, other, "sibling.txt", "sibling leaf\n")
	gitAt(t, other, "add", "sibling.txt")
	gitAt(t, other, "commit", "-m", "chore: sibling leaf")
	gitAt(t, other, "push")

	reapplyCalls := 0
	err := PushWithRebaseRetry(
		WithDir(clone),
		WithBackoff(time.Millisecond),
		WithReapply(func() error {
			reapplyCalls++
			// Re-derive the owned leaf onto the freshly reset trunk and recommit.
			writeFileAt(t, clone, "owned.txt", "owned leaf\n")
			gitAt(t, clone, "add", "owned.txt")
			gitAt(t, clone, "commit", "-m", "chore: owned leaf (reapplied)")
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("PushWithRebaseRetry with reapply: %v", err)
	}

	if reapplyCalls != 1 {
		t.Errorf("reapply calls = %d, want exactly 1", reapplyCalls)
	}
	for _, f := range []string{"owned.txt", "sibling.txt"} {
		if out, err := exec.Command("git", "-C", bare, "cat-file", "-e", "main:"+f).CombinedOutput(); err != nil {
			t.Errorf("trunk must carry %s after convergence: %v\n%s", f, err, out)
		}
	}
}

// TestPushWithRebaseRetry_ReapplyErrorSurfaces proves a failing re-apply
// callback aborts the retry loop with the callback's error rather than looping.
func TestPushWithRebaseRetry_ReapplyErrorSurfaces(t *testing.T) {
	bare, clone := newCloneWithRemote(t)

	writeFileAt(t, clone, "owned.txt", "owned leaf\n")
	gitAt(t, clone, "add", "owned.txt")
	gitAt(t, clone, "commit", "-m", "chore: owned leaf")

	other := filepath.Join(t.TempDir(), "other")
	gitAt(t, "", "clone", bare, other)
	configRepo(t, other)
	writeFileAt(t, other, "sibling.txt", "sibling leaf\n")
	gitAt(t, other, "add", "sibling.txt")
	gitAt(t, other, "commit", "-m", "chore: sibling leaf")
	gitAt(t, other, "push")

	err := PushWithRebaseRetry(
		WithDir(clone),
		WithBackoff(time.Millisecond),
		WithReapply(func() error { return os.ErrPermission }),
	)
	if err == nil {
		t.Fatal("a failing reapply callback must surface an error")
	}
	if !strings.Contains(err.Error(), "re-apply") {
		t.Errorf("error %q must attribute the failure to the re-apply step", err)
	}
}
