package main

import (
	"io"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/globals"
	"github.com/stablekernel/cascade/internal/log"
)

// resetGlobalFlagState restores process-wide flag state so subtests do not
// leak into each other.
func resetGlobalFlagState() {
	globals.SetDryRun(false)
	globals.SetJSON(false)
	log.SetLevel(log.LevelDebug)
	log.SetColors(true)
}

// probeTree injects a probe subcommand under the named command tree, executes
// it with the given global flags, and reports the process-wide flag state
// observed at RunE time (after every PreRun hook in the chain has fired).
func probeTree(t *testing.T, tree string, flags ...string) (dryRun, json, trace bool) {
	t.Helper()

	root := newRootCommand()
	parent, _, err := root.Find([]string{tree})
	if err != nil {
		t.Fatalf("finding command tree %q: %v", tree, err)
	}

	probe := &cobra.Command{
		Use: "probe",
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun = globals.DryRun()
			json = globals.JSON()
			trace = log.TraceEnabled()
			return nil
		},
	}
	parent.AddCommand(probe)

	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{tree, "probe"}, flags...))
	if err := root.Execute(); err != nil {
		t.Fatalf("executing %s probe: %v", tree, err)
	}
	return dryRun, json, trace
}

// TestGlobalFlags_AppliedAcrossCommandTrees proves that the root --dry-run,
// --json, and --trace flags take effect on every command tree, including the
// trees whose own PersistentPreRunE shadows the root hook (cobra runs only
// the closest PersistentPreRun in the chain). Silently dropping --dry-run on
// these trees is a data-safety bug: a dry-run finalize or update would push
// real state.
func TestGlobalFlags_AppliedAcrossCommandTrees(t *testing.T) {
	trees := []string{
		// Trees that define their own PersistentPreRunE and shadowed the
		// root hook before the fix.
		"orchestrate",
		"external",
		"promote",
		"simulate",
		"reset",
		// Control tree without its own hook: the root hook applies here.
		"generate-workflow",
	}

	for _, tree := range trees {
		t.Run(tree, func(t *testing.T) {
			resetGlobalFlagState()
			t.Cleanup(resetGlobalFlagState)

			dryRun, json, trace := probeTree(t, tree, "--dry-run", "--json", "--trace")

			if !dryRun {
				t.Errorf("%s: --dry-run was ignored (globals.DryRun() = false)", tree)
			}
			if !json {
				t.Errorf("%s: --json was ignored (globals.JSON() = false)", tree)
			}
			if !trace {
				t.Errorf("%s: --trace was ignored (trace logging not enabled)", tree)
			}
		})
	}
}

// TestGlobalFlags_DefaultOff guards against sticky state: without the flags,
// every global stays off.
func TestGlobalFlags_DefaultOff(t *testing.T) {
	resetGlobalFlagState()
	t.Cleanup(resetGlobalFlagState)

	dryRun, json, trace := probeTree(t, "orchestrate")

	if dryRun {
		t.Error("globals.DryRun() = true without --dry-run")
	}
	if json {
		t.Error("globals.JSON() = true without --json")
	}
	if trace {
		t.Error("trace logging enabled without --trace")
	}
}
