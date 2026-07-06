package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateReconcile_RejectsUnknownSource guards the adapter selector: an
// unrecognized source value must fail validation rather than reach the
// generator, since the emitted companion assumes a known adapter shape.
func TestValidateReconcile_RejectsUnknownSource(t *testing.T) {
	t.Parallel()
	cfg := &TrunkConfig{Reconcile: &ReconcileConfig{Enabled: true, Source: "renovate"}}
	errs := validateReconcile(cfg.Reconcile)
	require.NotEmpty(t, errs)
}

// TestValidateReconcile_RejectsUnknownCommit guards the commit-routing mode.
func TestValidateReconcile_RejectsUnknownCommit(t *testing.T) {
	t.Parallel()
	cfg := &TrunkConfig{Reconcile: &ReconcileConfig{Enabled: true, Commit: "rebase"}}
	errs := validateReconcile(cfg.Reconcile)
	require.NotEmpty(t, errs)
}

// TestValidateReconcile_AllowsEmptyAndKnownValues confirms the unset default
// and both known values for source and commit validate clean.
func TestValidateReconcile_AllowsEmptyAndKnownValues(t *testing.T) {
	t.Parallel()
	require.Empty(t, validateReconcile(nil))
	require.Empty(t, validateReconcile(&ReconcileConfig{}))
	require.Empty(t, validateReconcile(&ReconcileConfig{Enabled: true, Source: "dependabot", Commit: "append"}))
	require.Empty(t, validateReconcile(&ReconcileConfig{Enabled: true, Commit: "followup"}))
}
