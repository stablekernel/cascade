package environments

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
)

// Options configures an environments emit run.
type Options struct {
	// ConfigPath is the manifest path; empty means auto-detect.
	ConfigPath string
	// ManifestKey is the key in the manifest file holding the CI config.
	ManifestKey string
	// Output is the destination path. Empty or "-" writes to stdout.
	Output string
}

// NewCommand creates the environments command. It emits a per-environment
// configuration file an operator applies to GitHub's Environments REST API.
// cascade emits the file; the operator applies it. cascade never calls the
// GitHub API.
func NewCommand() *cobra.Command {
	var o Options

	cmd := &cobra.Command{
		Use:   "environments",
		Short: "Emit the per-environment config file for an operator to apply",
		Long: `Emit a per-environment configuration file an operator applies to GitHub's
Environments REST API. cascade emits the file; the operator applies it. cascade
never calls the GitHub API.

The output is a wrapper with one entry per manifest environment, in the
manifest's environments order:

  environments[].name            the cascade environment name
  environments[].gha_environment the GitHub Environment to configure
  environments[].environment     the body to PUT to the environments API
  environments[].operator_todo   companion guidance that is NOT part of the body

Apply each entry by sending only its .environment object, for example:

  cascade environments | jq -c '.environments[] | {gha_environment, environment}' | \
    while read -r row; do
      env=$(jq -r .gha_environment <<<"$row")
      jq .environment <<<"$row" | \
        gh api -X PUT "repos/OWNER/REPO/environments/$env" --input -
    done

The .environment body carries only the fields cascade can fully form from the
manifest (wait_timer and deployment_branch_policy). Required reviewers are listed
under operator_todo.required_reviewers as slugs because the REST API needs a
numeric reviewer id the manifest does not carry; resolve each slug to an id and
add it to the body's reviewers array. The expected env-scoped secret and variable
NAMES are listed under operator_todo.secrets and operator_todo.variables; cascade
emits names only, never values. Create them through the environment-secrets and
environment-variables APIs and set their values yourself.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return Run(o, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVarP(&o.ConfigPath, "config", "c", "", "Path to config file (default: auto-detect .github/manifest.yaml)")
	cmd.Flags().StringVar(&o.ManifestKey, "manifest-key", config.DefaultManifestKey, "Key in manifest file containing CI config")
	cmd.Flags().StringVarP(&o.Output, "output", "o", "", "Write to this path instead of stdout ('-' also means stdout)")

	return cmd
}

// Run resolves the manifest, builds the payload, and writes it. When
// Options.Output is empty or "-", it writes to stdout (w); otherwise it writes to
// that file path.
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

	out, err := Marshal(Build(cfg))
	if err != nil {
		return err
	}

	if o.Output == "" || o.Output == "-" {
		if _, werr := stdout.Write(out); werr != nil {
			return fmt.Errorf("writing environments payload: %w", werr)
		}
		return nil
	}

	if werr := os.WriteFile(o.Output, out, 0o644); werr != nil {
		return fmt.Errorf("writing environments payload to %s: %w", o.Output, werr)
	}
	return nil
}
