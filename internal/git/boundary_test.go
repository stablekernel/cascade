package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/taggrammar"
)

// newEnclosedNonRepoDir builds an outer repository with one commit and a
// v9.9.9 tag, plus a plain (non-repository) subdirectory inside it. It models
// a dir argument that is not itself a repository but sits inside one, the
// layout where an unpinned git invocation walks up and silently operates on
// the enclosing repository instead of failing.
func newEnclosedNonRepoDir(t *testing.T) (outer, inner string) {
	t.Helper()

	outer = t.TempDir()
	gitAt(t, "", "init", "-b", "main", outer)
	configRepo(t, outer)
	writeFileAt(t, outer, "seed.txt", "seed\n")
	gitAt(t, outer, "add", "seed.txt")
	gitAt(t, outer, "commit", "-m", "chore: seed")
	gitAt(t, outer, "tag", "v9.9.9")

	inner = filepath.Join(outer, "not-a-repo")
	if err := os.MkdirAll(inner, 0o750); err != nil {
		t.Fatalf("mkdir inner: %v", err)
	}
	return outer, inner
}

// TestGetLatestTagSpec_NonRepoDirDoesNotReadEnclosingRepo pins the repository
// boundary for tag discovery: a dir that is not itself a repository must error
// loudly, not walk up and read the tags of whatever repository happens to
// enclose it. Without the boundary, version derivation silently mints versions
// from a foreign repository's tag history.
func TestGetLatestTagSpec_NonRepoDirDoesNotReadEnclosingRepo(t *testing.T) {
	_, inner := newEnclosedNonRepoDir(t)

	tag, _, err := GetLatestTagSpec(inner, taggrammar.Default())
	if err == nil {
		t.Fatalf("GetLatestTagSpec in a non-repository dir = (%q, nil), want an error instead of the enclosing repository's tags", tag)
	}
}

// TestGetLatestReleaseTagSpec_NonRepoDirDoesNotReadEnclosingRepo is the
// release-tag sibling of the boundary pin above.
func TestGetLatestReleaseTagSpec_NonRepoDirDoesNotReadEnclosingRepo(t *testing.T) {
	_, inner := newEnclosedNonRepoDir(t)

	tag, _, err := GetLatestReleaseTagSpec(inner, taggrammar.Default())
	if err == nil {
		t.Fatalf("GetLatestReleaseTagSpec in a non-repository dir = (%q, nil), want an error instead of the enclosing repository's tags", tag)
	}
}

// TestRefetchAndReset_NonRepoDirErrors pins the boundary on the reset path: a
// non-repository dir must error, not fetch and hard-reset the enclosing
// repository's working tree (which would destroy unrelated local state).
func TestRefetchAndReset_NonRepoDirErrors(t *testing.T) {
	_, inner := newEnclosedNonRepoDir(t)

	if err := RefetchAndReset(inner); err == nil {
		t.Fatal("RefetchAndReset in a non-repository dir = nil, want an error")
	}
}
