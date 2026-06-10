package reset

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/log"
)

// NewCommand creates the reset command.
func NewCommand() *cobra.Command {
	var (
		flagResetState  bool
		flagDryRun      bool
		flagPush        bool
		flagRepoPath    string
		flagConfigPath  string
		flagManifestKey string
	)

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset a test repository by wiping releases, tags, and optionally state",
		Long: `Reset a test repository by wiping all GitHub releases and git tags.

Optionally reset the state section in the manifest for a clean slate.

This is intended for development and testing purposes only.

Examples:
  # Wipe all releases and tags (dry run)
  cascade reset --dry-run

  # Wipe all releases and tags
  cascade reset

  # Wipe releases, tags, AND reset state in manifest
  cascade reset --state

  # Wipe everything and push state changes
  cascade reset --state --push

  # Reset a specific repository
  cascade reset --repo /path/to/repo --state --push
`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Auto-detect config file if not specified
			if flagConfigPath == "" {
				repoPath := flagRepoPath
				if repoPath == "" {
					repoPath = "."
				}
				flagConfigPath = config.FindConfigFile(repoPath)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReset(Options{
				RepoPath:    flagRepoPath,
				ResetState:  flagResetState,
				DryRun:      flagDryRun,
				Push:        flagPush,
				ConfigPath:  flagConfigPath,
				ManifestKey: flagManifestKey,
			})
		},
	}

	cmd.Flags().BoolVar(&flagResetState, "state", false, "Reset state section in manifest")
	cmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "Preview changes without executing")
	cmd.Flags().BoolVar(&flagPush, "push", false, "Push state changes to remote (requires --state)")
	cmd.Flags().StringVar(&flagRepoPath, "repo", "", "Path to repository (defaults to current directory)")
	cmd.Flags().StringVar(&flagConfigPath, "config", "", "Path to manifest file (auto-detects .github/manifest.yaml)")
	cmd.Flags().StringVar(&flagManifestKey, "manifest-key", config.DefaultManifestKey, "Key in manifest file containing CI config")

	return cmd
}

func runReset(opts Options) error {
	if opts.DryRun {
		log.Info("[DRY-RUN] Preview mode - no changes will be made")
	}

	resetter, err := New(opts)
	if err != nil {
		return fmt.Errorf("failed to initialize resetter: %w", err)
	}

	result, err := resetter.Reset()
	if err != nil {
		return err
	}

	// Summary
	log.Section("Summary")
	log.Info("Releases deleted: %d", result.ReleasesDeleted)
	log.Info("Tags deleted: %d", result.TagsDeleted)
	if opts.ResetState {
		log.Info("State reset: %v", result.StateReset)
		if opts.Push {
			log.Info("Changes pushed: %v", result.Pushed)
		}
	}

	return nil
}
