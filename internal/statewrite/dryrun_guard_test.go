package statewrite

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/globals"
)

// TestPutContent_DryRunGuardRefusesWrite simulates a caller that reaches the
// contents-API state PUT without an explicit --dry-run gate. The backstop must
// refuse loudly before the gh CLI is even invoked; PATH is cleared so any
// attempt to exec gh fails with a distinctly different error.
func TestPutContent_DryRunGuardRefusesWrite(t *testing.T) {
	globals.SetDryRun(true)
	t.Cleanup(func() { globals.SetDryRun(false) })
	t.Setenv("PATH", "")

	err := NewGHClient().PutContent(
		"owner/repo", ".github/manifest.yaml", "main", "abc123",
		"chore: update state", []byte("ci: {}"), Identity{},
	)
	if err == nil {
		t.Fatal("an ungated state PUT under dry-run must be refused by the guard, got nil error")
	}
	if !strings.Contains(err.Error(), "dry-run guard") {
		t.Errorf("expected the loud dry-run guard error, got: %v", err)
	}
}
