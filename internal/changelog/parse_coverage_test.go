package changelog

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCommit_WithPRNumber(t *testing.T) {
	commit := git.Commit{
		Hash:    "abc123def456",
		Subject: "feat: add login endpoint (#42)",
		Body:    "",
	}
	cc := ParseCommit(commit)
	require.NotNil(t, cc)
	assert.Equal(t, "feat", cc.Type)
	assert.Equal(t, "42", cc.PRNumber)
	assert.Equal(t, "add login endpoint", cc.Description)
}

func TestParseCommit_WithOrgRepoPRRef(t *testing.T) {
	// PR references in description are extracted regardless of preceding text
	commit := git.Commit{
		Hash:    "def456abc123",
		Subject: "fix: resolve null pointer (#999)",
	}
	cc := ParseCommit(commit)
	require.NotNil(t, cc)
	assert.Equal(t, "999", cc.PRNumber)
	assert.Equal(t, "resolve null pointer", cc.Description)
}

func TestCategorizeCommits_NonRoutineNonFeatFix(t *testing.T) {
	// A commit type that is not feat/fix and is not a routine type
	// (e.g. "perf") should appear in the other slice.
	commits := []git.Commit{
		{Hash: "perf1234567", Subject: "perf(db): optimize query", Body: ""},
		{Hash: "build123456", Subject: "build(ci): speed up pipeline", Body: ""},
	}
	_, _, _, other := CategorizeCommits(commits)
	require.Len(t, other, 2)
	assert.Equal(t, "perf1234567", other[0].Hash)
	assert.Equal(t, "build123456", other[1].Hash)
}

func TestFormatMarkdown_CollapsibleFeatureSection(t *testing.T) {
	// collapsibleThreshold is 5; six commits should trigger the collapsible wrapper.
	features := make([]ConventionalCommit, 6)
	for i := range features {
		features[i] = ConventionalCommit{
			Description: fmt.Sprintf("feature number %d", i),
			Hash:        fmt.Sprintf("hash%04d", i),
			FullHash:    fmt.Sprintf("fullhash%08d", i),
		}
	}
	result := FormatMarkdown(nil, features, nil, nil, "owner/repo", "base123", "head456")

	assert.Contains(t, result, "<details>")
	assert.Contains(t, result, "<summary>")
	assert.Contains(t, result, "✨ Features")
	assert.Contains(t, result, "</details>")
}

func TestFormatMarkdown_CollapsibleOtherSection(t *testing.T) {
	// collapsibleThreshold is 5; six other commits should trigger the collapsible wrapper.
	other := make([]git.Commit, 6)
	for i := range other {
		other[i] = git.Commit{
			Hash:    fmt.Sprintf("otherhash%04d", i),
			Subject: fmt.Sprintf("other commit %d", i),
		}
	}
	result := FormatMarkdown(nil, nil, nil, other, "owner/repo", "base123", "head456")

	assert.Contains(t, result, "<details>")
	assert.Contains(t, result, "📝 Other Changes")
	assert.Contains(t, result, "</details>")
}

func TestFormatMarkdown_WithScopedCommits(t *testing.T) {
	// Commits with non-empty scopes should produce scope headers and
	// exercise the getSortedScopes named-scope branch.
	features := []ConventionalCommit{
		{Scope: "auth", Description: "add login", Hash: "abc1234", FullHash: "abc12345678"},
		{Scope: "api", Description: "add endpoint", Hash: "def1234", FullHash: "def12345678"},
	}
	result := FormatMarkdown(nil, features, nil, nil, "owner/repo", "base", "head")

	assert.Contains(t, result, "#### `auth`")
	assert.Contains(t, result, "#### `api`")
	assert.Contains(t, result, "add login")
	assert.Contains(t, result, "add endpoint")
}

func TestFormatMarkdown_MixedScopedAndUnscopedCommits(t *testing.T) {
	// Mix of scoped and unscoped commits: verifies both the named-scope header
	// and the trailing empty-scope group (no header rendered).
	features := []ConventionalCommit{
		{Scope: "auth", Description: "add login", Hash: "aaa1234", FullHash: "aaa12345678"},
		{Scope: "", Description: "general improvement", Hash: "bbb1234", FullHash: "bbb12345678"},
	}
	result := FormatMarkdown(nil, features, nil, nil, "owner/repo", "base", "head")

	assert.Contains(t, result, "#### `auth`")
	assert.Contains(t, result, "general improvement")
}

func TestFormatCommitLine_WithPRNumber(t *testing.T) {
	c := ConventionalCommit{
		Description: "add feature",
		Hash:        "abc1234",
		FullHash:    "abc1234567",
		PRNumber:    "42",
	}
	line := formatCommitLine(c, "owner/repo")

	assert.Contains(t, line, "[#42]")
	assert.Contains(t, line, "https://github.com/owner/repo/pull/42")
	assert.Contains(t, line, "add feature")
	assert.True(t, strings.HasPrefix(line, "- "))
}

func TestFormatCommitLine_WithoutPRNumber(t *testing.T) {
	c := ConventionalCommit{
		Description: "add feature",
		Hash:        "abc1234",
		FullHash:    "abc12345678",
	}
	line := formatCommitLine(c, "owner/repo")

	assert.Contains(t, line, "[`abc1234`]")
	assert.Contains(t, line, "https://github.com/owner/repo/commit/abc12345678")
}

func TestFormatOtherCommitLine_WithUsername(t *testing.T) {
	c := git.Commit{
		Hash:           "abc1234567",
		Subject:        "merge pull request",
		GitHubUsername: "alice",
	}
	line := formatOtherCommitLine(c, "owner/repo")

	assert.Contains(t, line, "(@alice)")
	assert.Contains(t, line, "merge pull request")
}

func TestFormatOtherCommitLine_WithoutUsername(t *testing.T) {
	c := git.Commit{
		Hash:    "abc1234567",
		Subject: "merge pull request",
	}
	line := formatOtherCommitLine(c, "owner/repo")

	assert.NotContains(t, line, "(@")
	assert.Contains(t, line, "merge pull request")
}
