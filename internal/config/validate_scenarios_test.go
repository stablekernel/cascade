package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestValidate_E2EScenarioConfigsPassGoValidation runs every shipped e2e
// scenario's config block through the real parse-and-validate path. The JSON
// schema sweep (internal/schema) checks structural shape; this sweep checks
// the Go-side validation rules, so a validation change that would newly
// reject a shipped scenario fails here instead of stranding the e2e harness.
func TestValidate_E2EScenarioConfigsPassGoValidation(t *testing.T) {
	root := scenarioRepoRoot(t)
	matches, err := filepath.Glob(filepath.Join(root, "e2e", "scenarios", "*.yaml"))
	if err != nil {
		t.Fatalf("glob scenarios: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one e2e scenario manifest")
	}

	validated := 0
	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read scenario: %v", err)
			}
			var doc struct {
				Config yaml.Node `yaml:"config"`
			}
			if err := yaml.Unmarshal(data, &doc); err != nil {
				t.Fatalf("parse scenario: %v", err)
			}
			if doc.Config.IsZero() {
				t.Skip("scenario has no config block")
			}
			// Scenario files are not ci-wrapped; wrap the config block and
			// route it through the same on-disk parse path the CLI uses.
			wrapped, err := yaml.Marshal(map[string]any{"ci": map[string]any{"config": &doc.Config}})
			if err != nil {
				t.Fatalf("wrap scenario config: %v", err)
			}
			tmp := filepath.Join(t.TempDir(), "manifest.yaml")
			if err := os.WriteFile(tmp, wrapped, 0o600); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			cfg, err := ParseWithKey(tmp, "ci")
			if err != nil {
				t.Fatalf("scenario config must parse: %v", err)
			}
			if errs := Validate(cfg); len(errs) > 0 {
				t.Fatalf("scenario config must pass validation, got:\n%s", errs)
			}
			validated++
		})
	}
}

// scenarioRepoRoot walks up from the working directory to the repository root
// (the directory containing go.mod).
func scenarioRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}
