package globals

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/log"
)

// ApplyFlags reads the global CLI flags (--dry-run, --json, --trace) off the
// invoked command's flag set and applies them to process-wide state.
//
// Cobra runs only the closest PersistentPreRun(E) in the command chain, so a
// subcommand tree that defines its own hook shadows the root's. Every
// PersistentPreRun(E) in the CLI must therefore call ApplyFlags first;
// otherwise the root's persistent flags are silently dropped for that tree
// and a --dry-run invocation mutates real state. Reading the values off the
// command's own flag set (persistent flags are inherited and merged at parse
// time) makes this correct regardless of which hook cobra invokes.
//
// A flag that is not defined on the command is skipped, so ApplyFlags is safe
// on command trees built without the root's persistent flags.
func ApplyFlags(cmd *cobra.Command) {
	if v, ok := boolFlag(cmd, "dry-run"); ok {
		SetDryRun(v)
	}

	jsonOut, jsonDefined := boolFlag(cmd, "json")
	if jsonDefined {
		SetJSON(jsonOut)
	}
	// Disable colors when outputting JSON or in non-interactive mode.
	if jsonOut || os.Getenv("NO_COLOR") != "" {
		log.SetColors(false)
	}

	if v, ok := boolFlag(cmd, "trace"); ok && v {
		log.EnableTrace()
	}
}

// boolFlag returns the value of a bool flag on cmd and whether the flag is
// defined there (including inherited persistent flags).
func boolFlag(cmd *cobra.Command, name string) (bool, bool) {
	v, err := cmd.Flags().GetBool(name)
	if err != nil {
		return false, false
	}
	return v, true
}
