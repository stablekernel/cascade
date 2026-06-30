package changelog

import (
	"encoding/json"
	"testing"

	"github.com/stablekernel/cascade/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupAuthorsBatch_EmptyEmails(t *testing.T) {
	result := lookupAuthorsBatch("owner/repo", nil, nil)
	assert.Empty(t, result)
}

func TestLookupAuthorsBatch_EmptyEmailSlice(t *testing.T) {
	result := lookupAuthorsBatch("owner/repo", []string{}, map[string]string{})
	assert.Empty(t, result)
}

func TestLookupAuthorsBatch_InvalidRepoFormat(t *testing.T) {
	// A repo string without a slash causes an early return.
	emails := []string{"alice@example.com"}
	hashes := map[string]string{"alice@example.com": "abc123"}
	result := lookupAuthorsBatch("noslash", emails, hashes)
	assert.Empty(t, result)
}

func TestUnmarshalJSON_InvalidEntrySkipped(t *testing.T) {
	// When one repository entry cannot be decoded as a commitObject, it is
	// silently skipped; valid entries are still decoded correctly.
	jsonData := `{
		"data": {
			"repository": {
				"c0": {"author": {"user": {"login": "alice"}}},
				"c1": "not-an-object"
			}
		}
	}`

	var response graphQLResponse
	err := json.Unmarshal([]byte(jsonData), &response)
	require.NoError(t, err)

	c0, ok := response.Data.Repository["c0"]
	require.True(t, ok, "c0 should be present")
	assert.Equal(t, "alice", c0.Author.User.Login)

	// c1 had invalid structure and was skipped; it may be absent or have empty login.
	if c1, exists := response.Data.Repository["c1"]; exists {
		assert.Empty(t, c1.Author.User.Login, "skipped entry should have no login")
	}
}

func TestUnmarshalJSON_TopLevelError(t *testing.T) {
	// Malformed JSON should return an error from UnmarshalJSON.
	var response graphQLResponse
	err := json.Unmarshal([]byte("not json"), &response)
	assert.Error(t, err)
}

func TestLookupConventionalCommitUsernames_WithCommits(t *testing.T) {
	// Verify the conversion loop and the username copy-back loop both execute.
	// The gh CLI call will fail in the test environment; the important thing is
	// that the function does not panic and returns the same number of commits.
	commits := []ConventionalCommit{
		{FullHash: "abc1234567", AuthorEmail: "alice@example.com", Description: "first change"},
		{FullHash: "def1234567", AuthorEmail: "bob@example.com", Description: "second change"},
	}
	result := LookupConventionalCommitUsernames(commits, "owner/repo")
	require.Len(t, result, 2)
	assert.Equal(t, "first change", result[0].Description)
	assert.Equal(t, "second change", result[1].Description)
}

func TestLookupGitHubUsernames_InvalidRepoFormat(t *testing.T) {
	// A repo without a slash propagates to lookupAuthorsBatch which returns empty,
	// so no commit gets a username.
	commits := []git.Commit{
		{Hash: "abc123", AuthorEmail: "alice@example.com"},
	}
	result := LookupGitHubUsernames(commits, "noslash")
	require.Len(t, result, 1)
	assert.Empty(t, result[0].GitHubUsername)
}
