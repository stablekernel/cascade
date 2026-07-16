package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// matrixBaseConfig is a minimal valid manifest with one build, used to isolate
// the matrix dimension validation rules.
func matrixBaseConfig() *TrunkConfig {
	return &TrunkConfig{
		TrunkBranch:  "main",
		Environments: EnvNames("dev", "prod"),
		Builds: []BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml"},
		},
	}
}

// TestValidate_MatrixDimensions covers the matrix dimension key and axis
// rules. Dimension keys are emitted raw as YAML mapping keys
// (strategy.matrix.<key>), as with: input keys, and inside
// ${{ matrix.<key> }} expressions, so a key GitHub Actions cannot parse must
// be rejected at validation time instead of at the first workflow run. GitHub
// accepts a matrix key that starts with a letter or underscore and contains
// only letters, digits, hyphens, and underscores; hyphens are valid in both a
// matrix axis name and a matrix.<key> context dereference. An axis with no
// values fails at run time with an empty matrix vector, so it is likewise
// rejected up front.
func TestValidate_MatrixDimensions(t *testing.T) {
	tests := []struct {
		name       string
		dimensions map[string][]string
		wantErrs   bool
		wantSubstr []string
	}{
		{
			name:       "key with a space rejected",
			dimensions: map[string][]string{"go version": {"1.22"}},
			wantErrs:   true,
			wantSubstr: []string{"go version", "matrix.dimensions"},
		},
		{
			name:       "key with a leading digit rejected",
			dimensions: map[string][]string{"2fast": {"a"}},
			wantErrs:   true,
			wantSubstr: []string{"2fast", "matrix.dimensions"},
		},
		{
			name:       "key with a leading hyphen rejected",
			dimensions: map[string][]string{"-os": {"linux"}},
			wantErrs:   true,
			wantSubstr: []string{"-os", "matrix.dimensions"},
		},
		{
			name:       "key with a colon rejected",
			dimensions: map[string][]string{"os: x": {"linux"}},
			wantErrs:   true,
			wantSubstr: []string{"matrix.dimensions"},
		},
		{
			name:       "key with a dot rejected",
			dimensions: map[string][]string{"go.version": {"1.22"}},
			wantErrs:   true,
			wantSubstr: []string{"go.version", "matrix.dimensions"},
		},
		{
			name:       "key with a newline rejected",
			dimensions: map[string][]string{"os\nx": {"linux"}},
			wantErrs:   true,
			wantSubstr: []string{"matrix.dimensions"},
		},
		{
			name:       "empty key rejected",
			dimensions: map[string][]string{"": {"linux"}},
			wantErrs:   true,
			wantSubstr: []string{"matrix.dimensions"},
		},
		{
			name:       "empty axis rejected",
			dimensions: map[string][]string{"os": {}},
			wantErrs:   true,
			wantSubstr: []string{"os", "at least one value"},
		},
		{
			name: "valid identifier keys pass",
			dimensions: map[string][]string{
				"os":         {"ubuntu-22.04", "macos-14"},
				"go_version": {"1.22"},
				"_arch":      {"amd64"},
			},
			wantErrs: false,
		},
		{
			// GitHub allows hyphens in a matrix axis name and in the
			// matrix.<key> dot dereference (for example matrix.node-version),
			// so a hyphenated key must keep validating.
			name:       "hyphenated key passes",
			dimensions: map[string][]string{"go-version": {"1.22", "1.23"}},
			wantErrs:   false,
		},
		{
			name:       "empty dimensions map passes",
			dimensions: map[string][]string{},
			wantErrs:   false,
		},
		{
			name:       "nil matrix passes",
			dimensions: nil,
			wantErrs:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := matrixBaseConfig()
			if tt.dimensions != nil || tt.name == "empty dimensions map passes" {
				cfg.Builds[0].Matrix = &MatrixConfig{Dimensions: tt.dimensions}
			}
			errs := Validate(cfg)
			if !tt.wantErrs {
				assert.Empty(t, errs, "expected no validation errors, got %v", errs)
				return
			}
			assert.NotEmpty(t, errs, "expected validation errors")
			joined := strings.Join(errs, "\n")
			for _, substr := range tt.wantSubstr {
				assert.Contains(t, joined, substr)
			}
		})
	}
}
