package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// GetChangedFiles returns the list of files changed between two commits
func GetChangedFiles(baseSHA, headSHA string) ([]string, error) {
	// Handle null SHA (new branch or first commit)
	if baseSHA == "0000000000000000000000000000000000000000" {
		return getAllFiles(headSHA)
	}

	cmd := exec.Command("git", "diff", "--name-only", baseSHA, headSHA)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	return parseLines(output), nil
}

// getAllFiles returns all files in the tree at the given SHA
func getAllFiles(sha string) ([]string, error) {
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", sha)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree: %w", err)
	}

	return parseLines(output), nil
}

// Commit represents a git commit
type Commit struct {
	Hash           string
	Subject        string
	Body           string
	Author         string
	AuthorEmail    string // Author email for deduplication
	GitHubUsername string // GitHub username (looked up via API)
}

// GetCommits returns commits between two SHAs
func GetCommits(baseSHA, headSHA string, excludePaths []string) ([]Commit, error) {
	// Build command with format: hash, subject, author, author email, body separated by special markers
	// Using ASCII record separator (0x1E) and unit separator (0x1F) which won't appear in commit messages
	format := "%H\x1f%s\x1f%an\x1f%ae\x1f%b\x1e"

	args := []string{"log", fmt.Sprintf("--format=%s", format), fmt.Sprintf("%s..%s", baseSHA, headSHA)}

	// Add path exclusions if specified
	if len(excludePaths) > 0 {
		args = append(args, "--")
		for _, p := range excludePaths {
			args = append(args, ":!"+p)
		}
	}

	cmd := exec.Command("git", args...)
	output, err := cmd.Output()
	if err != nil {
		// No commits in range is not an error
		if len(output) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("git log: %w", err)
	}

	return parseCommits(output), nil
}

func parseCommits(data []byte) []Commit {
	var commits []Commit

	// Split by record separator
	records := bytes.Split(data, []byte{0x1e})

	for _, record := range records {
		record = bytes.TrimSpace(record)
		if len(record) == 0 {
			continue
		}

		// Split by unit separator: hash, subject, author, author email, body
		parts := bytes.Split(record, []byte{0x1f})
		if len(parts) < 4 {
			continue
		}

		commit := Commit{
			Hash:        string(bytes.TrimSpace(parts[0])),
			Subject:     string(bytes.TrimSpace(parts[1])),
			Author:      string(bytes.TrimSpace(parts[2])),
			AuthorEmail: string(bytes.TrimSpace(parts[3])),
		}
		if len(parts) > 4 {
			commit.Body = string(bytes.TrimSpace(parts[4]))
		}

		if commit.Hash != "" {
			commits = append(commits, commit)
		}
	}

	return commits
}

func parseLines(data []byte) []string {
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// GetInitialCommit returns the SHA of the first commit in the repository
func GetInitialCommit() (string, error) {
	cmd := exec.Command("git", "rev-list", "--max-parents=0", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-list: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetLatestTag returns the most recent tag matching the given prefix, sorted by semver.
// Returns empty string if no matching tags found.
func GetLatestTag(prefix string) (string, string, error) {
	// Get all tags matching prefix, sorted by version descending
	// --sort=-v:refname sorts by version in descending order
	cmd := exec.Command("git", "tag", "-l", prefix+"*", "--sort=-v:refname")
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git tag: %w", err)
	}

	tags := parseLines(output)
	if len(tags) == 0 {
		return "", "", nil
	}

	// First tag is the latest (sorted descending)
	latestTag := tags[0]

	// Get the SHA for this tag
	cmd = exec.Command("git", "rev-list", "-n", "1", latestTag)
	output, err = cmd.Output()
	if err != nil {
		return latestTag, "", fmt.Errorf("git rev-list for tag: %w", err)
	}

	return latestTag, strings.TrimSpace(string(output)), nil
}

// CommitAndPush stages a file, commits with the given message, and pushes to origin.
func CommitAndPush(filePath, message string) error {
	// Stage the file
	cmd := exec.Command("git", "add", filePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w\n%s", err, output)
	}

	// Commit
	cmd = exec.Command("git", "commit", "-m", message)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w\n%s", err, output)
	}

	// Push
	cmd = exec.Command("git", "push")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %w\n%s", err, output)
	}

	return nil
}

// GetLatestReleaseTag returns the most recent non-prerelease tag (no -rc suffix).
// This is used to find the base version for calculating next release versions.
func GetLatestReleaseTag(prefix string) (string, string, error) {
	cmd := exec.Command("git", "tag", "-l", prefix+"*", "--sort=-v:refname")
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git tag: %w", err)
	}

	tags := parseLines(output)
	if len(tags) == 0 {
		return "", "", nil
	}

	// Find first tag without -rc suffix (published release)
	for _, tag := range tags {
		if !strings.Contains(tag, "-rc.") {
			// Get the SHA for this tag
			cmd = exec.Command("git", "rev-list", "-n", "1", tag)
			output, err = cmd.Output()
			if err != nil {
				return tag, "", fmt.Errorf("git rev-list for tag: %w", err)
			}
			return tag, strings.TrimSpace(string(output)), nil
		}
	}

	return "", "", nil
}
