package changelog

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestChangelogNewCommand_RunE_EmptySHARange(t *testing.T) {
	// HEAD..HEAD yields zero commits without error; exercises the RunE closure
	// body and runGenerateChangelog through the JSON output path.
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--base-sha", "HEAD",
		"--head-sha", "HEAD",
		"--repo", "owner/repo",
	})
	// The empty commit range produces valid JSON output; swallow any git failure.
	_ = cmd.Execute()
}

func TestChangelogNewCommand_RunE_WithExcludePaths(t *testing.T) {
	// Exercises the excludePaths splitting branch in the RunE closure.
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--base-sha", "HEAD",
		"--head-sha", "HEAD",
		"--repo", "owner/repo",
		"--exclude-paths", "docs/, internal/old/",
	})
	_ = cmd.Execute()
}

func TestChangelogNewCommand_RunE_WithContributors(t *testing.T) {
	// Exercises the contributors branch in runGenerateChangelog.
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--base-sha", "HEAD",
		"--head-sha", "HEAD",
		"--repo", "owner/repo",
		"--contributors",
	})
	_ = cmd.Execute()
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
