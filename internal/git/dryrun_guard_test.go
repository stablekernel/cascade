package git

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/globals"
)

// TestDeleteRemoteTag_DryRunGuardRefusesPush simulates a caller that reaches
// the tag-deleting push without an explicit --dry-run gate. The backstop must
// refuse loudly before git runs; the remote name is one that cannot exist so
// a pre-guard regression fails with a distinctly different git error.
func TestDeleteRemoteTag_DryRunGuardRefusesPush(t *testing.T) {
	globals.SetDryRun(true)
	t.Cleanup(func() { globals.SetDryRun(false) })

	err := DeleteRemoteTag("no-such-remote-for-guard-test", "v0.0.0-guard-test")
	if err == nil {
		t.Fatal("an ungated delete-push under dry-run must be refused by the guard, got nil error")
	}
	if !strings.Contains(err.Error(), "dry-run guard") {
		t.Errorf("expected the loud dry-run guard error, got: %v", err)
	}
}
