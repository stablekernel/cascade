package config

import "testing"

// TestParseDriftCheck proves the opt-in drift_check lane parses both toggles.
func TestParseDriftCheck(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
drift_check:
  enabled: true
  comment: true
`)
	if cfg.DriftCheck == nil || !cfg.DriftCheck.Enabled || !cfg.DriftCheck.Comment {
		t.Fatalf("drift_check: %#v", cfg.DriftCheck)
	}
}

// TestDriftCheckValidatesAtCurrentSchemaVersion proves the drift_check toggle is
// additive: a manifest that sets it validates cleanly at CurrentSchemaVersion,
// confirming the schema version was not bumped to introduce the field.
func TestDriftCheckValidatesAtCurrentSchemaVersion(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: CurrentSchemaVersion,
		TrunkBranch:   "main",
		DriftCheck:    &DriftCheckConfig{Enabled: true, Comment: true},
	}
	for _, e := range Validate(cfg) {
		t.Fatalf("unexpected validation error for drift_check at current schema version: %s", e)
	}
}
