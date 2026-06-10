package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSchemaVersionE2E exercises the full on-disk manifest load path
// (file -> ParseWithKey -> Validate + ValidateSchemaVersion) the way the
// parse-config command does, covering the schema-version compatibility gate:
//
//   - a valid schema_version parses and validates cleanly
//   - an incompatible (too-new) schema_version is rejected, blocking generation
//   - an omitted schema_version is accepted with a warning
func TestSchemaVersionE2E(t *testing.T) {
	manifest := func(schemaLine string) string {
		return fmt.Sprintf(`ci:
  config:
%s    trunk_branch: main
    environments:
      - dev
      - prod
    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers:
          - "src/**"
`, schemaLine)
	}

	tests := []struct {
		name         string
		schemaLine   string
		wantValid    bool
		wantWarn     bool
		errSubstring string
	}{
		{
			name:       "valid current schema_version parses and validates",
			schemaLine: fmt.Sprintf("    schema_version: %d\n", CurrentSchemaVersion),
			wantValid:  true,
			wantWarn:   false,
		},
		{
			name:       "omitted schema_version parses with a warning",
			schemaLine: "",
			wantValid:  true,
			wantWarn:   true,
		},
		{
			name:         "incompatible (too new) schema_version is rejected",
			schemaLine:   fmt.Sprintf("    schema_version: %d\n", CurrentSchemaVersion+1),
			wantValid:    false,
			errSubstring: "schema version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "manifest.yaml")
			if err := os.WriteFile(path, []byte(manifest(tt.schemaLine)), 0o644); err != nil {
				t.Fatalf("write manifest: %v", err)
			}

			cfg, err := Parse(path)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			errs := Validate(cfg)
			valid := len(errs) == 0
			if valid != tt.wantValid {
				t.Fatalf("valid = %v (errs=%v), want %v", valid, errs, tt.wantValid)
			}

			if tt.errSubstring != "" {
				found := false
				for _, e := range errs {
					if strings.Contains(e, tt.errSubstring) {
						found = true
					}
				}
				if !found {
					t.Fatalf("expected an error containing %q, got %v", tt.errSubstring, errs)
				}
			}

			warnings, fatal := cfg.ValidateSchemaVersion()
			if tt.wantValid && fatal != nil {
				t.Fatalf("unexpected fatal schema error on valid manifest: %v", fatal)
			}
			if tt.wantWarn && len(warnings) == 0 {
				t.Fatalf("expected a schema_version warning, got none")
			}
			if !tt.wantWarn && tt.wantValid && len(warnings) != 0 {
				t.Fatalf("expected no warnings, got %v", warnings)
			}
		})
	}
}
