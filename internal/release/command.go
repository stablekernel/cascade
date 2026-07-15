package release

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/globals"
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
	var tagOnly bool
	var configPath string
	var manifestKey string
	var component string

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
			if err := validateManageReleaseFlags(act, repo, environment, sha, tag); err != nil {
				return err
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

			// Scope RC-tag reaping to a declared component's tag namespace when
			// --component is set, so a component's publish never reaps a sibling
			// component's RC tags and a custom pre-release token is reaped instead
			// of accumulating. Without --component the reaper keeps its historical
			// permissive single-component behavior.
			opts, err := componentReapOptions(configPath, manifestKey, component)
			if err != nil {
				return err
			}

			// Every manage-release action except a pure lookup mutates GitHub
			// state (releases and tags), so --dry-run stops here with a preview
			// of the resolved plan and never constructs an API request.
			if globals.DryRun() {
				printDryRunPlan(act, repo, environment, sha, tag, newTag, deleteTag, createTag, tagOnly)
				return nil
			}

			manager := NewManager(repo, token, opts...)
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
				TagOnly:     tagOnly,
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
	cmd.Flags().BoolVar(&tagOnly, "tag-only", false, "Create the git tag only and skip creating a draft release (release workflow is the sole release creator)")
	cmd.Flags().StringVar(&configPath, "config", "", "Path to CI/CD config file (required with --component to resolve the component tag grammar)")
	cmd.Flags().StringVar(&manifestKey, "manifest-key", config.DefaultManifestKey, "Key in manifest file containing CI config")
	cmd.Flags().StringVar(&component, "component", "", "Declared component to scope RC-tag reaping to (multi-component manifests)")

	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("action")
	_ = cmd.MarkFlagRequired("environment")
	_ = cmd.MarkFlagRequired("tag")

	return cmd
}

// printDryRunPlan prints what a manage-release invocation would do without
// touching GitHub. It mirrors the resolved action and every tag operation the
// real run would perform, so an operator can preview the full mutation plan.
func printDryRunPlan(act Action, repo, environment, sha, tag, newTag, deleteTag string, createTag, tagOnly bool) {
	fmt.Printf("Dry run - would perform release action %q on %s (environment %s, tag %s)\n", act, repo, environment, tag)
	if createTag {
		fmt.Printf("Dry run - would create git tag %s at %s\n", tag, sha)
	}
	if tagOnly {
		fmt.Println("Dry run - tag-only mode: no release object would be created or mutated")
	}
	if newTag != "" {
		fmt.Printf("Dry run - would create new tag %s and update the release to it\n", newTag)
	}
	if deleteTag != "" {
		fmt.Printf("Dry run - would delete tag %s\n", deleteTag)
	}
	if act == ActionPublish {
		fmt.Printf("Dry run - would publish the release for %s and reap its RC tags\n", tag)
	}
	if act == ActionDelete {
		fmt.Printf("Dry run - would delete the draft release for %s\n", tag)
	}
}

// componentReapOptions resolves the Manager options that scope RC-tag reaping to
// a declared component. When component is empty the single-component path is
// used and no options are returned, so reaping behavior is unchanged. When a
// component is named the manifest is loaded and the component's strict tag
// grammar is threaded via WithTagGrammar so reaping is exact to that component's
// namespace. A named component requires --config so the grammar can be resolved.
func componentReapOptions(configPath, manifestKey, component string) ([]Option, error) {
	if component == "" {
		return nil, nil
	}
	if configPath == "" {
		return nil, fmt.Errorf("--config is required with --component to resolve the component tag grammar")
	}
	if manifestKey == "" {
		manifestKey = config.DefaultManifestKey
	}

	file, err := config.ParseManifestFile(configPath, manifestKey)
	if err != nil {
		return nil, fmt.Errorf("loading manifest for component %q: %w", component, err)
	}
	if file.Config == nil {
		return nil, fmt.Errorf("manifest %q has no %q config for component %q", configPath, manifestKey, component)
	}

	resolved, err := file.Config.ResolveComponent(component)
	if err != nil {
		return nil, fmt.Errorf("resolving component %q: %w", component, err)
	}

	return []Option{WithTagGrammar(resolved.TagGrammarSpec())}, nil
}

// tagCreatingActions are the actions that materialize a git tag pointing at a
// specific commit and therefore require --sha. The remaining actions (lock,
// update, delete) resolve an existing release by its tag and treat SHA only as
// an optional disambiguator, so SHA is not required for them.
var tagCreatingActions = map[Action]bool{
	ActionCreate:     true,
	ActionPrerelease: true,
	ActionPublish:    true,
}

// validateManageReleaseFlags checks the flags required by the manage-release
// command. repo, environment, and tag are required for every action. SHA is
// required only for the tag-creating actions (create, prerelease, publish),
// which tag a commit; the tag-addressed actions (lock, update, delete) resolve
// the release by tag and do not need it.
func validateManageReleaseFlags(act Action, repo, environment, sha, tag string) error {
	if repo == "" {
		return fmt.Errorf("--repo is required")
	}
	if environment == "" {
		return fmt.Errorf("--environment is required")
	}
	if tag == "" {
		return fmt.Errorf("--tag is required")
	}
	if sha == "" && tagCreatingActions[act] {
		return fmt.Errorf("--sha is required for action %q", act)
	}
	return nil
}
