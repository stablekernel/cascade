package config

import (
	"strings"
	"testing"
)

func TestGetSchemaVersion(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"omitted defaults to current", 0, CurrentSchemaVersion},
		{"explicit current", 1, 1},
		{"explicit value passes through", 2, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TrunkConfig{SchemaVersion: tt.in}
			if got := cfg.GetSchemaVersion(); got != tt.want {
				t.Fatalf("GetSchemaVersion() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestValidateSchemaVersionHelper exercises the full compatibility matrix
// against the unexported validateSchemaVersion helper. This lets us cover
// branches that are unreachable through ValidateSchemaVersion while
// MinSchemaVersion == CurrentSchemaVersion (e.g. the v < min rejection and the
// min..current-1 warn path that would require min < current to be live).
func TestValidateSchemaVersionHelper(t *testing.T) {
	const min = 2
	const current = 4

	tests := []struct {
		name         string
		v            int
		wantWarn     bool
		wantErr      bool
		warnContains string
		errContains  string
	}{
		{
			name:         "omitted (0) warns and is accepted",
			v:            0,
			wantWarn:     true,
			warnContains: "no schema_version",
		},
		{
			name:        "negative is rejected",
			v:           -1,
			wantErr:     true,
			errContains: "positive integer",
		},
		{
			name:        "below min is rejected",
			v:           min - 1,
			wantErr:     true,
			errContains: "no longer supported",
		},
		{
			name:         "at min (older supported) warns and is accepted",
			v:            min,
			wantWarn:     true,
			warnContains: "supports schema versions up to",
		},
		{
			name:         "between min and current warns and is accepted",
			v:            min + 1,
			wantWarn:     true,
			warnContains: "supports schema versions up to",
		},
		{
			name:         "current minus one warns and is accepted",
			v:            current - 1,
			wantWarn:     true,
			warnContains: "supports schema versions up to",
		},
		{
			name: "exactly current is accepted silently",
			v:    current,
		},
		{
			name:        "above current is rejected",
			v:           current + 1,
			wantErr:     true,
			errContains: "upgrade the CLI",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warn, err := validateSchemaVersion(tt.v, min, current)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (warn=%q)", warn)
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantWarn {
				if warn == "" {
					t.Fatalf("expected a warning, got none")
				}
				if tt.warnContains != "" && !strings.Contains(warn, tt.warnContains) {
					t.Fatalf("warning %q does not contain %q", warn, tt.warnContains)
				}
				return
			}
			if warn != "" {
				t.Fatalf("expected no warning, got %q", warn)
			}
		})
	}
}

func TestValidateSchemaVersion(t *testing.T) {
	tests := []struct {
		name        string
		version     int
		wantFatal   bool
		wantWarn    bool
		errContains string
	}{
		{
			name:     "current version accepted silently",
			version:  CurrentSchemaVersion,
			wantWarn: false,
		},
		{
			name:     "omitted version warns and is accepted",
			version:  0,
			wantWarn: true,
		},
		{
			name:        "newer than CLI is rejected",
			version:     CurrentSchemaVersion + 1,
			wantFatal:   true,
			errContains: "upgrade the CLI",
		},
		{
			name:        "negative version is rejected",
			version:     -1,
			wantFatal:   true,
			errContains: "positive integer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TrunkConfig{SchemaVersion: tt.version}
			warnings, fatalErr := cfg.ValidateSchemaVersion()

			if tt.wantFatal {
				if fatalErr == nil {
					t.Fatalf("expected fatal error, got nil")
					return
				}
				if tt.errContains != "" && !strings.Contains(fatalErr.Error(), tt.errContains) {
					t.Fatalf("error %q does not contain %q", fatalErr.Error(), tt.errContains)
				}
				return
			}

			if fatalErr != nil {
				t.Fatalf("unexpected fatal error: %v", fatalErr)
			}
			if tt.wantWarn && len(warnings) == 0 {
				t.Fatalf("expected a warning, got none")
			}
			if !tt.wantWarn && len(warnings) != 0 {
				t.Fatalf("expected no warnings, got %v", warnings)
			}
		})
	}
}

// TestValidateRejectsIncompatibleSchemaVersion ensures the schema check is wired
// into the top-level Validate so an unreadable manifest blocks generation.
func TestValidateRejectsIncompatibleSchemaVersion(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: CurrentSchemaVersion + 1,
		TrunkBranch:   "main",
	}
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatalf("expected Validate to report a schema_version error")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "schema version") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a schema version error in %v", errs)
	}
}

// TestValidateAcceptsCurrentSchemaVersion ensures a valid version does not
// introduce a spurious validation error.
func TestValidateAcceptsCurrentSchemaVersion(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: CurrentSchemaVersion,
		TrunkBranch:   "main",
	}
	for _, e := range Validate(cfg) {
		if strings.Contains(e, "schema version") {
			t.Fatalf("unexpected schema version error: %s", e)
		}
	}
}

// TestValidateOmittedSchemaVersionIsNotFatal ensures an omitted schema_version
// (an unversioned manifest) is accepted by Validate (warning only).
func TestValidateOmittedSchemaVersionIsNotFatal(t *testing.T) {
	cfg := &TrunkConfig{TrunkBranch: "main"} // SchemaVersion == 0
	for _, e := range Validate(cfg) {
		if strings.Contains(e, "schema version") {
			t.Fatalf("omitted schema_version should not be fatal, got: %s", e)
		}
	}
}
