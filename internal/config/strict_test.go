package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStrictManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "manifest.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return p
}

func errsContain(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

// TestValidate_RejectsMisspelledTopLevelKey verifies front-1 strictness: an
// unknown/misspelled top-level config key is a hard error carrying a
// did-you-mean suggestion, rather than being silently dropped by the lenient
// decoder (the pre-strictness behavior).
func TestValidate_RejectsMisspelledTopLevelKey(t *testing.T) {
	const body = `ci:
  config:
    trunk_branch: main
    enviroments: [dev, prod]
`
	cfg, err := Parse(writeStrictManifest(t, body))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	errs := Validate(cfg)
	if !errsContain(errs, "enviroments") {
		t.Fatalf("expected an error naming the unknown key 'enviroments', got: %v", errs)
	}
	if !errsContain(errs, "did you mean") || !errsContain(errs, "environments") {
		t.Fatalf("expected a 'did you mean environments?' suggestion, got: %v", errs)
	}
}

// TestValidate_RejectsComponentScopedFieldAtTopLevel verifies a field that is
// only meaningful under components.<name> (here: path) is rejected when pasted
// at the top level, instead of being silently ignored.
func TestValidate_RejectsComponentScopedFieldAtTopLevel(t *testing.T) {
	const body = `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    path: services/api
`
	cfg, err := Parse(writeStrictManifest(t, body))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	errs := Validate(cfg)
	if !errsContain(errs, "path") || !errsContain(errs, "unknown") {
		t.Fatalf("expected an unknown-field error naming 'path', got: %v", errs)
	}
}

// TestValidate_AcceptsKnownTopLevelKeys guards against false positives: a
// manifest using only modeled top-level fields must stay valid under strictness.
func TestValidate_AcceptsKnownTopLevelKeys(t *testing.T) {
	const body = `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    tag_prefix: v
`
	cfg, err := Parse(writeStrictManifest(t, body))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("Validate() rejected a manifest of known fields: %v", errs)
	}
}
