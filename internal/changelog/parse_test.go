package changelog

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/git"
)

func TestParseCommit(t *testing.T) {
	tests := []struct {
		name    string
		commit  git.Commit
		want    *ConventionalCommit
		wantNil bool
	}{
		{
			name: "feat with scope",
			commit: git.Commit{
				Hash:    "abc123def456",
				Subject: "feat(auth): add login endpoint",
				Body:    "",
			},
			want: &ConventionalCommit{
				Type:        "feat",
				Scope:       "auth",
				Breaking:    false,
				Description: "add login endpoint",
				Hash:        "abc123d",
				FullHash:    "abc123def456",
			},
		},
		{
			name: "fix without scope",
			commit: git.Commit{
				Hash:    "def456abc123",
				Subject: "fix: correct typo in error message",
				Body:    "",
			},
			want: &ConventionalCommit{
				Type:        "fix",
				Scope:       "",
				Breaking:    false,
				Description: "correct typo in error message",
				Hash:        "def456a",
				FullHash:    "def456abc123",
			},
		},
		{
			name: "breaking change with !",
			commit: git.Commit{
				Hash:    "111222333444",
				Subject: "feat(api)!: change response format",
				Body:    "",
			},
			want: &ConventionalCommit{
				Type:        "feat",
				Scope:       "api",
				Breaking:    true,
				Description: "change response format",
				Hash:        "1112223",
				FullHash:    "111222333444",
			},
		},
		{
			name: "breaking change in body",
			commit: git.Commit{
				Hash:    "555666777888",
				Subject: "feat: update config format",
				Body:    "BREAKING CHANGE: config keys are now camelCase",
			},
			want: &ConventionalCommit{
				Type:        "feat",
				Scope:       "",
				Breaking:    true,
				Description: "update config format",
				Hash:        "5556667",
				FullHash:    "555666777888",
			},
		},
		{
			name: "breaking change case insensitive",
			commit: git.Commit{
				Hash:    "aabbccdd1122",
				Subject: "fix: remove deprecated API",
				Body:    "breaking change: old API removed",
			},
			want: &ConventionalCommit{
				Type:        "fix",
				Scope:       "",
				Breaking:    true,
				Description: "remove deprecated API",
				Hash:        "aabbccd",
				FullHash:    "aabbccdd1122",
			},
		},
		{
			name: "non-conventional commit",
			commit: git.Commit{
				Hash:    "999000111222",
				Subject: "Update README",
				Body:    "",
			},
			wantNil: true,
		},
		{
			name: "chore commit",
			commit: git.Commit{
				Hash:    "chore1234567",
				Subject: "chore: update dependencies",
				Body:    "",
			},
			want: &ConventionalCommit{
				Type:        "chore",
				Scope:       "",
				Breaking:    false,
				Description: "update dependencies",
				Hash:        "chore12",
				FullHash:    "chore1234567",
			},
		},
		{
			name: "docs with scope",
			commit: git.Commit{
				Hash:    "docs12345678",
				Subject: "docs(readme): add installation guide",
				Body:    "",
			},
			want: &ConventionalCommit{
				Type:        "docs",
				Scope:       "readme",
				Breaking:    false,
				Description: "add installation guide",
				Hash:        "docs123",
				FullHash:    "docs12345678",
			},
		},
		{
			name: "fix with breaking bang no scope",
			commit: git.Commit{
				Hash:    "bang12345678",
				Subject: "fix!: remove deprecated function",
				Body:    "",
			},
			want: &ConventionalCommit{
				Type:        "fix",
				Scope:       "",
				Breaking:    true,
				Description: "remove deprecated function",
				Hash:        "bang123",
				FullHash:    "bang12345678",
			},
		},
		{
			name: "narrative referencing BREAKING CHANGE wrapped across lines is not a footer",
			commit: git.Commit{
				Hash:    "wrap1234abcd",
				Subject: "fix(promote): wire breaking-change gate to conventional-commit parser",
				Body: "Replace the placeholder with real parsing: walk history and return\n" +
					"true if any commit is breaking (`feat!:`, `fix(scope)!:`, or any\n" +
					"commit with a `BREAKING\nCHANGE:` body footer).",
			},
			want: &ConventionalCommit{
				Type:        "fix",
				Scope:       "promote",
				Breaking:    false,
				Description: "wire breaking-change gate to conventional-commit parser",
				Hash:        "wrap123",
				FullHash:    "wrap1234abcd",
			},
		},
		{
			name: "BREAKING CHANGE footer at body start matches",
			commit: git.Commit{
				Hash:    "footer123abc",
				Subject: "feat: rework auth",
				Body:    "BREAKING CHANGE: tokens are now signed",
			},
			want: &ConventionalCommit{
				Type:        "feat",
				Scope:       "",
				Breaking:    true,
				Description: "rework auth",
				Hash:        "footer1",
				FullHash:    "footer123abc",
			},
		},
		{
			name: "BREAKING CHANGE footer mid-body on its own line matches",
			commit: git.Commit{
				Hash:    "midfooter123",
				Subject: "feat: bump api version",
				Body:    "Some narrative first.\n\nBREAKING CHANGE: rename foo to bar",
			},
			want: &ConventionalCommit{
				Type:        "feat",
				Scope:       "",
				Breaking:    true,
				Description: "bump api version",
				Hash:        "midfoot",
				FullHash:    "midfooter123",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCommit(tt.commit)
			if tt.wantNil {
				if got != nil {
					t.Errorf("ParseCommit() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("ParseCommit() = nil, want %+v", tt.want)
				return // unreachable but satisfies staticcheck
			}
			if got.Type != tt.want.Type {
				t.Errorf("Type = %q, want %q", got.Type, tt.want.Type)
			}
			if got.Scope != tt.want.Scope {
				t.Errorf("Scope = %q, want %q", got.Scope, tt.want.Scope)
			}
			if got.Breaking != tt.want.Breaking {
				t.Errorf("Breaking = %v, want %v", got.Breaking, tt.want.Breaking)
			}
			if got.Description != tt.want.Description {
				t.Errorf("Description = %q, want %q", got.Description, tt.want.Description)
			}
			if got.Hash != tt.want.Hash {
				t.Errorf("Hash = %q, want %q", got.Hash, tt.want.Hash)
			}
		})
	}
}

func TestIsRoutineType(t *testing.T) {
	routine := []string{"docs", "chore", "ci", "test", "style", "refactor"}
	notRoutine := []string{"feat", "fix", "perf", "build"}

	for _, typ := range routine {
		if !IsRoutineType(typ) {
			t.Errorf("IsRoutineType(%q) = false, want true", typ)
		}
	}

	for _, typ := range notRoutine {
		if IsRoutineType(typ) {
			t.Errorf("IsRoutineType(%q) = true, want false", typ)
		}
	}
}

func TestCategorizeCommits(t *testing.T) {
	commits := []git.Commit{
		{Hash: "feat1111111", Subject: "feat: add new feature", Body: ""},
		{Hash: "fix11111111", Subject: "fix: bug fix", Body: ""},
		{Hash: "break111111", Subject: "feat!: breaking feature", Body: ""},
		{Hash: "docs1111111", Subject: "docs: update readme", Body: ""},
		{Hash: "chore111111", Subject: "chore: update deps", Body: ""},
		{Hash: "other111111", Subject: "Merge pull request #123", Body: ""},
		{Hash: "feat2222222", Subject: "feat(api): another feature", Body: ""},
		{Hash: "fix22222222", Subject: "fix(db): database fix", Body: "BREAKING CHANGE: schema changed"},
	}

	breaking, features, fixes, other := CategorizeCommits(commits)

	// Check breaking changes
	if len(breaking) != 2 {
		t.Errorf("len(breaking) = %d, want 2", len(breaking))
	}

	// Check features (non-breaking)
	if len(features) != 2 {
		t.Errorf("len(features) = %d, want 2", len(features))
	}

	// Check fixes (non-breaking)
	if len(fixes) != 1 {
		t.Errorf("len(fixes) = %d, want 1", len(fixes))
	}

	// Check other (non-conventional only, routine types are filtered)
	if len(other) != 1 {
		t.Errorf("len(other) = %d, want 1 (Merge commit)", len(other))
	}
}

func TestFormatMarkdown(t *testing.T) {
	breaking := []ConventionalCommit{
		{Description: "breaking change", Hash: "break12", FullHash: "break1234567"},
	}
	features := []ConventionalCommit{
		{Description: "new feature", Hash: "feat123", FullHash: "feat1234567"},
	}
	fixes := []ConventionalCommit{
		{Description: "bug fix", Hash: "fix1234", FullHash: "fix12345678"},
	}
	other := []git.Commit{
		{Hash: "other123456", Subject: "Other change"},
	}

	result := FormatMarkdown(breaking, features, fixes, other, "owner/repo", "base123", "head456")

	// Check sections exist with emoji headers
	if !strings.Contains(result, "### ⚠️ Breaking Changes") {
		t.Error("Missing Breaking Changes section")
	}
	if !strings.Contains(result, "### ✨ Features") {
		t.Error("Missing Features section")
	}
	if !strings.Contains(result, "### 🐛 Bug Fixes") {
		t.Error("Missing Bug Fixes section")
	}
	if !strings.Contains(result, "### 📝 Other Changes") {
		t.Error("Missing Other Changes section")
	}

	// Check summary badges
	if !strings.Contains(result, "![Breaking Changes]") {
		t.Error("Missing breaking changes badge")
	}

	// Check links
	if !strings.Contains(result, "https://github.com/owner/repo/commit/break1234567") {
		t.Error("Missing breaking change commit link")
	}
	if !strings.Contains(result, "https://github.com/owner/repo/compare/base123...head456") {
		t.Error("Missing compare link")
	}
}

func TestFormatMarkdown_Empty(t *testing.T) {
	result := FormatMarkdown(nil, nil, nil, nil, "owner/repo", "base", "head")

	if result != "" {
		t.Errorf("FormatMarkdown() with empty inputs = %q, want empty string", result)
	}
}

func TestFormatMarkdown_WithContributors(t *testing.T) {
	features := []ConventionalCommit{
		{Description: "new feature", Hash: "feat123", FullHash: "feat1234567", GitHubUsername: "alice"},
	}
	fixes := []ConventionalCommit{
		{Description: "bug fix", Hash: "fix1234", FullHash: "fix12345678", GitHubUsername: "bob"},
	}

	result := FormatMarkdown(nil, features, fixes, nil, "owner/repo", "base123", "head456")

	// Check inline attribution exists (the "(@username)" format).
	if !strings.Contains(result, "(@alice)") {
		t.Error("Missing inline contributor attribution for alice")
	}
	if !strings.Contains(result, "(@bob)") {
		t.Error("Missing inline contributor attribution for bob")
	}

	// Verify no separate Contributors section
	if strings.Contains(result, "### Contributors") {
		t.Error("Should not have separate Contributors section - attribution is inline")
	}
}

func TestShortHash(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abc123def456789", "abc123d"},
		{"short", "short"},
		{"exactly7", "exactly"},
		{"", ""},
	}

	for _, tt := range tests {
		got := shortHash(tt.input)
		if got != tt.want {
			t.Errorf("shortHash(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestEscapeBranchMentions(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Branch names should be escaped
		{"use @master for setup-cli action", "use &#64;master for setup-cli action"},
		{"use @main ref", "use &#64;main ref"},
		{"reset to @HEAD", "reset to &#64;HEAD"},
		{"fetch from @origin/master", "fetch from &#64;origin/master"},

		// Version tags should be escaped
		{"update to @v1", "update to &#64;v1"},
		{"update to @v1.0.0", "update to &#64;v1.0.0"},
		{"update to @v1.0.0-rc.0", "update to &#64;v1.0.0-rc.0"},
		{"use @v2.3.4-beta.1", "use &#64;v2.3.4-beta.1"},

		// Real usernames should NOT be escaped
		{"by @octocat", "by @octocat"},
		{"thanks @alice123", "thanks @alice123"},
		{"cc @bob_smith", "cc @bob_smith"},

		// Mixed content
		{"use @master ref by @octocat", "use &#64;master ref by @octocat"},
		{"update @v1.0.0 and @v2.0.0", "update &#64;v1.0.0 and &#64;v2.0.0"},

		// No matches
		{"simple description", "simple description"},
		{"email user@example.com", "email user@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeBranchMentions(tt.input)
			if got != tt.want {
				t.Errorf("escapeBranchMentions(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func ExampleParseCommit() {
	cc := ParseCommit(git.Commit{
		Hash:    "1234567890abcdef",
		Subject: "feat(api): add retry support (#42)",
		Author:  "Ada Lovelace",
	})
	fmt.Printf("%s(%s): %s [PR %s]\n", cc.Type, cc.Scope, cc.Description, cc.PRNumber)
	// Output: feat(api): add retry support [PR 42]
}
