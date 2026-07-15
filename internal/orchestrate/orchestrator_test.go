package orchestrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

func TestParseResults(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: map[string]string{},
		},
		{
			name:  "single result",
			input: "app:success",
			expected: map[string]string{
				"app": "success",
			},
		},
		{
			name:  "multiple results",
			input: "infra:success,app:failure",
			expected: map[string]string{
				"infra": "success",
				"app":   "failure",
			},
		},
		{
			name:  "with spaces",
			input: "infra : success , app : failure",
			expected: map[string]string{
				"infra": "success",
				"app":   "failure",
			},
		},
		{
			name:  "three results",
			input: "build:success,deploy:success,test:failure",
			expected: map[string]string{
				"build":  "success",
				"deploy": "success",
				"test":   "failure",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseResults(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d results, got %d", len(tt.expected), len(result))
			}
			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("expected %s=%s, got %s=%s", k, v, k, result[k])
				}
			}
		})
	}
}

func TestSplitTrim(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		sep      string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			sep:      ",",
			expected: []string{},
		},
		{
			name:     "single item",
			input:    "hello",
			sep:      ",",
			expected: []string{"hello"},
		},
		{
			name:     "multiple items",
			input:    "a,b,c",
			sep:      ",",
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "with spaces",
			input:    " a , b , c ",
			sep:      ",",
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "colon separator",
			input:    "key:value",
			sep:      ":",
			expected: []string{"key", "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitTrim(tt.input, tt.sep)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d items, got %d: %v", len(tt.expected), len(result), result)
				return
			}
			for i, v := range tt.expected {
				if result[i] != v {
					t.Errorf("expected item %d to be %q, got %q", i, v, result[i])
				}
			}
		})
	}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		pattern string
		match   bool
	}{
		// Exact matches
		{"exact match", "src/main.go", "src/main.go", true},
		{"exact no match", "src/main.go", "src/other.go", false},

		// Single star patterns. A single "*" matches within one path segment and
		// does not cross "/", matching the emitted GitHub Actions paths filter.
		{"star extension root only", "main.go", "*.go", true},
		{"star extension does not cross slash", "src/main.go", "*.go", false},
		{"star extension no match", "src/main.go", "*.ts", false},
		{"star prefix", "test_main.go", "test_*", true},
		{"star single segment", "a/x/b", "a/*/b", true},
		{"star single segment no cross slash", "a/x/y/b", "a/*/b", false},

		// F12: a single "*" match is anchored, not an unanchored substring search.
		{"star anchored no leading garbage", "xfooybar", "foo*bar", false},
		{"star anchored match", "fooybar", "foo*bar", true},

		// Double star patterns.
		{"double star", "src/pkg/main.go", "src/**", true},
		{"double star nested", "src/a/b/c/main.go", "src/**", true},
		{"double star no match", "docs/readme.md", "src/**", false},

		// F01: a leading "**/" recursive glob followed by a segment glob must
		// match files at any depth, not only the literal suffix.
		{"recursive glob extension", "internal/foo.go", "**/*.go", true},
		{"recursive glob extension deep", "a/b/c/foo.go", "**/*.go", true},
		{"recursive glob extension no match", "internal/foo.ts", "**/*.go", false},
		{"recursive glob mid pattern", "pkg/a/b/c.ts", "pkg/**/*.ts", true},
		{"recursive glob mid pattern wrong prefix", "src/a/b/c.ts", "pkg/**/*.ts", false},

		// Common CI/CD patterns
		{"infra pattern", "infra/cdk/stack.ts", "infra/**", true},
		{"go.mod pattern", "go.mod", "go.mod", true},
		{"workflow pattern", ".github/workflows/ci.yaml", ".github/**", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := config.MatchGlobPattern(tt.pattern, tt.path)
			if result != tt.match {
				t.Errorf("MatchGlobPattern(%q, %q) = %v, want %v", tt.pattern, tt.path, result, tt.match)
			}
		})
	}
}

func TestTruncateSHA(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"abcdef1234567890", "abcdef1"},
		{"abc", "abc"},
		{"", ""},
		{"1234567", "1234567"},
		{"12345678", "1234567"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncateSHA(tt.input)
			if result != tt.expected {
				t.Errorf("truncateSHA(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestContains(t *testing.T) {
	slice := []string{"a", "b", "c"}

	if !contains(slice, "a") {
		t.Error("expected contains(slice, 'a') to be true")
	}
	if !contains(slice, "b") {
		t.Error("expected contains(slice, 'b') to be true")
	}
	if contains(slice, "d") {
		t.Error("expected contains(slice, 'd') to be false")
	}
	if contains(nil, "a") {
		t.Error("expected contains(nil, 'a') to be false")
	}
}

func TestIndexOf(t *testing.T) {
	slice := []string{"dev", "test", "uat", "prod"}

	tests := []struct {
		item     string
		expected int
	}{
		{"dev", 0},
		{"test", 1},
		{"uat", 2},
		{"prod", 3},
		{"staging", -1},
		{"", -1},
	}

	for _, tt := range tests {
		t.Run(tt.item, func(t *testing.T) {
			result := indexOf(slice, tt.item)
			if result != tt.expected {
				t.Errorf("indexOf(slice, %q) = %d, want %d", tt.item, result, tt.expected)
			}
		})
	}
}

func TestNewOrchestrator(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".github", "manifest.yaml")

	// Create .github directory
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Write a manifest with ci: key at top level
	config := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - test
      - prod
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.0
`
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Test creating orchestrator with default ci key
	orch, err := NewOrchestrator(configPath, "ci", "dev")
	if err != nil {
		t.Fatalf("NewOrchestrator failed: %v", err)
	}

	if orch.environment != "dev" {
		t.Errorf("expected environment 'dev', got %q", orch.environment)
	}
}

func TestNewOrchestratorInvalidPath(t *testing.T) {
	_, err := NewOrchestrator("/nonexistent/path/cicd.yaml", "", "dev")
	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}

func TestNewOrchestratorInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cicd.yaml")

	// Write invalid YAML
	if err := os.WriteFile(configPath, []byte("invalid: yaml: content:"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	_, err := NewOrchestrator(configPath, "", "dev")
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

// TestCalculateChangelogRefs covers the priority ladder of base-SHA selection:
//
//  1. Multi-env intermediate → next env's state
//  2. No-env or last env → state["release"] (or latest_release)
//  3. Nothing released → initial commit (git fallback, not exercised here)
//
// The git fallback isn't exercised; it requires a real repo and the rest of
// the priority ladder is what we're really fixing.
func TestCalculateChangelogRefs(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		envs        []string
		state       map[string]*config.EnvState
		latest      *config.LatestReleaseState
		wantSHA     string
		wantTag     string
	}{
		{
			name:        "intermediate env uses next env's state",
			environment: "dev",
			envs:        []string{"dev", "test", "prod"},
			state: map[string]*config.EnvState{
				"test": {SHA: "test-sha", Version: "v1.0.0-rc.0"},
				"prod": {SHA: "prod-sha", Version: "v0.9.0"},
			},
			wantSHA: "test-sha",
			wantTag: "v1.0.0-rc.0",
		},
		{
			name:        "intermediate env with empty next-env state falls through to release",
			environment: "dev",
			envs:        []string{"dev", "test", "prod"},
			state: map[string]*config.EnvState{
				"release": {SHA: "rel-sha", Version: "v1.0.0"},
			},
			wantSHA: "rel-sha",
			wantTag: "v1.0.0",
		},
		{
			name:        "no-env library uses state[release] when present",
			environment: "prerelease",
			envs:        []string{},
			state: map[string]*config.EnvState{
				"release": {SHA: "rel-sha", Version: "v0.1.0"},
			},
			wantSHA: "rel-sha",
			wantTag: "v0.1.0",
		},
		{
			name:        "no-env library falls back to latest_release when state[release] absent",
			environment: "prerelease",
			envs:        []string{},
			state:       map[string]*config.EnvState{},
			latest:      &config.LatestReleaseState{SHA: "latest-sha", Version: "v0.2.0"},
			wantSHA:     "latest-sha",
			wantTag:     "v0.2.0",
		},
		{
			name:        "terminal env in multi-env uses state[release]",
			environment: "prod",
			envs:        []string{"dev", "test", "prod"},
			state: map[string]*config.EnvState{
				"release": {SHA: "rel-sha", Version: "v1.0.0"},
			},
			wantSHA: "rel-sha",
			wantTag: "v1.0.0",
		},
		{
			name:        "state[release] with empty SHA is treated as absent",
			environment: "prerelease",
			envs:        []string{},
			state: map[string]*config.EnvState{
				"release": {SHA: "", Version: "v0.1.0"},
			},
			latest:  &config.LatestReleaseState{SHA: "latest-sha", Version: "v0.2.0"},
			wantSHA: "latest-sha",
			wantTag: "v0.2.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &Orchestrator{
				environment: tt.environment,
				cicdFile: &config.CICDFile{
					Config: &config.TrunkConfig{
						Environments: config.EnvNames(tt.envs...),
					},
					State:         tt.state,
					LatestRelease: tt.latest,
				},
			}
			gotSHA, gotTag := o.calculateChangelogRefs()
			if gotSHA != tt.wantSHA {
				t.Errorf("base SHA: got %q, want %q", gotSHA, tt.wantSHA)
			}
			if gotTag != tt.wantTag {
				t.Errorf("previous tag: got %q, want %q", gotTag, tt.wantTag)
			}
		})
	}
}
