package globals

import (
	"strings"
	"testing"
)

// TestGuardMutation_RefusesUnderDryRun proves the backstop fails loudly: when
// --dry-run is set and a mutation boundary is reached anyway (a command forgot
// its explicit gate), GuardMutation returns an error naming the operation
// instead of silently allowing or silently skipping the mutation.
func TestGuardMutation_RefusesUnderDryRun(t *testing.T) {
	SetDryRun(true)
	t.Cleanup(func() { SetDryRun(false) })

	err := GuardMutation("DELETE release v1.2.3")
	if err == nil {
		t.Fatal("GuardMutation must refuse a mutation under dry-run")
	}
	if !strings.Contains(err.Error(), "DELETE release v1.2.3") {
		t.Errorf("guard error must name the refused operation, got: %v", err)
	}
	if !strings.Contains(err.Error(), "dry-run") {
		t.Errorf("guard error must name dry-run as the cause, got: %v", err)
	}
}

// TestGuardMutation_AllowsWhenNotDryRun proves the backstop is a no-op on
// real runs so every mutation path behaves exactly as before.
func TestGuardMutation_AllowsWhenNotDryRun(t *testing.T) {
	SetDryRun(false)
	if err := GuardMutation("PUT branch protection"); err != nil {
		t.Fatalf("GuardMutation must allow mutations when dry-run is off, got: %v", err)
	}
}
