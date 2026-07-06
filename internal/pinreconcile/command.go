package pinreconcile

import (
	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
)

// defaultCheckArtifactPath is where --check writes its data-only relevance
// artifact when --check-output is not set.
const defaultCheckArtifactPath = "pin-reconcile-result.json"

// NewCommand creates the "reconcile" command: adopting an external governed
// action-pin change (for example a Dependabot bump landing in a generated
// workflow) back into the manifest's action_pins, then regenerating so every
// workflow the manifest produces agrees with it again. reconcile never
// pushes, commits, or merges; that stays a caller's job (a CI companion or a
// human).
func NewCommand() *cobra.Command {
	var configPath string
	var manifestKey string
	var actionPinsPath string
	var checkOutput string
	var root string
	var check bool
	var ownRepo bool
	var changedFiles []string

	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Adopt a governed action-pin change back into the manifest",
		Long: `reconcile adopts an external action-pin change (for example a Dependabot
bump landing in a generated workflow) back into the manifest's action_pins and
regenerates every workflow the manifest produces, so cascade's owned output
agrees with it again. It reads the changed source files passed via
--changed-file and the manifest; it never reads a pin back out of a generated
file, and it never pushes, commits, or merges.

Three modes:
  (default)   Reconcile a user repo's manifest.
  --check     Read-only detector: reports relevance and writes a data-only
              JSON artifact, writing nothing else.
  --own-repo  Reconcile cascade's own action_pins.yaml manifest.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			key := manifestKey
			if key == "" {
				key = config.DefaultManifestKey
			}

			if check {
				return runCheck(root, checkOutput, changedFiles)
			}
			if ownRepo {
				_, err := RunOwnRepo(OwnRepoOptions{
					Root:           root,
					ManifestPath:   configPath,
					ManifestKey:    key,
					ActionPinsPath: actionPinsPath,
					ChangedFiles:   changedFiles,
				})
				return err
			}

			_, err := Run(Options{
				Root:         root,
				ManifestPath: configPath,
				ManifestKey:  key,
				ChangedFiles: changedFiles,
			})
			return err
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to the manifest file (default: <root>/.github/manifest.yaml)")
	cmd.Flags().StringVar(&manifestKey, "manifest-key", config.DefaultManifestKey, "Key in the manifest file containing the CI config")
	cmd.Flags().StringVar(&root, "root", ".", "Repository root the reconcile scans and writes relative to")
	cmd.Flags().BoolVar(&check, "check", false, "Read-only detector mode: report relevance and write a JSON artifact")
	cmd.Flags().BoolVar(&ownRepo, "own-repo", false, "Reconcile cascade's own action_pins.yaml manifest")
	cmd.Flags().StringVar(&actionPinsPath, "action-pins", "", "Path to action_pins.yaml (own-repo mode)")
	cmd.Flags().StringVar(&checkOutput, "check-output", defaultCheckArtifactPath, "Path to write the check-mode JSON artifact")
	cmd.Flags().StringSliceVar(&changedFiles, "changed-file", nil, "A changed source file to scan for a governed pin bump (repeatable)")

	cmd.MarkFlagsMutuallyExclusive("check", "own-repo")

	return cmd
}

// runCheck computes relevance for the changed files against the governed set
// and writes the data-only artifact companions read.
func runCheck(root, output string, changedFiles []string) error {
	sourceRefs, err := scanChangedFiles(root, changedFiles)
	if err != nil {
		return err
	}
	governed, err := governedActions()
	if err != nil {
		return err
	}
	res, err := Check(Input{Governed: governed, SourceRefs: sourceRefs})
	if err != nil {
		return err
	}
	return WriteCheckArtifact(output, res)
}
