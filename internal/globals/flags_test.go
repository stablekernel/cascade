package globals

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/log"
)

func TestApplyFlags_SetsGlobalsFromCommandFlags(t *testing.T) {
	SetDryRun(false)
	SetJSON(false)
	t.Cleanup(func() {
		SetDryRun(false)
		SetJSON(false)
		log.SetLevel(log.LevelDebug)
		log.SetColors(true)
	})

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("trace", false, "")
	if err := cmd.Flags().Parse([]string{"--dry-run", "--json", "--trace"}); err != nil {
		t.Fatalf("parsing flags: %v", err)
	}

	ApplyFlags(cmd)

	if !DryRun() {
		t.Error("expected DryRun() to be true after ApplyFlags")
	}
	if !JSON() {
		t.Error("expected JSON() to be true after ApplyFlags")
	}
	if !log.TraceEnabled() {
		t.Error("expected trace logging enabled after ApplyFlags")
	}
}

func TestApplyFlags_SkipsUndefinedFlags(t *testing.T) {
	// A command without the global flags must not clobber existing state.
	SetDryRun(true)
	SetJSON(true)
	t.Cleanup(func() {
		SetDryRun(false)
		SetJSON(false)
		log.SetColors(true)
	})

	cmd := &cobra.Command{Use: "bare"}
	ApplyFlags(cmd)

	if !DryRun() {
		t.Error("ApplyFlags clobbered DryRun on a command without a dry-run flag")
	}
	if !JSON() {
		t.Error("ApplyFlags clobbered JSON on a command without a json flag")
	}
}
