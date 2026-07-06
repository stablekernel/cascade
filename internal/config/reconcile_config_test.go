package config

import "testing"

// TestParseReconcile proves the opt-in reconcile companion lane parses its
// fields: enabled, source (the adapter selector), and commit (the routing
// mode).
func TestParseReconcile(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
reconcile:
  enabled: true
  source: dependabot
  commit: followup
`)
	if cfg.Reconcile == nil || !cfg.Reconcile.Enabled {
		t.Fatalf("reconcile: %#v", cfg.Reconcile)
	}
	if cfg.Reconcile.Source != "dependabot" {
		t.Fatalf("reconcile.source: got %q", cfg.Reconcile.Source)
	}
	if cfg.Reconcile.Commit != "followup" {
		t.Fatalf("reconcile.commit: got %q", cfg.Reconcile.Commit)
	}
}

// TestReconcileValidatesAtCurrentSchemaVersion proves the reconcile toggle is
// additive: a manifest that sets it validates cleanly at CurrentSchemaVersion,
// confirming the schema version was not bumped to introduce the field.
func TestReconcileValidatesAtCurrentSchemaVersion(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: CurrentSchemaVersion,
		TrunkBranch:   "main",
		Reconcile:     &ReconcileConfig{Enabled: true, Source: "dependabot", Commit: "append"},
	}
	for _, e := range Validate(cfg) {
		t.Fatalf("unexpected validation error for reconcile at current schema version: %s", e)
	}
}
