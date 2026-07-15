package changelog

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChangelogNewCommand_Structure(t *testing.T) {
	cmd := NewCommand()

	assert.Equal(t, "generate-changelog", cmd.Use)
	assert.NotEmpty(t, cmd.Short)

	assert.NotNil(t, cmd.Flags().Lookup("base-sha"))
	assert.NotNil(t, cmd.Flags().Lookup("head-sha"))
	assert.NotNil(t, cmd.Flags().Lookup("repo"))
	assert.NotNil(t, cmd.Flags().Lookup("exclude-paths"))
	assert.NotNil(t, cmd.Flags().Lookup("contributors"))
}

// changelogRepo builds a hermetic git repo with a chore base commit, a feat
// touching src/, and a fix touching docs/, changes into it, and returns the
// base commit SHA.
func changelogRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	gitCL(t, "init", "-b", "main")
	gitCL(t, "config", "user.email", "dev@example.com")
	gitCL(t, "config", "user.name", "Dev Example")
	gitCL(t, "config", "commit.gpgsign", "false")

	commitFileCL(t, "README.md", "seed", "chore: seed")
	base := gitOutCL(t, "rev-parse", "HEAD")
	commitFileCL(t, filepath.Join("src", "parser.go"), "package src", "feat: add parser")
	commitFileCL(t, filepath.Join("docs", "guide.md"), "guide", "fix: correct guide typo")
	return base
}

func gitCL(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

func gitOutCL(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).Output()
	require.NoError(t, err, "git %v", args)
	return strings.TrimSpace(string(out))
}

func commitFileCL(t *testing.T, path, content, message string) {
	t.Helper()
	if d := filepath.Dir(path); d != "." {
		require.NoError(t, os.MkdirAll(d, 0o750))
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	gitCL(t, "add", path)
	gitCL(t, "commit", "-m", message)
}

// executeChangelogCapture runs generate-changelog with args and returns what
// it wrote to os.Stdout (the command prints there directly, not to the cobra
// writer) decoded as a ChangelogResult.
func executeChangelogCapture(t *testing.T, args []string) ChangelogResult {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	cmd := NewCommand()
	var cmdOut bytes.Buffer
	cmd.SetOut(&cmdOut)
	cmd.SetErr(&cmdOut)
	cmd.SetArgs(args)
	runErr := cmd.Execute()

	require.NoError(t, w.Close())
	data, readErr := io.ReadAll(r)
	os.Stdout = orig
	require.NoError(t, readErr)
	require.NoError(t, runErr)

	var result ChangelogResult
	require.NoError(t, json.Unmarshal(data, &result), "stdout is not a ChangelogResult: %s", data)
	return result
}

// TestChangelogNewCommand_RunE_EmptyRangeEmitsEmptyResult pins the empty-range
// contract: HEAD..HEAD succeeds and emits an empty changelog with every
// category flag false, rather than erroring or fabricating content.
func TestChangelogNewCommand_RunE_EmptyRangeEmitsEmptyResult(t *testing.T) {
	changelogRepo(t)

	result := executeChangelogCapture(t, []string{
		"--base-sha", "HEAD",
		"--head-sha", "HEAD",
		"--repo", "owner/repo",
	})
	assert.Equal(t, ChangelogResult{}, result)
}

// TestChangelogNewCommand_RunE_ExcludePathsDropOnlyMatchingCommits proves the
// exclude-paths flag filters commits by pathspec: the docs-only fix disappears
// while the src feat survives. The unfiltered control run shows both, so the
// assertion fails if exclusion stops working (or over-filters). The exclude
// list carries surrounding whitespace to pin the documented trimming.
func TestChangelogNewCommand_RunE_ExcludePathsDropOnlyMatchingCommits(t *testing.T) {
	base := changelogRepo(t)

	unfiltered := executeChangelogCapture(t, []string{
		"--base-sha", base,
		"--head-sha", "HEAD",
		"--repo", "owner/repo",
	})
	assert.True(t, unfiltered.HasFeatures)
	assert.True(t, unfiltered.HasFixes)
	assert.False(t, unfiltered.HasBreaking)
	assert.Contains(t, unfiltered.Changelog, "add parser")
	assert.Contains(t, unfiltered.Changelog, "correct guide typo")

	filtered := executeChangelogCapture(t, []string{
		"--base-sha", base,
		"--head-sha", "HEAD",
		"--repo", "owner/repo",
		"--exclude-paths", "docs/ , internal/old/",
	})
	assert.True(t, filtered.HasFeatures)
	assert.False(t, filtered.HasFixes)
	assert.Contains(t, filtered.Changelog, "add parser")
	assert.NotContains(t, filtered.Changelog, "correct guide typo")
}

// TestChangelogNewCommand_RunE_ContributorsAttributeUsernames proves the
// contributors flag flows a looked-up GitHub username into the rendered
// changelog lines. The gh CLI is stubbed with a fixed GraphQL response so the
// lookup is hermetic: the repo's single author resolves to octocat, and both
// the feat and fix lines must carry the attribution.
func TestChangelogNewCommand_RunE_ContributorsAttributeUsernames(t *testing.T) {
	base := changelogRepo(t)

	stubDir := t.TempDir()
	stub := "#!/bin/sh\n" +
		`echo '{"data":{"repository":{"c0":{"author":{"user":{"login":"octocat"}}}}}}'` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "gh"), []byte(stub), 0o755)) //nolint:gosec // test stub must be executable
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result := executeChangelogCapture(t, []string{
		"--base-sha", base,
		"--head-sha", "HEAD",
		"--repo", "owner/repo",
		"--contributors",
	})
	assert.Contains(t, result.Changelog, "add parser")
	assert.Contains(t, result.Changelog, "correct guide typo")
	assert.Equal(t, 2, strings.Count(result.Changelog, "(@octocat)"),
		"both commit lines should carry the stubbed username attribution")
}

func TestChangelogNewCommand_MissingRequiredFlags(t *testing.T) {
	// Omitting required flags produces an error before RunE runs.
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--repo", "owner/repo"})
	err := cmd.Execute()
	assert.Error(t, err)
}
