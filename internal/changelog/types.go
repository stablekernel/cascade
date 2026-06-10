package changelog

// ChangelogResult is the output of generate-changelog command
type ChangelogResult struct {
	Changelog   string `json:"changelog"`
	HasBreaking bool   `json:"has_breaking"`
	HasFeatures bool   `json:"has_features"`
	HasFixes    bool   `json:"has_fixes"`
}

// ConventionalCommit represents a parsed conventional commit
type ConventionalCommit struct {
	Type           string // feat, fix, docs, chore, etc.
	Scope          string // optional scope in parentheses
	Breaking       bool   // has ! or BREAKING CHANGE
	Description    string // the commit description
	Hash           string // short hash
	FullHash       string // full hash
	Author         string // commit author name
	AuthorEmail    string // author email for deduplication
	GitHubUsername string // GitHub username (looked up via API)
	PRNumber       string // PR number if present in description (e.g., "#123")
}
