package visualize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLookupTheme_BuiltinsAndAliases checks that the built-in names resolve and
// that "default" aliases the cascade theme, while an unknown name is reported as
// not built in so the caller can fall back to loading a file.
func TestLookupTheme_BuiltinsAndAliases(t *testing.T) {
	cases := []struct {
		name     string
		wantName string
		wantOK   bool
	}{
		{"cascade", "cascade", true},
		{"bland", "bland", true},
		{"default", "cascade", true},
		{"", "cascade", true},
		{"midnight", "", false},
		{"./themes/custom.json", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := LookupTheme(tc.name)
			if ok != tc.wantOK {
				t.Fatalf("LookupTheme(%q) ok = %v, want %v", tc.name, ok, tc.wantOK)
			}
			if ok && got.Name != tc.wantName {
				t.Errorf("LookupTheme(%q) name = %q, want %q", tc.name, got.Name, tc.wantName)
			}
		})
	}
}

// TestBuiltinThemes_EmitValidMermaid checks that each built-in theme renders an
// init directive and at least one classDef and class assignment, so the styling
// reaches the diagram rather than being silently dropped.
func TestBuiltinThemes_EmitValidMermaid(t *testing.T) {
	for _, theme := range []Theme{CascadeTheme, BlandTheme} {
		t.Run(theme.Name, func(t *testing.T) {
			out, err := NewMermaidEmitter().Emit(buildVM(t, representativeConfig()), theme)
			if err != nil {
				t.Fatalf("Emit: %v", err)
			}
			if !strings.Contains(out, "flowchart TD") {
				t.Errorf("missing flowchart header in:\n%s", out)
			}
			if !strings.Contains(out, "%%{init:") {
				t.Errorf("missing init directive in:\n%s", out)
			}
			if !strings.Contains(out, "classDef ") {
				t.Errorf("missing classDef in:\n%s", out)
			}
			if !strings.Contains(out, "class ") {
				t.Errorf("missing class assignment in:\n%s", out)
			}
		})
	}
}

// TestCascadeAndBland_Differ asserts the two built-in themes produce distinct
// output for the same manifest, so a theme swap is visually meaningful.
func TestCascadeAndBland_Differ(t *testing.T) {
	vm := buildVM(t, representativeConfig())

	cascade, err := NewMermaidEmitter().Emit(vm, CascadeTheme)
	if err != nil {
		t.Fatalf("cascade emit: %v", err)
	}
	bland, err := NewMermaidEmitter().Emit(vm, BlandTheme)
	if err != nil {
		t.Fatalf("bland emit: %v", err)
	}
	if cascade == bland {
		t.Fatalf("cascade and bland themes produced identical output:\n%s", cascade)
	}
}

// TestEmit_PerThemeDeterministic checks that emitting a theme repeatedly yields
// byte-identical output, so a styled diagram is stable run to run.
func TestEmit_PerThemeDeterministic(t *testing.T) {
	for _, theme := range []Theme{CascadeTheme, BlandTheme} {
		t.Run(theme.Name, func(t *testing.T) {
			first, err := NewMermaidEmitter().Emit(buildVM(t, representativeConfig()), theme)
			if err != nil {
				t.Fatalf("first emit: %v", err)
			}
			for i := 0; i < 10; i++ {
				next, err := NewMermaidEmitter().Emit(buildVM(t, representativeConfig()), theme)
				if err != nil {
					t.Fatalf("emit %d: %v", i, err)
				}
				if next != first {
					t.Fatalf("emit %d differs:\n%s\nvs\n%s", i, next, first)
				}
			}
		})
	}
}

// TestLoadTheme_AppliesStyling writes a small theme file, loads it, and asserts
// its custom fill reaches the emitted classDef.
func TestLoadTheme_AppliesStyling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.json")
	body := `{
  "name": "custom",
  "base": "base",
  "lineColor": "#abcdef",
  "nodeStyles": {
    "validate": {"fill": "#123456", "stroke": "#654321", "text": "#fefefe"}
  }
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write theme: %v", err)
	}

	theme, err := LoadTheme(path)
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	if theme.Name != "custom" {
		t.Errorf("theme name = %q, want custom", theme.Name)
	}

	out, err := NewMermaidEmitter().Emit(buildVM(t, representativeConfig()), theme)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if !strings.Contains(out, "fill:#123456") {
		t.Errorf("custom fill not applied in:\n%s", out)
	}
	if !strings.Contains(out, `"lineColor": "#abcdef"`) {
		t.Errorf("custom line color not applied in:\n%s", out)
	}
}

// TestLoadTheme_MissingFile_Errors checks that an absent theme path errors
// clearly rather than rendering an unstyled diagram.
func TestLoadTheme_MissingFile_Errors(t *testing.T) {
	_, err := LoadTheme(filepath.Join(t.TempDir(), "absent.json"))
	if err == nil {
		t.Fatal("expected error for missing theme file, got nil")
	}
	if !strings.Contains(err.Error(), "absent.json") {
		t.Errorf("error should name the file, got: %v", err)
	}
}

// TestLoadTheme_Malformed_Errors checks that invalid JSON and a missing name
// both produce a clear error.
func TestLoadTheme_Malformed_Errors(t *testing.T) {
	dir := t.TempDir()

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadTheme(bad); err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}

	noName := filepath.Join(dir, "noname.json")
	if err := os.WriteFile(noName, []byte(`{"base": "base"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadTheme(noName)
	if err == nil {
		t.Fatal("expected error for theme missing name, got nil")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error should mention the missing name field, got: %v", err)
	}
}
