package changelog

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/git"
)

// NewCommand creates the generate-changelog command
func NewCommand() *cobra.Command {
	var baseSHA string
	var headSHA string
	var repo string
	var excludePaths string
	var contributors bool

	cmd := &cobra.Command{
		Use:   "generate-changelog",
		Short: "Generate changelog from conventional commits",
		Long: `Generate a markdown changelog from commits between two SHAs.
Parses conventional commit messages and groups them by type.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var excludeList []string
			if excludePaths != "" {
				for _, p := range strings.Split(excludePaths, ",") {
					p = strings.TrimSpace(p)
					if p != "" {
						excludeList = append(excludeList, p)
					}
				}
			}
			return runGenerateChangelog(baseSHA, headSHA, repo, excludeList, contributors)
		},
	}

	cmd.Flags().StringVar(&baseSHA, "base-sha", "", "Base commit SHA")
	cmd.Flags().StringVar(&headSHA, "head-sha", "", "Head commit SHA")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository in owner/repo format")
	cmd.Flags().StringVar(&excludePaths, "exclude-paths", "", "Comma-separated paths to exclude")
	cmd.Flags().BoolVar(&contributors, "contributors", false, "Include contributors section")

	_ = cmd.MarkFlagRequired("base-sha")
	_ = cmd.MarkFlagRequired("head-sha")
	_ = cmd.MarkFlagRequired("repo")

	return cmd
}

func runGenerateChangelog(baseSHA, headSHA, repo string, excludePaths []string, includeContributors bool) error {
	commits, err := git.GetCommits(baseSHA, headSHA, excludePaths)
	if err != nil {
		return fmt.Errorf("getting commits: %w", err)
	}

	// Look up GitHub usernames for inline attribution if contributors flag is set
	if includeContributors && repo != "" {
		commits = LookupGitHubUsernames(commits, repo)
	}

	breaking, features, fixes, other := CategorizeCommits(commits)

	changelog := FormatMarkdown(breaking, features, fixes, other, repo, baseSHA, headSHA)

	result := ChangelogResult{
		Changelog:   changelog,
		HasBreaking: len(breaking) > 0,
		HasFeatures: len(features) > 0,
		HasFixes:    len(fixes) > 0,
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
