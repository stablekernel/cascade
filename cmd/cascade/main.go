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
	"github.com/stablekernel/cascade/internal/hotfix"
	initcmd "github.com/stablekernel/cascade/internal/initcmd"
	"github.com/stablekernel/cascade/internal/log"
	"github.com/stablekernel/cascade/internal/orchestrate"
	"github.com/stablekernel/cascade/internal/plan"
	"github.com/stablekernel/cascade/internal/promote"
	"github.com/stablekernel/cascade/internal/release"
	"github.com/stablekernel/cascade/internal/reset"
	"github.com/stablekernel/cascade/internal/rollback"
	"github.com/stablekernel/cascade/internal/schema"
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

// Global flags
var (
	flagDryRun bool
	flagTrace  bool
	flagJSON   bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "cascade",
		Short: "CI/CD orchestration tool for trunk-based development",
		Long: `cascade is a CLI tool that provides CI/CD orchestration capabilities
for trunk-based development workflows. It handles configuration parsing,
change detection, and changelog generation.`,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Configure logging based on flags
			if flagTrace {
				log.EnableTrace()
			}
			// Disable colors when outputting JSON or in non-interactive mode
			if flagJSON || os.Getenv("NO_COLOR") != "" {
				log.SetColors(false)
			}
			// Set global flags for access by subcommands
			globals.SetDryRun(flagDryRun)
			globals.SetJSON(flagJSON)
		},
	}

	// Add persistent (global) flags
	rootCmd.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "Preview mode - show what would happen without making changes")
	rootCmd.PersistentFlags().BoolVar(&flagTrace, "trace", false, "Enable TRACE level logging for detailed internals")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "Output structured JSON for workflow consumption")

	// Add subcommands
	rootCmd.AddCommand(branchprotection.NewCommand())
	rootCmd.AddCommand(config.NewCommand())
	rootCmd.AddCommand(changes.NewCommand())
	rootCmd.AddCommand(changelog.NewCommand())
	rootCmd.AddCommand(environments.NewCommand())
	rootCmd.AddCommand(external.NewCommand())
	rootCmd.AddCommand(generate.NewCommand())
	rootCmd.AddCommand(verify.NewCommand())
	rootCmd.AddCommand(plan.NewCommand())
	rootCmd.AddCommand(hotfix.NewCommand())
	rootCmd.AddCommand(initcmd.NewCommand())
	rootCmd.AddCommand(orchestrate.NewCommand())
	rootCmd.AddCommand(promote.NewCommand())
	rootCmd.AddCommand(release.NewCommand())
	rootCmd.AddCommand(reset.NewCommand())
	rootCmd.AddCommand(rollback.NewCommand())
	rootCmd.AddCommand(schema.NewCommand())
	rootCmd.AddCommand(status.NewCommand())
	rootCmd.AddCommand(versionpkg.NewCommand())
	rootCmd.AddCommand(newVersionCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCodeFor(err))
	}
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
