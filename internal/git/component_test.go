package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/taggrammar"
)

// commitFileAt writes a file at a (possibly nested) repo-relative path, staging
// and committing it, and returns the resulting commit SHA. It is the path-scoped
// sibling of commitFile, used to advance one component's subtree in isolation.
func commitFileAt(t *testing.T, repoPath, content, message string) string {
	t.Helper()
	if dir := filepath.Dir(repoPath); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(repoPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write file %s: %v", repoPath, err)
	}
	runGit(t, "add", repoPath)
	runGit(t, "commit", "-m", message)
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestGetCommitsForPaths_ScopesToIncludePaths proves a commit touching only one
// component's subtree appears in that component's range and not a sibling's, so
// per-component version derivation never sees a sibling's commits.
func TestGetCommitsForPaths_ScopesToIncludePaths(t *testing.T) {
	newScratchRepo(t)
	base := commitFileAt(t, "README.md", "root", "chore: seed")
	commitFileAt(t, "services/api/main.go", "package api", "feat: api change")
	commitFileAt(t, "services/web/main.go", "package web", "fix: web change")

	apiCommits, err := GetCommitsForPaths(base, "HEAD", []string{"services/api"})
	if err != nil {
		t.Fatalf("GetCommitsForPaths(api): %v", err)
	}
	if len(apiCommits) != 1 {
		t.Fatalf("api range: got %d commits, want 1", len(apiCommits))
	}
	if apiCommits[0].Subject != "feat: api change" {
		t.Errorf("api range subject = %q, want %q", apiCommits[0].Subject, "feat: api change")
	}

	webCommits, err := GetCommitsForPaths(base, "HEAD", []string{"services/web"})
	if err != nil {
		t.Fatalf("GetCommitsForPaths(web): %v", err)
	}
	if len(webCommits) != 1 {
		t.Fatalf("web range: got %d commits, want 1", len(webCommits))
	}
	if webCommits[0].Subject != "fix: web change" {
		t.Errorf("web range subject = %q, want %q", webCommits[0].Subject, "fix: web change")
	}
}

// TestGetCommitsForPaths_NilPathsEqualsWholeRepo proves that with no include
// paths GetCommitsForPaths sees every commit in the range, matching
// GetCommits(base, head, nil): the single-component reduction is unchanged.
func TestGetCommitsForPaths_NilPathsEqualsWholeRepo(t *testing.T) {
	newScratchRepo(t)
	base := commitFileAt(t, "README.md", "root", "chore: seed")
	commitFileAt(t, "services/api/main.go", "package api", "feat: api change")
	commitFileAt(t, "services/web/main.go", "package web", "fix: web change")

	scoped, err := GetCommitsForPaths(base, "HEAD", nil)
	if err != nil {
		t.Fatalf("GetCommitsForPaths(nil): %v", err)
	}
	plain, err := GetCommits(base, "HEAD", nil)
	if err != nil {
		t.Fatalf("GetCommits(nil): %v", err)
	}
	if len(scoped) != len(plain) {
		t.Fatalf("nil-path scope: got %d commits, want %d (whole repo)", len(scoped), len(plain))
	}
	if len(scoped) != 2 {
		t.Errorf("nil-path scope: got %d commits, want 2", len(scoped))
	}
}

// TestGetLatestTagSpec_StrictPrefixIsolatesNamespace proves a component's strict
// prefix accepts only its own tags and rejects a sibling's, so a component's
// current-version lookup never reads across the namespace boundary.
func TestGetLatestTagSpec_StrictPrefixIsolatesNamespace(t *testing.T) {
	dir := newScratchRepo(t)
	commitFileAt(t, "README.md", "root", "chore: seed")

	tagHead(t, "api-1.2.0")
	tagHead(t, "api-1.2.1")
	tagHead(t, "web-2.0.0")   // sibling namespace, higher base version
	tagHead(t, "api-nightly") // foreign tag matching the api glob

	spec := taggrammar.Default()
	spec.Prefix = "api-"
	spec.StrictPrefix = true

	got, sha, err := GetLatestTagSpec(dir, spec)
	if err != nil {
		t.Fatalf("GetLatestTagSpec(api): %v", err)
	}
	if got != "api-1.2.1" {
		t.Errorf("GetLatestTagSpec(api) = %q, want %q (must ignore web-* and foreign tags)", got, "api-1.2.1")
	}
	if sha == "" {
		t.Errorf("GetLatestTagSpec(api) returned empty SHA for %q", got)
	}
}

// TestGetLatestTag_StaysPermissive proves the GetLatestTag wrapper preserves its
// historical permissive-prefix behavior for the single-component path.
func TestGetLatestTag_StaysPermissive(t *testing.T) {
	dir := newScratchRepo(t)
	commitFileAt(t, "README.md", "root", "chore: seed")
	tagHead(t, "v0.5.0")
	tagHead(t, "v0.5.1")

	got, _, err := GetLatestTag(dir, "v")
	if err != nil {
		t.Fatalf("GetLatestTag: %v", err)
	}
	if got != "v0.5.1" {
		t.Errorf("GetLatestTag = %q, want %q", got, "v0.5.1")
	}
}
