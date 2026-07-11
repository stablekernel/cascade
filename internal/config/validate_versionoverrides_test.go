package config

import "testing"

// TestValidateVersionOverridesReservedField exercises the reserved
// release.version_overrides.dir pointer. Validation applies only when the block
// is present, mirrors the components.path rules (no absolute path, no ".."
// segments), and never rejects a manifest that is valid without the block.
func TestValidateVersionOverridesReservedField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		release     *ReleaseConfig
		wantErr     bool
		errContains string
	}{
		{
			name:    "nil release is valid",
			release: nil,
			wantErr: false,
		},
		{
			name:    "release without version_overrides is valid",
			release: &ReleaseConfig{},
			wantErr: false,
		},
		{
			name:    "version_overrides with empty dir is valid",
			release: &ReleaseConfig{VersionOverrides: &VersionOverridesConfig{}},
			wantErr: false,
		},
		{
			name:    "version_overrides with a clean relative dir is valid",
			release: &ReleaseConfig{VersionOverrides: &VersionOverridesConfig{Dir: ".cascade/overrides"}},
			wantErr: false,
		},
		{
			name:        "absolute dir is rejected",
			release:     &ReleaseConfig{VersionOverrides: &VersionOverridesConfig{Dir: "/etc/overrides"}},
			wantErr:     true,
			errContains: "release.version_overrides.dir must be a relative path, not absolute",
		},
		{
			name:        "dir with parent segment is rejected",
			release:     &ReleaseConfig{VersionOverrides: &VersionOverridesConfig{Dir: "../escape"}},
			wantErr:     true,
			errContains: "release.version_overrides.dir must not contain '..' segments",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			errs := validateVersionOverrides(tt.release)
			if tt.wantErr {
				if len(errs) == 0 {
					t.Fatalf("expected an error, got none")
				}
				if !hasErrContaining(errs, tt.errContains) {
					t.Fatalf("expected error containing %q, got %v", tt.errContains, errs)
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("expected no errors, got %v", errs)
			}
		})
	}
}

// TestParseVersionOverridesReservedField asserts a manifest carrying the reserved
// release.version_overrides block parses into the typed field, validates at
// CurrentSchemaVersion, and does not bump schema_version.
func TestParseVersionOverridesReservedField(t *testing.T) {
	t.Parallel()

	cfg := parseInline(t, `
environments: [dev, prod]
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
release:
  version_overrides:
    dir: .cascade/version-overrides
`)
	if cfg.Release == nil {
		t.Fatalf("release block did not parse")
		return
	}
	if cfg.Release.VersionOverrides == nil {
		t.Fatalf("release.version_overrides did not parse")
		return
	}
	if got := cfg.Release.VersionOverrides.Dir; got != ".cascade/version-overrides" {
		t.Fatalf("release.version_overrides.dir = %q", got)
	}
	// version_overrides parses and structurally validates, but using it is a hard
	// lint error: the pointer is reserved and not wired to generation.
	if errs := Validate(cfg); !hasErrContaining(errs, "release.version_overrides is reserved and not implemented in this cascade version") {
		t.Fatalf("expected reserved version_overrides rejection, got %v", errs)
	}
	if got := cfg.GetSchemaVersion(); got != CurrentSchemaVersion {
		t.Fatalf("schema_version = %d, want %d (reserved shape must not bump)", got, CurrentSchemaVersion)
	}
	if CurrentSchemaVersion != 1 {
		t.Fatalf("CurrentSchemaVersion = %d, want 1 (reserved shape must not bump)", CurrentSchemaVersion)
	}
}

// TestParseVersionOverridesAbsentStaysValid confirms a manifest with a release
// block but no version_overrides validates clean: the reservation never adds an
// error to a manifest that does not opt in.
func TestParseVersionOverridesAbsentStaysValid(t *testing.T) {
	t.Parallel()

	cfg := parseInline(t, `
environments: [dev, prod]
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
release:
  disabled: false
`)
	if cfg.Release == nil {
		t.Fatalf("release block did not parse")
		return
	}
	if cfg.Release.VersionOverrides != nil {
		t.Fatalf("version_overrides should be nil when absent, got %#v", cfg.Release.VersionOverrides)
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}
