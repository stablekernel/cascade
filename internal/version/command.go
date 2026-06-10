package version

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/stablekernel/cascade/internal/changelog"
	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
)

// NewCommand creates the next-version command
func NewCommand() *cobra.Command {
	var configPath string
	var environment string
	var baseSHA string
	var headSHA string
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "next-version",
		Short: "Calculate the next semantic version",
		Long: `Calculate the next semantic version based on conventional commits.

The version is calculated by:
1. Looking at the next environment's current version as the base
2. Analyzing commits between the base SHA and HEAD
3. Determining bump type (breaking=major, feat=minor, fix=patch)
4. Incrementing RC if same base version, otherwise starting at RC 0

Examples:
  # Calculate next version for dev environment
  cascade next-version --environment dev

  # Calculate with specific SHA range
  cascade next-version --environment dev --base-sha abc123 --head-sha def456`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Auto-detect config file if not specified
			if configPath == "" {
				configPath = config.FindConfigFile("")
			}

			// Load config
			cfg, err := config.Parse(configPath)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			// Find environment index
			envIndex := -1
			for i, env := range cfg.Environments {
				if env == environment {
					envIndex = i
					break
				}
			}
			if envIndex == -1 {
				return fmt.Errorf("environment %q not found in config", environment)
			}

			// Get current dev version and next env version from state
			var currentDevVersion, nextEnvVersion, nextEnvSHA string

			// Load state from manifest
			cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
			if err == nil && cicdFile.State != nil {
				// Current environment's version
				if state, ok := cicdFile.State[environment]; ok {
					currentDevVersion = state.Version
				}

				// Next environment's version (for comparison)
				if envIndex+1 < len(cfg.Environments) {
					nextEnv := cfg.Environments[envIndex+1]
					if state, ok := cicdFile.State[nextEnv]; ok {
						nextEnvVersion = state.Version
						nextEnvSHA = state.SHA
					}
				}
			}

			// Default base SHA to next env's SHA if not specified
			if baseSHA == "" {
				baseSHA = nextEnvSHA
			}

			// Default head SHA to current HEAD
			if headSHA == "" {
				headSHA = "HEAD"
			}

			// Get commits between base and head
			var commits []changelog.ConventionalCommit
			if baseSHA == "" {
				// No base SHA - get all commits from initial commit
				baseSHA, _ = git.GetInitialCommit()
			}
			if baseSHA != "" {
				gitCommits, err := git.GetCommits(baseSHA, headSHA, nil)
				if err != nil {
					return fmt.Errorf("getting commits: %w", err)
				}
				// Parse each commit
				for _, gc := range gitCommits {
					if cc := changelog.ParseCommit(gc); cc != nil {
						commits = append(commits, *cc)
					}
				}
			}

			// Calculate next version
			calc := NewCalculator(cfg.GetTagPrefix())
			nextVersion, err := calc.CalculateNext(currentDevVersion, nextEnvVersion, commits)
			if err != nil {
				return fmt.Errorf("calculating version: %w", err)
			}

			if outputJSON {
				output := map[string]interface{}{
					"version":           nextVersion.String(),
					"base_version":      nextVersion.Base(),
					"current_dev":       currentDevVersion,
					"next_env":          nextEnvVersion,
					"bump_type":         bumpTypeString(DetermineBumpType(commits)),
					"commit_count":      len(commits),
					"release_candidate": nextVersion.PreRelease,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(output)
			}

			fmt.Println(nextVersion.String())
			return nil
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to manifest file (default: .github/manifest.yaml)")
	cmd.Flags().StringVarP(&environment, "environment", "e", "", "Target environment")
	cmd.Flags().StringVar(&baseSHA, "base-sha", "", "Base SHA for commit analysis (defaults to next env's SHA)")
	cmd.Flags().StringVar(&headSHA, "head-sha", "", "Head SHA for commit analysis (defaults to HEAD)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")

	_ = cmd.MarkFlagRequired("environment")

	return cmd
}

func bumpTypeString(b BumpType) string {
	switch b {
	case BumpMajor:
		return "major"
	case BumpMinor:
		return "minor"
	case BumpPatch:
		return "patch"
	default:
		return "none"
	}
}
