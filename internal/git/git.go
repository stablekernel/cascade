package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
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

// ListTags returns every tag in the repository. It returns an empty slice when
// the repository has no tags.
func ListTags() ([]string, error) {
	cmd := exec.Command("git", "tag", "-l")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git tag -l: %w", err)
	}
	return parseLines(output), nil
}

// CommitAndPushWithRetry stages filePath, commits it with message, and pushes
// to the current branch's upstream, retrying the push up to three times behind a
// pull --rebase. A "nothing to commit" state is treated as success (no-op). This
// is the manifest state-write path shared by promote and hotfix finalize: an
// API-created commit on real GitHub goes through a different path, so this is the
// plain-git fallback used when committing locally.
func CommitAndPushWithRetry(filePath, message string) error {
	cmd := exec.Command("git", "add", filePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(out), err)
	}

	cmd = exec.Command("git", "commit", "-m", message)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit failed: %s: %w", string(out), err)
	}

	for i := 0; i < 3; i++ {
		cmd = exec.Command("git", "push")
		if _, err := cmd.CombinedOutput(); err == nil {
			return nil
		}

		cmd = exec.Command("git", "pull", "--rebase")
		_, _ = cmd.CombinedOutput() // ignore error - best effort
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("git push failed after 3 retries")
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

// IsAncestor reports whether ancestor is an ancestor of descendant by running
// "git merge-base --is-ancestor". An exit code of 0 means true, an exit code of 1
// means false, and any other exit code or execution failure is returned as an error.
//
// Both commits must be present in the local object store. In a shallow clone the
// relevant history may be missing, so callers that rely on this must ensure full
// history is fetched (for example fetch-depth: 0).
func IsAncestor(ancestor, descendant string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 1 {
			return false, nil
		}
	}

	return false, fmt.Errorf("git merge-base --is-ancestor: %w", err)
}

// BranchExists reports whether the remote-tracking ref refs/remotes/<remote>/<name>
// exists by running "git rev-parse --verify". An exit code of 0 means the ref
// exists, a non-zero exit code means it does not, and an execution failure is
// returned as an error.
//
// This checks remote-tracking refs, so the remote must have been fetched first.
// A shallow or partial fetch that omits the branch will cause this to report false.
func BranchExists(remote, name string) (bool, error) {
	ref := fmt.Sprintf("refs/remotes/%s/%s", remote, name)
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}

	return false, fmt.Errorf("git rev-parse --verify %s: %w", ref, err)
}

// RemoteBranchSHA returns the SHA of the remote-tracking ref
// refs/remotes/<remote>/<name> by running "git rev-parse". The returned SHA is
// whitespace-trimmed. An error is returned if the ref cannot be resolved.
//
// This resolves a remote-tracking ref, so the remote must have been fetched first.
// A shallow or partial fetch that omits the branch will cause this to fail.
func RemoteBranchSHA(remote, name string) (string, error) {
	ref := fmt.Sprintf("refs/remotes/%s/%s", remote, name)
	cmd := exec.Command("git", "rev-parse", ref)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(output)), nil
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
