package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

func TestRemoteBranchSHA(t *testing.T) {
	dir := newScratchRepo(t)

	wantSHA := commitFile(t, "a.txt", "one", "first commit")
	runGit(t, "branch", "-M", "main")
	runGit(t, "branch", "feature")

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
		name    string
		remote  string
		branch  string
		want    string
		wantErr bool
	}{
		{name: "existing branch", remote: "origin", branch: "feature", want: wantSHA},
		{name: "missing branch", remote: "origin", branch: "nope", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RemoteBranchSHA(tt.remote, tt.branch)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("RemoteBranchSHA() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("RemoteBranchSHA() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("RemoteBranchSHA() = %q, want %q", got, tt.want)
			}
		})
	}
}

// tagHead creates a lightweight tag pointing at the current HEAD.
func tagHead(t *testing.T, name string) {
	t.Helper()
	runGit(t, "tag", name)
}

func TestGetLatestTag_IgnoresNonVersionTags(t *testing.T) {
	newScratchRepo(t)
	commitFile(t, "a.txt", "one", "first commit")

	// Valid version tags plus non-version tags that sort newer by base version.
	tagHead(t, "v0.5.0")
	tagHead(t, "v0.5.1")
	tagHead(t, "v0.6.0-dryrun.1") // higher base version, not a cascade version
	tagHead(t, "vnightly")        // foreign tag matching the prefix glob

	got, sha, err := GetLatestTag("v")
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
	newScratchRepo(t)
	commitFile(t, "a.txt", "one", "first commit")

	tagHead(t, "v0.5.0")
	tagHead(t, "v0.5.1")
	tagHead(t, "v0.6.0-dryrun.1") // not an -rc tag, but also not a valid version
	tagHead(t, "vnightly")

	got, sha, err := GetLatestReleaseTag("v")
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

	got, _, err := GetLatestReleaseTag("v")
	if err != nil {
		t.Fatalf("GetLatestReleaseTag() unexpected error: %v", err)
	}
	if got != "v1.0.0" {
		t.Errorf("GetLatestReleaseTag() = %q, want %q", got, "v1.0.0")
	}
}

func TestIsValidVersionTag(t *testing.T) {
	tests := []struct {
		tag  string
		want bool
	}{
		{"v1.2.3", true},
		{"v0.5.1", true},
		{"v1.0.1-rc.0", true},
		{"v1.0.1-rc.4.hotfix.5", true},
		{"1.2.3", true},        // empty prefix is allowed
		{"release1.2.3", true}, // alphabetic prefix is allowed
		{"v0.6.0-dryrun.1", false},
		{"vnightly", false},
		{"nightly", false},
		{"latest", false},
		{"v1.2", false},
		{"v1.2.3-rc", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			if got := IsValidVersionTag(tt.tag); got != tt.want {
				t.Errorf("IsValidVersionTag(%q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}
