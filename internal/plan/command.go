package plan

import (
	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
)

// NewCommand creates the plan command, a read-only preview that renders, as a
// per-file unified diff, what generate-workflow would change in the committed
// workflow and action files.
func NewCommand() *cobra.Command {
	var o Options

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview the workflow diff the manifest would generate",
		Long: `Preview, as a per-file unified diff, what generate-workflow would change in the
committed GitHub Actions workflow and action files, without writing anything.

plan is the human-facing preview counterpart to verify. It prints the diff
between the committed bytes and what the manifest would currently generate: a
new file appears as a whole-file add, a changed file as a unified hunk, and a
file already in sync produces no diff.

plan always exits 0 on success whether or not a diff exists, because it is
informational rather than a gate. It exits non-zero only for an operational
failure, such as a missing or invalid manifest. This is the key difference from
verify: plan is the human preview you read, while verify is the pass/fail gate
you wire into CI.

plan is read-only: it never writes files, runs git, or modifies the repository.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return Run(o, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}

	cmd.Flags().StringVarP(&o.ConfigPath, "config", "c", "", "Path to config file (default: auto-detect .github/manifest.yaml)")
	cmd.Flags().StringVar(&o.ManifestKey, "manifest-key", config.DefaultManifestKey, "Key in manifest file containing CI config")
	cmd.Flags().StringVar(&o.ActionFolder, "action-folder", "manage-release", "Folder name for the manage-release composite action")
	cmd.Flags().StringVarP(&o.OutputPath, "output", "o", ".github/workflows/orchestrate.yaml", "Path of the orchestrate workflow")
	cmd.Flags().StringVar(&o.PromoteOutputPath, "promote-output", ".github/workflows/promote.yaml", "Path of the promote workflow")

	return cmd
}
