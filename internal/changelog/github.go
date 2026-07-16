package changelog

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/stablekernel/cascade/internal/git"
)

// LookupGitHubUsernames looks up GitHub usernames for commits via a single GraphQL query.
// It deduplicates by author email, so 500 commits by 5 authors results in 1 API call.
func LookupGitHubUsernames(commits []git.Commit, repo string) []git.Commit {
	if repo == "" || len(commits) == 0 {
		return commits
	}

	// Group commits by author email to find unique authors
	emailToIndices := make(map[string][]int)
	emailToHash := make(map[string]string) // Pick one commit hash per author

	for i, c := range commits {
		if c.AuthorEmail == "" {
			continue
		}
		emailToIndices[c.AuthorEmail] = append(emailToIndices[c.AuthorEmail], i)
		if _, exists := emailToHash[c.AuthorEmail]; !exists {
			emailToHash[c.AuthorEmail] = c.Hash
		}
	}

	if len(emailToHash) == 0 {
		return commits
	}

	// Build ordered list of emails for consistent alias mapping
	emails := make([]string, 0, len(emailToHash))
	for email := range emailToHash {
		emails = append(emails, email)
	}

	// Lookup all unique authors in a single GraphQL call
	emailToUsername := lookupAuthorsBatch(repo, emails, emailToHash)

	// Apply usernames to all commits by each author
	for email, username := range emailToUsername {
		if username == "" {
			continue
		}
		for _, idx := range emailToIndices[email] {
			commits[idx].GitHubUsername = username
		}
	}

	return commits
}

// lookupAuthorsBatch fetches GitHub usernames for multiple authors in a single GraphQL query.
func lookupAuthorsBatch(repo string, emails []string, emailToHash map[string]string) map[string]string {
	result := make(map[string]string)

	if len(emails) == 0 {
		return result
	}

	// Parse owner/repo
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return result
	}
	owner, name := parts[0], parts[1]

	// Build GraphQL query with aliases for each commit
	var queryParts []string
	for i, email := range emails {
		hash := emailToHash[email]
		// Use alias c0, c1, c2... for each commit lookup
		queryParts = append(queryParts, fmt.Sprintf(`c%d: object(oid: "%s") {
      ... on Commit {
        author {
          user {
            login
          }
        }
      }
    }`, i, hash))
	}

	query := fmt.Sprintf(`query {
  repository(owner: "%s", name: "%s") {
    %s
  }
}`, owner, name, strings.Join(queryParts, "\n    "))

	// Execute GraphQL query via gh cli
	cmd := exec.Command("gh", "api", "graphql", "-f", "query="+query)
	output, err := cmd.Output()
	if err != nil {
		return result
	}

	// Parse response
	var response graphQLResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return result
	}

	// Extract usernames from response
	for i, email := range emails {
		alias := fmt.Sprintf("c%d", i)
		if commitData, ok := response.Data.Repository[alias]; ok {
			if commitData.Author.User.Login != "" {
				result[email] = commitData.Author.User.Login
			}
		}
	}

	return result
}

// graphQLResponse represents the GitHub GraphQL API response structure
type graphQLResponse struct {
	Data struct {
		Repository map[string]commitObject `json:"-"`
	} `json:"data"`
}

// commitObject represents a commit in the GraphQL response
type commitObject struct {
	Author struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"author"`
}

// UnmarshalJSON handles the dynamic alias keys in the repository response
func (r *graphQLResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		Data struct {
			Repository map[string]json.RawMessage `json:"repository"`
		} `json:"data"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	r.Data.Repository = make(map[string]commitObject)
	for key, value := range raw.Data.Repository {
		var commit commitObject
		if err := json.Unmarshal(value, &commit); err != nil {
			continue // Skip invalid entries
		}
		r.Data.Repository[key] = commit
	}

	return nil
}
