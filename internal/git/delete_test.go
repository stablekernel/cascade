package git

import (
	"os"
	"testing"
)

// cloneWithOrigin creates a clone of dir whose origin is dir, chdirs into the
// clone for the test, fetches origin, and returns the clone path.
func cloneWithOrigin(t *testing.T, dir string) string {
	t.Helper()
	clone := t.TempDir()
	runGit(t, "clone", dir, clone)

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
	runGit(t, "fetch", "origin")
	return clone
}

func TestDeleteRemoteBranch(t *testing.T) {
	dir := newScratchRepo(t)
	commitFile(t, "a.txt", "one", "first commit")
	runGit(t, "branch", "-M", "main")
	runGit(t, "branch", "env/test")

	cloneWithOrigin(t, dir)

	exists, err := BranchExists("origin", "env/test")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if !exists {
		t.Fatalf("precondition: env/test should exist on origin")
	}

	if err := DeleteRemoteBranch("origin", "env/test"); err != nil {
		t.Fatalf("DeleteRemoteBranch: %v", err)
	}

	runGit(t, "fetch", "origin", "--prune")
	exists, err = BranchExists("origin", "env/test")
	if err != nil {
		t.Fatalf("BranchExists after delete: %v", err)
	}
	if exists {
		t.Fatalf("env/test should be deleted on origin")
	}

	// Deleting an already-absent branch is a no-op success (idempotent).
	if err := DeleteRemoteBranch("origin", "env/test"); err != nil {
		t.Fatalf("DeleteRemoteBranch on missing branch should be a no-op: %v", err)
	}
}

func TestDeleteRemoteTag(t *testing.T) {
	dir := newScratchRepo(t)
	commitFile(t, "a.txt", "one", "first commit")
	runGit(t, "branch", "-M", "main")
	runGit(t, "tag", "v1.4.0-rc.2.hotfix.1")

	cloneWithOrigin(t, dir)
	runGit(t, "fetch", "origin", "--tags")

	tags, err := ListTags()
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if !contains(tags, "v1.4.0-rc.2.hotfix.1") {
		t.Fatalf("precondition: hotfix tag should be present, got %v", tags)
	}

	if err := DeleteRemoteTag("origin", "v1.4.0-rc.2.hotfix.1"); err != nil {
		t.Fatalf("DeleteRemoteTag: %v", err)
	}

	// Deleting an already-absent tag is a no-op success (idempotent).
	if err := DeleteRemoteTag("origin", "v1.4.0-rc.2.hotfix.1"); err != nil {
		t.Fatalf("DeleteRemoteTag on missing tag should be a no-op: %v", err)
	}
}

func TestListRemoteBranches(t *testing.T) {
	dir := newScratchRepo(t)
	commitFile(t, "a.txt", "one", "first commit")
	runGit(t, "branch", "-M", "main")
	runGit(t, "branch", "env/test")
	runGit(t, "branch", "env/uat")

	cloneWithOrigin(t, dir)

	branches, err := ListRemoteBranches("origin")
	if err != nil {
		t.Fatalf("ListRemoteBranches: %v", err)
	}

	for _, want := range []string{"main", "env/test", "env/uat"} {
		if !contains(branches, want) {
			t.Errorf("expected branch %q in %v", want, branches)
		}
	}
	// The remote HEAD pointer must not leak through as a branch name.
	if contains(branches, "HEAD") {
		t.Errorf("HEAD should not be returned as a branch: %v", branches)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
