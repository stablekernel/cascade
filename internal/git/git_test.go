package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stablekernel/cascade/internal/taggrammar"
)

// newScratchRepo initializes a git repository in a temp directory, changes the
// working directory to it for the duration of the test, and returns the repo path.
// The original working directory is restored via t.Cleanup.
func newScratchRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

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

	runGit(t, "init")
	runGit(t, "config", "user.email", "test@example.com")
	runGit(t, "config", "user.name", "Test User")
	runGit(t, "config", "commit.gpgsign", "false")

	return dir
}

// runGit runs a git command in the current working directory and fails the test on error.
func runGit(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// commitFile writes a file, stages it, commits with the given message, and
// returns the resulting commit SHA.
func commitFile(t *testing.T, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(".", name), []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, "add", name)
	runGit(t, "commit", "-m", message)

	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestParseCommits(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want []Commit
	}{
		{
			name: "single commit",
			// Format: hash, subject, author, email, body
			data: []byte("abc123\x1fFix bug\x1fJohn Doe\x1fjohn@example.com\x1fThis fixes the bug\x1e"),
			want: []Commit{
				{Hash: "abc123", Subject: "Fix bug", Author: "John Doe", AuthorEmail: "john@example.com", Body: "This fixes the bug"},
			},
		},
		{
			name: "multiple commits",
			data: []byte("abc123\x1fFirst commit\x1fAlice\x1falice@example.com\x1fBody 1\x1edef456\x1fSecond commit\x1fBob\x1fbob@example.com\x1fBody 2\x1e"),
			want: []Commit{
				{Hash: "abc123", Subject: "First commit", Author: "Alice", AuthorEmail: "alice@example.com", Body: "Body 1"},
				{Hash: "def456", Subject: "Second commit", Author: "Bob", AuthorEmail: "bob@example.com", Body: "Body 2"},
			},
		},
		{
			name: "commit without body",
			data: []byte("abc123\x1fSimple fix\x1fDev\x1fdev@example.com\x1f\x1e"),
			want: []Commit{
				{Hash: "abc123", Subject: "Simple fix", Author: "Dev", AuthorEmail: "dev@example.com", Body: ""},
			},
		},
		{
			name: "empty input",
			data: []byte(""),
			want: nil,
		},
		{
			name: "multiline body",
			data: []byte("abc123\x1fFeat: something\x1fAuthor Name\x1fauthor@example.com\x1fLine 1\nLine 2\nLine 3\x1e"),
			want: []Commit{
				{Hash: "abc123", Subject: "Feat: something", Author: "Author Name", AuthorEmail: "author@example.com", Body: "Line 1\nLine 2\nLine 3"},
			},
		},
		{
			name: "whitespace handling",
			data: []byte("  abc123  \x1f  Subject  \x1f  Author  \x1f  author@example.com  \x1f  Body  \x1e"),
			want: []Commit{
				{Hash: "abc123", Subject: "Subject", Author: "Author", AuthorEmail: "author@example.com", Body: "Body"},
			},
		},
		{
			name: "same author multiple commits",
			data: []byte("commit1\x1fFix 1\x1fAlice\x1falice@example.com\x1f\x1ecommit2\x1fFix 2\x1fAlice\x1falice@example.com\x1f\x1e"),
			want: []Commit{
				{Hash: "commit1", Subject: "Fix 1", Author: "Alice", AuthorEmail: "alice@example.com", Body: ""},
				{Hash: "commit2", Subject: "Fix 2", Author: "Alice", AuthorEmail: "alice@example.com", Body: ""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCommits(tt.data)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseCommits() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestGetCommits_UnknownBaseSHAReturnsError proves a git failure (a bad or
// unknown base SHA) is surfaced as an error rather than swallowed as an empty
// commit range. Swallowing it would let the caller silently recompute the same
// version with no bump, cutting a wrong version with no warning.
func TestGetCommits_UnknownBaseSHAReturnsError(t *testing.T) {
	newScratchRepo(t)
	head := commitFile(t, "a.txt", "one", "first commit")

	if _, err := GetCommits("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", head, nil); err == nil {
		t.Fatal("GetCommits() with an unknown base SHA: expected error, got nil")
	}
}

// TestGetCommits_EmptyRangeIsNotAnError proves a legitimately empty range
// (base == head, git exits 0) returns no commits and no error.
func TestGetCommits_EmptyRangeIsNotAnError(t *testing.T) {
	newScratchRepo(t)
	head := commitFile(t, "a.txt", "one", "first commit")

	commits, err := GetCommits(head, head, nil)
	if err != nil {
		t.Fatalf("GetCommits() empty range: unexpected error: %v", err)
	}
	if len(commits) != 0 {
		t.Fatalf("GetCommits() empty range: got %d commits, want 0", len(commits))
	}
}

func TestParseLines(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want []string
	}{
		{
			name: "multiple lines",
			data: []byte("file1.go\nfile2.go\nfile3.go"),
			want: []string{"file1.go", "file2.go", "file3.go"},
		},
		{
			name: "trailing newline",
			data: []byte("file1.go\nfile2.go\n"),
			want: []string{"file1.go", "file2.go"},
		},
		{
			name: "empty lines filtered",
			data: []byte("file1.go\n\nfile2.go\n\n"),
			want: []string{"file1.go", "file2.go"},
		},
		{
			name: "whitespace trimmed",
			data: []byte("  file1.go  \n  file2.go  "),
			want: []string{"file1.go", "file2.go"},
		},
		{
			name: "empty input",
			data: []byte(""),
			want: nil,
		},
		{
			name: "single file",
			data: []byte("only-file.txt"),
			want: []string{"only-file.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLines(tt.data)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseLines() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsAncestor_TrueFalseAndError(t *testing.T) {
	newScratchRepo(t)

	first := commitFile(t, "a.txt", "one", "first commit")
	second := commitFile(t, "b.txt", "two", "second commit")

	tests := []struct {
		name       string
		ancestor   string
		descendant string
		want       bool
		wantErr    bool
	}{
		{name: "is ancestor", ancestor: first, descendant: second, want: true},
		{name: "not ancestor", ancestor: second, descendant: first, want: false},
		{name: "bad sha errors", ancestor: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", descendant: second, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IsAncestor(tt.ancestor, tt.descendant)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("IsAncestor() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("IsAncestor() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("IsAncestor() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBranchExists(t *testing.T) {
	dir := newScratchRepo(t)

	commitFile(t, "a.txt", "one", "first commit")
	runGit(t, "branch", "-M", "main")
	runGit(t, "branch", "feature")

	// Create a second repo that uses the scratch repo as its origin remote.
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

	tests := []struct {
		name   string
		remote string
		branch string
		want   bool
	}{
		{name: "existing branch", remote: "origin", branch: "feature", want: true},
		{name: "missing branch", remote: "origin", branch: "nope", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BranchExists(tt.remote, tt.branch)
			if err != nil {
				t.Fatalf("BranchExists() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("BranchExists() = %v, want %v", got, tt.want)
			}
		})
	}
}

// gitAt runs a git command in dir via "git -C" and fails the test on error. An
// empty dir runs git without -C (in the process working directory).
func gitAt(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
}

// configRepo sets the identity and disables signing for a working clone so
// commits succeed deterministically in the test environment.
func configRepo(t *testing.T, dir string) {
	t.Helper()
	gitAt(t, dir, "config", "user.email", "test@example.com")
	gitAt(t, dir, "config", "user.name", "Test User")
	gitAt(t, dir, "config", "commit.gpgsign", "false")
}

// sharedRemoteClones builds a bare remote plus two working clones tracking it.
// The seed clone commits and pushes seedFile; the other clone then advances the
// remote with otherFile so a subsequent push from seed is rejected as
// non-fast-forward. When otherFile equals seedFile the advance conflicts on the
// same path, setting up a genuine rebase conflict. Both clones have signing
// disabled and an identity configured.
func sharedRemoteClones(t *testing.T, seedFile, seedBody, otherFile, otherBody string) (seed, other string) {
	t.Helper()

	origin := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	seed = t.TempDir()
	gitAt(t, "", "clone", origin, seed)
	configRepo(t, seed)
	if err := os.WriteFile(filepath.Join(seed, seedFile), []byte(seedBody), 0o600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	gitAt(t, seed, "add", seedFile)
	gitAt(t, seed, "commit", "-m", "seed state")
	gitAt(t, seed, "push", "origin", "main")

	other = t.TempDir()
	gitAt(t, "", "clone", origin, other)
	configRepo(t, other)
	if err := os.WriteFile(filepath.Join(other, otherFile), []byte(otherBody), 0o600); err != nil {
		t.Fatalf("write other file: %v", err)
	}
	gitAt(t, other, "add", otherFile)
	gitAt(t, other, "commit", "-m", "concurrent write")
	gitAt(t, other, "push", "origin", "main")

	return seed, other
}

// TestPushWithRebaseRetry_RetriesNonFastForward proves the exported push half of
// the shared helper rebases onto an advanced upstream and retries when the first
// push is rejected non-fast-forward. WithDir drives the seed clone without
// changing the process working directory, and WithBackoff keeps the retry fast.
func TestPushWithRebaseRetry_RetriesNonFastForward(t *testing.T) {
	// The concurrent writer touches an unrelated file so the rebase replays
	// cleanly rather than conflicting.
	seed, _ := sharedRemoteClones(t, "state.txt", "base\n", "OTHER.md", "concurrent\n")

	// A local, committed change on the seed clone whose base is now behind trunk.
	if err := os.WriteFile(filepath.Join(seed, "state.txt"), []byte("local change\n"), 0o600); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	gitAt(t, seed, "add", "state.txt")
	gitAt(t, seed, "commit", "-m", "local change")

	if err := PushWithRebaseRetry(WithDir(seed), WithBackoff(time.Millisecond)); err != nil {
		t.Fatalf("PushWithRebaseRetry should rebase and retry a non-fast-forward push, got: %v", err)
	}

	log := runGitOut(t, seed, "log", "--oneline", "origin/main")
	for _, want := range []string{"seed state", "concurrent write", "local change"} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected origin/main history to contain %q after retry, got:\n%s", want, log)
		}
	}
}

// TestPushWithRebaseRetry_AbortsRebaseOnConflict proves the exported push helper
// aborts the rebase and returns the wrapped error, leaving no mid-rebase state,
// when the pull --rebase conflicts. WithDir targets the seed clone directly.
func TestPushWithRebaseRetry_AbortsRebaseOnConflict(t *testing.T) {
	// The concurrent writer changes the same file, so the rebase conflicts.
	seed, _ := sharedRemoteClones(t, "state.txt", "base\n", "state.txt", "remote change\n")

	if err := os.WriteFile(filepath.Join(seed, "state.txt"), []byte("local change\n"), 0o600); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	gitAt(t, seed, "add", "state.txt")
	gitAt(t, seed, "commit", "-m", "local change")

	if err := PushWithRebaseRetry(WithDir(seed), WithBackoff(time.Millisecond)); err == nil {
		t.Fatal("PushWithRebaseRetry with a conflicting remote: expected error, got nil")
	}

	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, statErr := os.Stat(filepath.Join(seed, ".git", name)); statErr == nil {
			t.Fatalf("repository left mid-rebase: .git/%s still present", name)
		}
	}
}

// runGitOut runs a git command in dir and returns its trimmed stdout, failing the
// test on error.
func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git -C %s %s: %v", dir, strings.Join(args, " "), err)
	}
	return string(out)
}

// tagHead creates a lightweight tag pointing at the current HEAD.
func tagHead(t *testing.T, name string) {
	t.Helper()
	runGit(t, "tag", name)
}

func TestGetLatestTag_IgnoresNonVersionTags(t *testing.T) {
	dir := newScratchRepo(t)
	commitFile(t, "a.txt", "one", "first commit")

	// Valid version tags plus non-version tags that sort newer by base version.
	tagHead(t, "v0.5.0")
	tagHead(t, "v0.5.1")
	tagHead(t, "v0.6.0-dryrun.1") // higher base version, not a cascade version
	tagHead(t, "vnightly")        // foreign tag matching the prefix glob

	got, sha, err := GetLatestTag(dir, "v")
	if err != nil {
		t.Fatalf("GetLatestTag() unexpected error: %v", err)
	}
	if got != "v0.5.1" {
		t.Errorf("GetLatestTag() = %q, want %q (must ignore -dryrun and foreign tags)", got, "v0.5.1")
	}
	if sha == "" {
		t.Errorf("GetLatestTag() returned empty SHA for %q", got)
	}
}

func TestGetLatestReleaseTag_IgnoresNonVersionTags(t *testing.T) {
	dir := newScratchRepo(t)
	commitFile(t, "a.txt", "one", "first commit")

	tagHead(t, "v0.5.0")
	tagHead(t, "v0.5.1")
	tagHead(t, "v0.6.0-dryrun.1") // not an -rc tag, but also not a valid version
	tagHead(t, "vnightly")

	got, sha, err := GetLatestReleaseTag(dir, "v")
	if err != nil {
		t.Fatalf("GetLatestReleaseTag() unexpected error: %v", err)
	}
	if got != "v0.5.1" {
		t.Errorf("GetLatestReleaseTag() = %q, want %q (must ignore -dryrun and foreign tags)", got, "v0.5.1")
	}
	if sha == "" {
		t.Errorf("GetLatestReleaseTag() returned empty SHA for %q", got)
	}
}

func TestGetLatestReleaseTag_SkipsRCButKeepsValidRelease(t *testing.T) {
	newScratchRepo(t)
	commitFile(t, "a.txt", "one", "first commit")

	tagHead(t, "v1.0.0")
	tagHead(t, "v1.0.1-rc.0") // valid prerelease, must be skipped for "release"

	got, _, err := GetLatestReleaseTag("", "v")
	if err != nil {
		t.Fatalf("GetLatestReleaseTag() unexpected error: %v", err)
	}
	if got != "v1.0.0" {
		t.Errorf("GetLatestReleaseTag() = %q, want %q", got, "v1.0.0")
	}
}

// initRepoAt initializes a git repository at dir, commits a file, and creates a
// lightweight tag pointing at the resulting commit, all via "git -C" so the
// process working directory is never changed.
func initRepoAt(t *testing.T, dir, tag string) {
	t.Helper()
	for _, args := range [][]string{
		{"-C", dir, "init"},
		{"-C", dir, "config", "user.email", "test@example.com"},
		{"-C", dir, "config", "user.name", "Test User"},
		{"-C", dir, "config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	for _, args := range [][]string{
		{"-C", dir, "add", "f.txt"},
		{"-C", dir, "commit", "-m", "seed"},
		{"-C", dir, "tag", tag},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// TestGetLatestTag_ScopesToDir proves the lookup reads the repository at the
// given dir, not the process working directory. The cwd repo carries a decoy
// tag that sorts higher; a cwd-scoped lookup would return it.
func TestGetLatestTag_ScopesToDir(t *testing.T) {
	newScratchRepo(t) // cwd is a repo with a higher-sorting decoy tag
	commitFile(t, "a.txt", "one", "first commit")
	tagHead(t, "v9.9.9")

	target := t.TempDir()
	initRepoAt(t, target, "v1.2.3")

	got, sha, err := GetLatestTag(target, "v")
	if err != nil {
		t.Fatalf("GetLatestTag() unexpected error: %v", err)
	}
	if got != "v1.2.3" {
		t.Errorf("GetLatestTag() = %q, want %q (must read the dir repo, not cwd)", got, "v1.2.3")
	}
	if sha == "" {
		t.Errorf("GetLatestTag() returned empty SHA for %q", got)
	}
}

// TestGetLatestReleaseTag_ScopesToDir mirrors TestGetLatestTag_ScopesToDir for
// the release-tag lookup.
func TestGetLatestReleaseTag_ScopesToDir(t *testing.T) {
	newScratchRepo(t)
	commitFile(t, "a.txt", "one", "first commit")
	tagHead(t, "v9.9.9")

	target := t.TempDir()
	initRepoAt(t, target, "v1.2.3")

	got, sha, err := GetLatestReleaseTag(target, "v")
	if err != nil {
		t.Fatalf("GetLatestReleaseTag() unexpected error: %v", err)
	}
	if got != "v1.2.3" {
		t.Errorf("GetLatestReleaseTag() = %q, want %q (must read the dir repo, not cwd)", got, "v1.2.3")
	}
	if sha == "" {
		t.Errorf("GetLatestReleaseTag() returned empty SHA for %q", got)
	}
}

// TestGetLatestReleaseTag_CustomTokenNotMistakenForRelease proves the release
// classifier narrows on "parses with no pre-release" rather than a hard-wired
// "-rc." substring. Under a beta grammar, v1.2.3-beta.1 is a pre-release and
// must be skipped, so the lookup returns the real release v1.2.2.
func TestGetLatestReleaseTag_CustomTokenNotMistakenForRelease(t *testing.T) {
	newScratchRepo(t)
	commitFile(t, "a.txt", "one", "first commit")

	tagHead(t, "v1.2.2")        // published release
	tagHead(t, "v1.2.3-beta.1") // pre-release under the beta grammar

	spec := taggrammar.Default()
	spec.PreReleaseToken = "beta"

	got, _, err := GetLatestReleaseTagSpec("", spec)
	if err != nil {
		t.Fatalf("GetLatestReleaseTagSpec() unexpected error: %v", err)
	}
	if got != "v1.2.2" {
		t.Errorf("GetLatestReleaseTagSpec() = %q, want %q (beta pre-release must not count as a release)", got, "v1.2.2")
	}
}
