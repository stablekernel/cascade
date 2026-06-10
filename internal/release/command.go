package release

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// NewCommand creates the manage-release command
func NewCommand() *cobra.Command {
	var repo string
	var action string
	var environment string
	var sha string
	var tag string
	var changelog string
	var changelogFile string
	var token string
	var previousTag string
	var newTag string
	var deleteTag string
	var createTag bool

	cmd := &cobra.Command{
		Use:   "manage-release",
		Short: "Manage GitHub draft releases",
		Long: `Manage GitHub draft releases with lifecycle operations.

Actions:
  create     - Create a new draft release with status line in body
  update     - Update existing draft release with status line
  lock       - Mark release as pre-release (for test environments)
  prerelease - Convert draft to pre-release with optional semver tag
  publish    - Publish release and remove status line from body
  delete     - Delete a draft release (for orphan cleanup)

Tag Operations:
  --create-tag    Create git tag on create action (for initial release)
  --new-tag       Create new semver tag and update release (for prerelease)
  --delete-tag    Delete specified tag after publish (cleanup short-sha)
  --previous-tag  Set previous tag for changelog comparison

Status Line:
  Draft releases include "## Status: Deployed to <Env>" at the top of the body.
  This line is automatically added on create/update and removed on publish.

Outputs (to stdout):
  release_id   - GitHub release ID
  release_url  - API URL to the release
  html_url     - Browser URL to the release`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate action
			act, err := ValidateAction(action)
			if err != nil {
				return err
			}

			// Validate required fields
			if repo == "" {
				return fmt.Errorf("--repo is required")
			}
			if environment == "" {
				return fmt.Errorf("--environment is required")
			}
			if sha == "" {
				return fmt.Errorf("--sha is required")
			}
			if tag == "" {
				return fmt.Errorf("--tag is required")
			}

			// Get token from flag or environment
			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
				if token == "" {
					token = os.Getenv("GH_TOKEN")
				}
			}
			if token == "" {
				return fmt.Errorf("--token is required (or set GITHUB_TOKEN/GH_TOKEN)")
			}

			// Read changelog from file if specified (handles multiline content better)
			if changelogFile != "" {
				content, err := os.ReadFile(changelogFile)
				if err != nil {
					return fmt.Errorf("reading changelog file: %w", err)
				}
				changelog = strings.TrimSpace(string(content))
			}

			manager := NewManager(repo, token)
			result, err := manager.Manage(Options{
				Action:      act,
				Environment: environment,
				SHA:         sha,
				Tag:         tag,
				Changelog:   changelog,
				PreviousTag: previousTag,
				NewTag:      newTag,
				DeleteTag:   deleteTag,
				CreateTag:   createTag,
			})
			if err != nil {
				return err
			}

			// Output results (compatible with bash script output format)
			fmt.Println(result.ReleaseID)
			fmt.Println(result.ReleaseURL)
			fmt.Println(result.HTMLURL)

			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "Repository in owner/repo format")
	cmd.Flags().StringVar(&action, "action", "", "Action: create, update, lock, prerelease, publish, delete")
	cmd.Flags().StringVar(&environment, "environment", "", "Target environment")
	cmd.Flags().StringVar(&sha, "sha", "", "Release commit SHA")
	cmd.Flags().StringVar(&tag, "tag", "", "Tag name")
	cmd.Flags().StringVar(&changelog, "changelog", "", "Release notes markdown")
	cmd.Flags().StringVar(&changelogFile, "changelog-file", "", "Path to file containing release notes (overrides --changelog)")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (or use GITHUB_TOKEN env)")
	cmd.Flags().StringVar(&previousTag, "previous-tag", "", "Previous tag for changelog comparison")
	cmd.Flags().StringVar(&newTag, "new-tag", "", "New semver tag (for prerelease action)")
	cmd.Flags().StringVar(&deleteTag, "delete-tag", "", "Tag to delete after publish (cleanup)")
	cmd.Flags().BoolVar(&createTag, "create-tag", false, "Create git tag on create action")

	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("action")
	_ = cmd.MarkFlagRequired("environment")
	_ = cmd.MarkFlagRequired("sha")
	_ = cmd.MarkFlagRequired("tag")

	return cmd
}
