package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cloneRepo clones src into a fresh temp dir, switches the working directory to
// the clone for the duration of the test, configures a committer identity, and
// returns the clone path. The original working directory is restored on cleanup.
func cloneRepo(t *testing.T, src string) string {
	t.Helper()

	clone := t.TempDir()
	cmd := exec.Command("git", "clone", src, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(clone); err != nil {
		t.Fatalf("chdir clone: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	runGit(t, "config", "user.email", "clone@example.com")
	runGit(t, "config", "user.name", "Clone User")
	runGit(t, "config", "commit.gpgsign", "false")
	runGit(t, "config", "pull.rebase", "true")

	return clone
}

// chdir switches the working directory to dir for the duration of the test and
// restores the original on cleanup.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}

// readHeadFile returns the contents of name at the current HEAD of the repo in dir.
func readHeadFile(t *testing.T, dir, name string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "show", "HEAD:"+name).Output()
	if err != nil {
		t.Fatalf("git show HEAD:%s: %v", name, err)
	}
	return string(out)
}

// TestCommitAndPushWithRetry_RecoversFromNonFastForward proves the resilience the
// external-update path relies on: when the remote has advanced behind the local
// checkout's back (a stale parent, exactly the concurrent external-update race),
// a plain push is rejected non-fast-forward, but CommitAndPushWithRetry rebases
// and succeeds without dropping either side's change.
func TestCommitAndPushWithRetry_RecoversFromNonFastForward(t *testing.T) {
	// Bare origin shared by two working clones.
	origin := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	// Seed the origin with a manifest that already carries one external slot.
	// Both racers add a NEW, non-adjacent slot, mirroring two upstream artifacts
	// updating the same primary manifest concurrently.
	const seed = "state:\n  dev:\n    sha: base\n    external:\n      existing:\n        repo: org/existing\n"
	bootstrap := cloneRepo(t, origin)
	commitFile(t, "manifest.yaml", seed, "seed manifest")
	runGit(t, "push", "origin", "HEAD:main")

	// "racer A" clones the seeded origin. Its checkout becomes stale once B pushes.
	racerA := cloneRepo(t, origin)

	// "racer B" clones, prepends its slot, and pushes first. This advances origin
	// so racerA's parent is no longer the tip.
	const bManifest = "state:\n  dev:\n    sha: base\n    external:\n      cdk:\n        repo: org/cdk\n      existing:\n        repo: org/existing\n"
	racerB := cloneRepo(t, origin)
	if err := os.WriteFile(filepath.Join(racerB, "manifest.yaml"), []byte(bManifest), 0o600); err != nil {
		t.Fatalf("write B manifest: %v", err)
	}
	chdir(t, racerB)
	runGit(t, "add", "manifest.yaml")
	runGit(t, "commit", "-m", "B: add cdk slot")
	runGit(t, "push", "origin", "HEAD:main")

	// racerA now appends its own slot against the stale parent and pushes through
	// the retrying path. A plain push here is rejected non-fast-forward.
	chdir(t, racerA)
	const aManifest = "state:\n  dev:\n    sha: base\n    external:\n      existing:\n        repo: org/existing\n      lambda:\n        repo: org/lambda\n"
	if err := os.WriteFile(filepath.Join(racerA, "manifest.yaml"), []byte(aManifest), 0o600); err != nil {
		t.Fatalf("write A manifest: %v", err)
	}

	if err := CommitAndPushWithRetry("manifest.yaml", "A: add lambda slot"); err != nil {
		t.Fatalf("CommitAndPushWithRetry() error = %v, want nil (must rebase over B and push)", err)
	}

	// Origin tip must contain BOTH commits' subjects: no slot was lost.
	logOut, err := exec.Command("git", "-C", origin, "log", "--format=%s", "main").Output()
	if err != nil {
		t.Fatalf("git log origin: %v", err)
	}
	log := string(logOut)
	if !strings.Contains(log, "A: add lambda slot") {
		t.Errorf("origin missing racerA commit; log:\n%s", log)
	}
	if !strings.Contains(log, "B: add cdk slot") {
		t.Errorf("origin missing racerB commit (state loss); log:\n%s", log)
	}

	// The manifest at origin HEAD must carry BOTH new slots: the rebase merges
	// A's append on top of B's prepend, so no artifact's slot is lost.
	got := readHeadFile(t, origin, "manifest.yaml")
	if !strings.Contains(got, "lambda") {
		t.Errorf("origin HEAD manifest missing racerA (lambda) slot; got:\n%s", got)
	}
	if !strings.Contains(got, "cdk") {
		t.Errorf("origin HEAD manifest missing racerB (cdk) slot - state loss; got:\n%s", got)
	}

	_ = bootstrap
}
