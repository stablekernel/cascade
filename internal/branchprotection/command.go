package branchprotection

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
)

// Options configures a branch-protection emit run.
type Options struct {
	// ConfigPath is the manifest path; empty means auto-detect.
	ConfigPath string
	// ManifestKey is the key in the manifest file holding the CI config.
	ManifestKey string
	// Branch labels the protection target. It affects only the operator_todo
	// note; the PUT body itself is branch-agnostic. The required contexts (Setup
	// and Finalize) are the orchestrate-workflow steps jobs, identical across
	// branches and environments, so the branch never changes them.
	Branch string
	// Output is the destination path. Empty or "-" writes to stdout.
	Output string
}

// NewCommand creates the branch-protection command. It emits the JSON body an
// operator applies to GitHub's branch-protection API for a cascade-managed
// trunk. cascade emits the file; the operator applies it. cascade never calls
// the GitHub API.
func NewCommand() *cobra.Command {
	var o Options

	cmd := &cobra.Command{
		Use:   "branch-protection",
		Short: "Emit the branch-protection JSON body for an operator to apply",
		Long: `Emit the JSON body an operator applies to GitHub's branch-protection API for
a cascade-managed trunk. cascade emits the file; the operator applies it. cascade
never calls the GitHub API.

The output is a wrapper with two top-level keys:

  protection     the EXACT body to PUT to the branches protection API
  operator_todo  companion guidance that is NOT part of the PUT body

Apply it by sending only the .protection object, for example:

  cascade branch-protection | jq .protection | \
    gh api -X PUT repos/OWNER/REPO/branches/main/protection --input -

The required status checks contain only the cascade-controlled Setup and Finalize
jobs, which run on every pipeline run, so .protection applied as-is never blocks a
pull request. The reusable-workflow caller jobs (validate, build, deploy) are not
required directly because cascade knows their display-name prefix but not the
inner job name GitHub appends to form the real check-run context. Those prefixes
are listed under operator_todo.complete_these_contexts as "<DisplayName> /
<inner-job>" placeholders for you to complete.

The --branch flag only labels the guidance note; the required contexts are the
same across branches and environments because they are the orchestrate-workflow
steps jobs.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return Run(o, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVarP(&o.ConfigPath, "config", "c", "", "Path to config file (default: auto-detect .github/manifest.yaml)")
	cmd.Flags().StringVar(&o.ManifestKey, "manifest-key", config.DefaultManifestKey, "Key in manifest file containing CI config")
	cmd.Flags().StringVar(&o.Branch, "branch", "main", "Branch the protection targets (labels the guidance note only; does not change the required contexts)")
	cmd.Flags().StringVarP(&o.Output, "output", "o", "", "Write to this path instead of stdout ('-' also means stdout)")

	return cmd
}

// Run resolves the manifest, builds the payload, and writes it. When Options.Output
// is empty or "-", it writes to stdout (w); otherwise it writes to that file path.
func Run(o Options, stdout io.Writer) error {
	configPath := o.ConfigPath
	if configPath == "" {
		configPath = config.FindConfigFile("")
	}

	manifestKey := o.ManifestKey
	if manifestKey == "" {
		manifestKey = config.DefaultManifestKey
	}

	cfg, err := config.ParseWithKey(configPath, manifestKey)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	if errs := config.Validate(cfg); len(errs) > 0 {
		return fmt.Errorf("config validation failed: %s", errs[0])
	}

	branch := o.Branch
	if branch == "" {
		branch = "main"
	}

	out, err := Marshal(Build(cfg, branch))
	if err != nil {
		return err
	}

	if o.Output == "" || o.Output == "-" {
		if _, werr := stdout.Write(out); werr != nil {
			return fmt.Errorf("writing branch-protection payload: %w", werr)
		}
		return nil
	}

	if werr := os.WriteFile(o.Output, out, 0o644); werr != nil {
		return fmt.Errorf("writing branch-protection payload to %s: %w", o.Output, werr)
	}
	return nil
}
