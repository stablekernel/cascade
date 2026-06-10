package changelog

import (
	"encoding/json"
	"testing"

	"github.com/stablekernel/cascade/internal/git"
)

func TestLookupGitHubUsernames_EmptyInputs(t *testing.T) {
	tests := []struct {
		name    string
		commits []git.Commit
		repo    string
	}{
		{
			name:    "empty commits",
			commits: []git.Commit{},
			repo:    "owner/repo",
		},
		{
			name:    "nil commits",
			commits: nil,
			repo:    "owner/repo",
		},
		{
			name: "empty repo",
			commits: []git.Commit{
				{Hash: "abc123", AuthorEmail: "test@example.com"},
			},
			repo: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := LookupGitHubUsernames(tt.commits, tt.repo)
			// Should return input unchanged without making API calls
			if len(result) != len(tt.commits) {
				t.Errorf("LookupGitHubUsernames() changed slice length: got %d, want %d", len(result), len(tt.commits))
			}
		})
	}
}

func TestLookupGitHubUsernames_DeduplicationByEmail(t *testing.T) {
	// Test that commits are properly grouped by author email
	// This test verifies the deduplication logic without making actual API calls

	commits := []git.Commit{
		{Hash: "commit1", AuthorEmail: "alice@example.com"},
		{Hash: "commit2", AuthorEmail: "bob@example.com"},
		{Hash: "commit3", AuthorEmail: "alice@example.com"}, // Same as commit1
		{Hash: "commit4", AuthorEmail: "alice@example.com"}, // Same as commit1
		{Hash: "commit5", AuthorEmail: "carol@example.com"},
	}

	// Build the deduplication map (same logic as in LookupGitHubUsernames)
	emailToIndices := make(map[string][]int)
	emailToHash := make(map[string]string)

	for i, c := range commits {
		if c.AuthorEmail == "" {
			continue
		}
		emailToIndices[c.AuthorEmail] = append(emailToIndices[c.AuthorEmail], i)
		if _, exists := emailToHash[c.AuthorEmail]; !exists {
			emailToHash[c.AuthorEmail] = c.Hash
		}
	}

	// Verify unique authors
	if len(emailToHash) != 3 {
		t.Errorf("Expected 3 unique authors, got %d", len(emailToHash))
	}

	// Verify alice has 3 commits
	if len(emailToIndices["alice@example.com"]) != 3 {
		t.Errorf("Expected alice to have 3 commits, got %d", len(emailToIndices["alice@example.com"]))
	}

	// Verify first commit hash is used for alice
	if emailToHash["alice@example.com"] != "commit1" {
		t.Errorf("Expected alice's hash to be commit1, got %s", emailToHash["alice@example.com"])
	}
}

func TestGraphQLResponseUnmarshal(t *testing.T) {
	// Test the custom JSON unmarshaling for GraphQL response
	jsonData := `{
		"data": {
			"repository": {
				"c0": {
					"author": {
						"user": {
							"login": "alice"
						}
					}
				},
				"c1": {
					"author": {
						"user": {
							"login": "bob"
						}
					}
				},
				"c2": {
					"author": {
						"user": null
					}
				}
			}
		}
	}`

	var response graphQLResponse
	if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Check c0 (alice)
	if c0, ok := response.Data.Repository["c0"]; !ok {
		t.Error("Missing c0 in response")
	} else if c0.Author.User.Login != "alice" {
		t.Errorf("c0 login = %q, want 'alice'", c0.Author.User.Login)
	}

	// Check c1 (bob)
	if c1, ok := response.Data.Repository["c1"]; !ok {
		t.Error("Missing c1 in response")
	} else if c1.Author.User.Login != "bob" {
		t.Errorf("c1 login = %q, want 'bob'", c1.Author.User.Login)
	}

	// Check c2 (null user - unlinked email)
	if c2, ok := response.Data.Repository["c2"]; !ok {
		t.Error("Missing c2 in response")
	} else if c2.Author.User.Login != "" {
		t.Errorf("c2 login = %q, want empty string for null user", c2.Author.User.Login)
	}
}

func TestGraphQLResponseUnmarshal_EmptyRepository(t *testing.T) {
	jsonData := `{
		"data": {
			"repository": {}
		}
	}`

	var response graphQLResponse
	if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if len(response.Data.Repository) != 0 {
		t.Errorf("Expected empty repository, got %d entries", len(response.Data.Repository))
	}
}

func TestLookupConventionalCommitUsernames_EmptyInputs(t *testing.T) {
	tests := []struct {
		name    string
		commits []ConventionalCommit
		repo    string
	}{
		{
			name:    "empty commits",
			commits: []ConventionalCommit{},
			repo:    "owner/repo",
		},
		{
			name:    "nil commits",
			commits: nil,
			repo:    "owner/repo",
		},
		{
			name: "empty repo",
			commits: []ConventionalCommit{
				{FullHash: "abc123", AuthorEmail: "test@example.com"},
			},
			repo: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := LookupConventionalCommitUsernames(tt.commits, tt.repo)
			if len(result) != len(tt.commits) {
				t.Errorf("LookupConventionalCommitUsernames() changed slice length: got %d, want %d", len(result), len(tt.commits))
			}
		})
	}
}

func TestLookupGitHubUsernames_NoEmails(t *testing.T) {
	// Commits without author emails should not trigger API calls
	commits := []git.Commit{
		{Hash: "commit1", Author: "Alice"},
		{Hash: "commit2", Author: "Bob"},
	}

	result := LookupGitHubUsernames(commits, "owner/repo")

	// Should return unchanged (no emails to lookup)
	for i, c := range result {
		if c.GitHubUsername != "" {
			t.Errorf("Commit %d should have empty GitHubUsername, got %q", i, c.GitHubUsername)
		}
	}
}
