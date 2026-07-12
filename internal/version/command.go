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
	var component string
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

			// Default head SHA to current HEAD.
			if headSHA == "" {
				headSHA = "HEAD"
			}

			var currentDevVersion, nextEnvVersion string
			var commits []changelog.ConventionalCommit
			var calc *Calculator

			if component != "" && cfg.HasComponents() {
				// Component-scoped derivation: the version comes only from the
				// component's path-scoped commits and its strict tag namespace, so
				// a sibling component's tags and commits are invisible. This mirrors
				// orchestrate.calculateComponentVersion so the two paths agree.
				currentDevVersion, nextEnvVersion, commits, calc, err =
					componentVersionInputs(cfg, component, baseSHA, headSHA)
				if err != nil {
					return err
				}
			} else {
				// Single-component path, unchanged: version comes from recorded
				// per-environment state and the whole-repo commit range under the
				// manifest's resolved (permissive) grammar.
				currentDevVersion, nextEnvVersion, commits, calc, err =
					singleComponentVersionInputs(cfg, configPath, environment, baseSHA, headSHA)
				if err != nil {
					return err
				}
			}

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
	cmd.Flags().StringVar(&component, "component", "", "Declared component to scope the version to (multi-component manifests)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")

	_ = cmd.MarkFlagRequired("environment")

	return cmd
}

// singleComponentVersionInputs derives the version inputs for the single-component
// (or no-component) path: recorded per-environment state supplies the current and
// next-env versions, and the whole-repo commit range under the manifest's resolved
// grammar supplies the bump. This is the historical next-version behavior, factored
// out unchanged.
func singleComponentVersionInputs(cfg *config.TrunkConfig, configPath, environment, baseSHA, headSHA string) (currentDevVersion, nextEnvVersion string, commits []changelog.ConventionalCommit, calc *Calculator, err error) {
	envIndex := -1
	envNames := cfg.EnvironmentNames()
	for i, env := range envNames {
		if env == environment {
			envIndex = i
			break
		}
	}
	if envIndex == -1 {
		return "", "", nil, nil, fmt.Errorf("environment %q not found in config", environment)
	}

	var nextEnvSHA string
	cicdFile, perr := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	if perr == nil && cicdFile.State != nil {
		if state, ok := cicdFile.State[environment]; ok {
			currentDevVersion = state.Version
		}
		if envIndex+1 < len(envNames) {
			nextEnv := envNames[envIndex+1]
			if state, ok := cicdFile.State[nextEnv]; ok {
				nextEnvVersion = state.Version
				nextEnvSHA = state.SHA
			}
		}
	}

	if baseSHA == "" {
		baseSHA = nextEnvSHA
	}
	if baseSHA == "" {
		baseSHA, _ = git.GetInitialCommit()
	}
	if baseSHA != "" {
		gitCommits, cerr := git.GetCommits(baseSHA, headSHA, nil)
		if cerr != nil {
			return "", "", nil, nil, fmt.Errorf("getting commits: %w", cerr)
		}
		for _, gc := range gitCommits {
			if cc := changelog.ParseCommit(gc); cc != nil {
				commits = append(commits, *cc)
			}
		}
	}

	// Calculate next version under the manifest's resolved tag grammar so a custom
	// prefix, pre-release token, or separator is honored. With no tag_grammar block
	// this resolves to the historical default.
	return currentDevVersion, nextEnvVersion, commits, NewCalculatorWithGrammar(cfg.ResolveTagGrammar()), nil
}

// componentVersionInputs derives the version inputs for a named component: its
// current version and release base come from its own strict tag namespace, and the
// commit range is scoped to the component's path. It mirrors
// orchestrate.calculateComponentVersion so the CLI and production paths agree on a
// component's next version. baseSHA, when set, overrides the tag-derived base.
func componentVersionInputs(cfg *config.TrunkConfig, component, baseSHA, headSHA string) (currentDevVersion, nextEnvVersion string, commits []changelog.ConventionalCommit, calc *Calculator, err error) {
	resolved, rerr := cfg.ResolveComponent(component)
	if rerr != nil {
		return "", "", nil, nil, fmt.Errorf("resolving component %q: %w", component, rerr)
	}
	spec := resolved.TagGrammarSpec()

	if latestTag, _, terr := git.GetLatestTagSpec("", spec); terr == nil && latestTag != "" {
		currentDevVersion = latestTag
	}
	var baseTagSHA string
	if latestRelease, releaseSHA, lerr := git.GetLatestReleaseTagSpec("", spec); lerr == nil && latestRelease != "" {
		nextEnvVersion = latestRelease
		baseTagSHA = releaseSHA
	}

	if baseSHA == "" {
		baseSHA = baseTagSHA
	}
	if baseSHA == "" {
		baseSHA, _ = git.GetInitialCommit()
	}
	if baseSHA != "" {
		gitCommits, cerr := git.GetCommitsForPaths(baseSHA, headSHA, []string{resolved.Path})
		if cerr != nil {
			return "", "", nil, nil, fmt.Errorf("getting commits for component %q: %w", component, cerr)
		}
		for _, gc := range gitCommits {
			if cc := changelog.ParseCommit(gc); cc != nil {
				commits = append(commits, *cc)
			}
		}
	}

	return currentDevVersion, nextEnvVersion, commits, NewCalculatorWithGrammar(spec), nil
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
