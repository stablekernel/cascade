// Command cascade compiles a declarative manifest (.github/manifest.yaml)
// into GitHub Actions workflows for trunk-based, multi-environment release
// pipelines, and provides the lifecycle subcommands the generated workflows
// invoke, such as orchestrate, promote, release, hotfix, and rollback.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/branchprotection"
	"github.com/stablekernel/cascade/internal/changelog"
	"github.com/stablekernel/cascade/internal/changes"
	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/environments"
	"github.com/stablekernel/cascade/internal/external"
	"github.com/stablekernel/cascade/internal/generate"
	"github.com/stablekernel/cascade/internal/globals"
	"github.com/stablekernel/cascade/internal/graph"
	"github.com/stablekernel/cascade/internal/hotfix"
	initcmd "github.com/stablekernel/cascade/internal/initcmd"
	"github.com/stablekernel/cascade/internal/orchestrate"
	"github.com/stablekernel/cascade/internal/pinreconcile"
	"github.com/stablekernel/cascade/internal/plan"
	"github.com/stablekernel/cascade/internal/promote"
	"github.com/stablekernel/cascade/internal/release"
	"github.com/stablekernel/cascade/internal/reset"
	"github.com/stablekernel/cascade/internal/rollback"
	"github.com/stablekernel/cascade/internal/schema"
	"github.com/stablekernel/cascade/internal/simulate"
	"github.com/stablekernel/cascade/internal/status"
	"github.com/stablekernel/cascade/internal/verify"
	versionpkg "github.com/stablekernel/cascade/internal/version"
)

// Version information - set at build time via ldflags
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	rootCmd := newRootCommand()

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCodeFor(err))
	}
}

// newRootCommand builds the root command with all global flags and
// subcommands wired. Extracted from main so tests can execute the real
// command tree in-process.
func newRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "cascade",
		Short: "Compile a release manifest into GitHub Actions workflows",
		Long: `cascade compiles a declarative manifest (.github/manifest.yaml) into
GitHub Actions workflows for trunk-based, multi-environment release
pipelines. It is a compiler, not a control plane: after generation,
everything runs as native GitHub Actions with no external service
watching your repository.

The manifest describes your environments, builds, deploys, and release
policy, and also records live deployment state. Subcommands cover the
full lifecycle: generating workflows, orchestrating builds and deploys,
promoting releases through environments, and the hotfix and rollback
off-ramps.`,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Apply the global flags (--dry-run, --json, --trace) to
			// process-wide state. Subcommand trees that define their own
			// PersistentPreRunE shadow this hook, so they call
			// globals.ApplyFlags themselves.
			globals.ApplyFlags(cmd)
		},
	}

	// Add persistent (global) flags
	rootCmd.PersistentFlags().Bool("dry-run", false, "Preview mode - show what would happen without making changes")
	rootCmd.PersistentFlags().Bool("trace", false, "Enable TRACE level logging for detailed internals")
	rootCmd.PersistentFlags().Bool("json", false, "Output structured JSON for workflow consumption")

	// Add subcommands
	rootCmd.AddCommand(branchprotection.NewCommand())
	rootCmd.AddCommand(config.NewCommand())
	rootCmd.AddCommand(changes.NewCommand())
	rootCmd.AddCommand(changelog.NewCommand())
	rootCmd.AddCommand(environments.NewCommand())
	rootCmd.AddCommand(external.NewCommand())
	rootCmd.AddCommand(generate.NewCommand())
	rootCmd.AddCommand(graph.NewCommand())
	rootCmd.AddCommand(verify.NewCommand())
	rootCmd.AddCommand(plan.NewCommand())
	rootCmd.AddCommand(hotfix.NewCommand())
	rootCmd.AddCommand(initcmd.NewCommand())
	rootCmd.AddCommand(orchestrate.NewCommand())
	rootCmd.AddCommand(pinreconcile.NewCommand())
	rootCmd.AddCommand(promote.NewCommand())
	rootCmd.AddCommand(release.NewCommand())
	rootCmd.AddCommand(reset.NewCommand())
	rootCmd.AddCommand(rollback.NewCommand())
	rootCmd.AddCommand(schema.NewCommand())
	rootCmd.AddCommand(simulate.NewCommand())
	rootCmd.AddCommand(status.NewCommand())
	rootCmd.AddCommand(versionpkg.NewCommand())
	rootCmd.AddCommand(newVersionCmd())

	return rootCmd
}

// exitCodeFor maps a command error to a process exit code. A command may opt
// into a specific code by returning an error that implements ExitCode() int
// (verify uses this to distinguish drift from an operational failure); every
// other error keeps the default exit code 1.
func exitCodeFor(err error) int {
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("cascade %s\n", version)
			fmt.Printf("  commit: %s\n", commit)
			fmt.Printf("  built:  %s\n", date)
		},
	}
}
