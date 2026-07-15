// Package changelog generates release changelogs from conventional
// commit history, parsing commits into typed entries and enriching
// authors with GitHub usernames for release notes.
package changelog

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/stablekernel/cascade/internal/git"
)

// Section configuration
const (
	collapsibleThreshold = 5 // Use collapsible section when more than this many items
)

var (
	// Matches: type(scope)!: description or type!: description or type(scope): description or type: description
	conventionalPattern = regexp.MustCompile(`^([a-z]+)(?:\(([^)]+)\))?(!)?\s*:\s*(.+)$`)
	// Per Conventional Commits, `BREAKING CHANGE:` is a footer that appears
	// at the start of a line. Anchor with `(?m)^` and use `[ \t]+` between
	// BREAKING and CHANGE so word-wrapped narrative (`...BREAKING\nCHANGE:...`)
	// doesn't match. Earlier `\s+` accepted newlines and flagged unrelated
	// commits as breaking.
	breakingBodyPattern = regexp.MustCompile(`(?im)^BREAKING[ \t]+CHANGE\s*:`)
	// Matches PR references like (#123) or (org/repo#123)
	prPattern = regexp.MustCompile(`\(#(\d+)\)$`)
	// Matches @branch patterns that should be escaped (not real GitHub mentions)
	// This includes: @master, @main, @HEAD, @origin/*, @v1.0.0, @v1, etc.
	branchMentionPattern = regexp.MustCompile(`@(master|main|HEAD|origin/\S+|v\d+(?:\.\d+)*(?:-[a-zA-Z0-9.]+)?)(?:\b|$)`)
)

// ParseCommit parses a git commit into a ConventionalCommit
func ParseCommit(commit git.Commit) *ConventionalCommit {
	matches := conventionalPattern.FindStringSubmatch(commit.Subject)
	if matches == nil {
		return nil
	}

	description := matches[4]
	var prNumber string

	// Extract PR number from description if present (e.g., "add feature (#123)")
	if prMatches := prPattern.FindStringSubmatch(description); prMatches != nil {
		prNumber = prMatches[1]
		// Remove the PR reference from description for cleaner display
		description = strings.TrimSpace(prPattern.ReplaceAllString(description, ""))
	}

	cc := &ConventionalCommit{
		Type:           matches[1],
		Scope:          matches[2],
		Breaking:       matches[3] == "!",
		Description:    description,
		Hash:           shortHash(commit.Hash),
		FullHash:       commit.Hash,
		Author:         commit.Author,
		AuthorEmail:    commit.AuthorEmail,
		GitHubUsername: commit.GitHubUsername,
		PRNumber:       prNumber,
	}

	// Check body for BREAKING CHANGE:
	if !cc.Breaking && breakingBodyPattern.MatchString(commit.Body) {
		cc.Breaking = true
	}

	return cc
}

// IsRoutineType returns true for commit types that should be skipped in changelog
func IsRoutineType(t string) bool {
	switch t {
	case "docs", "chore", "ci", "test", "style", "refactor":
		return true
	default:
		return false
	}
}

func shortHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// CategorizeCommits groups commits by type
func CategorizeCommits(commits []git.Commit) (breaking, features, fixes []ConventionalCommit, other []git.Commit) {
	for _, commit := range commits {
		cc := ParseCommit(commit)
		if cc == nil {
			other = append(other, commit)
			continue
		}

		if cc.Breaking {
			breaking = append(breaking, *cc)
		} else if cc.Type == "feat" {
			features = append(features, *cc)
		} else if cc.Type == "fix" {
			fixes = append(fixes, *cc)
		} else if !IsRoutineType(cc.Type) {
			other = append(other, commit)
		}
		// Routine types (docs, chore, ci, test, style, refactor) are skipped
	}

	return
}

// FormatMarkdown generates a markdown changelog with emoji headers, scope grouping, and collapsible sections
func FormatMarkdown(breaking, features, fixes []ConventionalCommit, other []git.Commit, repo, baseSHA, headSHA string) string {
	var sb strings.Builder

	// Calculate totals for summary
	totalBreaking := len(breaking)
	totalFeatures := len(features)
	totalFixes := len(fixes)
	totalOther := len(other)

	// Only generate content if we have changes
	if totalBreaking+totalFeatures+totalFixes+totalOther == 0 {
		return ""
	}

	// Summary badges
	sb.WriteString(formatSummaryBadges(totalBreaking, totalFeatures, totalFixes, totalOther))
	sb.WriteString("\n")

	if totalBreaking > 0 {
		formatSection(&sb, "⚠️ Breaking Changes", breaking, repo, totalBreaking)
	}

	if totalFeatures > 0 {
		formatSection(&sb, "✨ Features", features, repo, totalFeatures)
	}

	if totalFixes > 0 {
		formatSection(&sb, "🐛 Bug Fixes", fixes, repo, totalFixes)
	}

	if totalOther > 0 {
		formatOtherSection(&sb, "📝 Other Changes", other, repo, totalOther)
	}

	// Full Changelog link
	sb.WriteString("---\n\n")
	compareURL := "https://github.com/" + repo + "/compare/" + shortHash(baseSHA) + "..." + shortHash(headSHA)
	sb.WriteString("**Full Changelog**: [`" + shortHash(baseSHA) + "..." + shortHash(headSHA) + "`](" + compareURL + ")\n")

	return sb.String()
}

// formatSection writes a section with commits grouped by scope
func formatSection(sb *strings.Builder, title string, commits []ConventionalCommit, repo string, count int) {
	// Group commits by scope
	grouped := groupByScope(commits)
	scopes := getSortedScopes(grouped)

	// Determine if we need collapsible
	useCollapsible := count > collapsibleThreshold

	if useCollapsible {
		fmt.Fprintf(sb, "<details>\n<summary><h3>%s (%d)</h3></summary>\n\n", title, count)
	} else {
		fmt.Fprintf(sb, "### %s\n\n", title)
	}

	for _, scope := range scopes {
		scopeCommits := grouped[scope]
		if scope != "" {
			fmt.Fprintf(sb, "#### `%s`\n", scope)
		}
		for _, c := range scopeCommits {
			sb.WriteString(formatCommitLine(c, repo))
		}
		if scope != "" {
			sb.WriteString("\n")
		}
	}

	if useCollapsible {
		sb.WriteString("</details>\n\n")
	} else {
		sb.WriteString("\n")
	}
}

// formatOtherSection writes the other changes section
func formatOtherSection(sb *strings.Builder, title string, commits []git.Commit, repo string, count int) {
	useCollapsible := count > collapsibleThreshold

	if useCollapsible {
		fmt.Fprintf(sb, "<details>\n<summary><h3>%s (%d)</h3></summary>\n\n", title, count)
	} else {
		fmt.Fprintf(sb, "### %s\n\n", title)
	}

	for _, c := range commits {
		sb.WriteString(formatOtherCommitLine(c, repo))
	}

	if useCollapsible {
		sb.WriteString("</details>\n\n")
	} else {
		sb.WriteString("\n")
	}
}

// groupByScope groups commits by their scope
func groupByScope(commits []ConventionalCommit) map[string][]ConventionalCommit {
	grouped := make(map[string][]ConventionalCommit)
	for _, c := range commits {
		scope := c.Scope
		grouped[scope] = append(grouped[scope], c)
	}
	return grouped
}

// getSortedScopes returns scopes sorted alphabetically, with empty scope last
func getSortedScopes(grouped map[string][]ConventionalCommit) []string {
	var scopes []string
	hasEmpty := false
	for scope := range grouped {
		if scope == "" {
			hasEmpty = true
		} else {
			scopes = append(scopes, scope)
		}
	}
	sort.Strings(scopes)
	if hasEmpty {
		scopes = append(scopes, "") // Empty scope goes last
	}
	return scopes
}

// formatSummaryBadges creates visual summary badges
func formatSummaryBadges(breaking, features, fixes, other int) string {
	var badges []string

	if breaking > 0 {
		badges = append(badges, fmt.Sprintf("![Breaking Changes](https://img.shields.io/badge/breaking-%d-red)", breaking))
	}
	if features > 0 {
		badges = append(badges, fmt.Sprintf("![Features](https://img.shields.io/badge/features-%d-blue)", features))
	}
	if fixes > 0 {
		badges = append(badges, fmt.Sprintf("![Bug Fixes](https://img.shields.io/badge/fixes-%d-green)", fixes))
	}
	if other > 0 {
		badges = append(badges, fmt.Sprintf("![Other](https://img.shields.io/badge/other-%d-gray)", other))
	}

	return strings.Join(badges, " ") + "\n"
}

func formatCommitLine(c ConventionalCommit, repo string) string {
	// Build reference (PR or commit hash)
	var ref string
	if c.PRNumber != "" {
		ref = fmt.Sprintf("[#%s](https://github.com/%s/pull/%s)", c.PRNumber, repo, c.PRNumber)
	} else {
		ref = fmt.Sprintf("[`%s`](https://github.com/%s/commit/%s)", c.Hash, repo, c.FullHash)
	}

	// Build attribution
	var attribution string
	if c.GitHubUsername != "" {
		attribution = fmt.Sprintf(" (@%s)", c.GitHubUsername)
	}

	// Escape @branch patterns to prevent GitHub from treating them as user mentions
	description := escapeBranchMentions(c.Description)

	return fmt.Sprintf("- %s %s%s\n", description, ref, attribution)
}

// escapeBranchMentions replaces @ with HTML entity &#64; for branch/tag patterns
// to prevent GitHub from interpreting them as user mentions in release notes.
// Examples: @master -> &#64;master, @v1.0.0 -> &#64;v1.0.0
// Note: Backticks don't work - GitHub still parses @mentions in code spans.
func escapeBranchMentions(text string) string {
	return branchMentionPattern.ReplaceAllString(text, "&#64;$1")
}

func formatOtherCommitLine(c git.Commit, repo string) string {
	hash := shortHash(c.Hash)
	ref := fmt.Sprintf("[`%s`](https://github.com/%s/commit/%s)", hash, repo, c.Hash)

	var attribution string
	if c.GitHubUsername != "" {
		attribution = fmt.Sprintf(" (@%s)", c.GitHubUsername)
	}

	// Escape @branch patterns to prevent GitHub from treating them as user mentions
	subject := escapeBranchMentions(c.Subject)

	return fmt.Sprintf("- %s %s%s\n", subject, ref, attribution)
}
