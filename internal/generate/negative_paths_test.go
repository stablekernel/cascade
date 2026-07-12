package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// newNegPathsConfig builds a minimal manifest whose global triggers list mixes
// a positive wildcard with two negation ("!") exclusions, exercising the issue
// scenario: triggers: ["**", "!**/*.md", "!docs/**"].
func newNegPathsConfig(triggers []string) *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Triggers:     triggers,
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml"},
		},
	}
}

// TestEmit_NegativePathsAppearVerbatim asserts the generated orchestrate
// workflow's paths: filter reflects the trigger list exactly as written,
// including the "!"-prefixed exclusion entries. GitHub Actions evaluates these
// inline (a path triggers if it matches a positive pattern and no "!" pattern),
// so passing them through verbatim is what makes the emitted filter agree with
// CLI-side detection.
func TestEmit_NegativePathsAppearVerbatim(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/build.yaml"),
		[]byte("on:\n  workflow_call:\n"), 0644))

	triggers := []string{"**", "!**/*.md", "!docs/**"}
	gen := NewGenerator(newNegPathsConfig(triggers), tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// The on.push.paths block must contain every pattern, including negations.
	assert.Contains(t, result, "    paths:\n", "orchestrate workflow must emit a paths filter")
	for _, pattern := range triggers {
		assert.Contains(t, result, "      - '"+pattern+"'\n",
			"emitted paths filter must include trigger %q verbatim", pattern)
	}
}

// TestEmit_PositiveOnlyUnchanged asserts that a positive-only trigger list emits
// exactly as it did before negation support: no "!" entries appear, behaviour is
// non-breaking.
func TestEmit_PositiveOnlyUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/build.yaml"),
		[]byte("on:\n  workflow_call:\n"), 0644))

	gen := NewGenerator(newNegPathsConfig([]string{"src/**", "go.mod"}), tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "      - 'src/**'\n")
	assert.Contains(t, result, "      - 'go.mod'\n")
	// No negation entry must appear in the emitted paths filter for a
	// positive-only trigger list (scope the check to the paths block; the rest
	// of the workflow legitimately contains "!" in shell steps).
	assert.NotContains(t, pathsBlock(result), "!",
		"positive-only triggers must not emit any negation entry in the paths filter")
}

// TestEmitAndDetectAgree_DocsOnlyExcluded is the cross-implementation
// consistency check required by the issue: for triggers
// ["**", "!**/*.md", "!docs/**"], a docs-only change must NOT trigger, a source
// change MUST trigger, and a mixed change MUST trigger. The emitted GitHub
// Actions paths filter and the CLI-side detector (config.MatchAnyTrigger) must
// agree on all three outcomes, since both derive from the same trigger list.
func TestEmitAndDetectAgree_DocsOnlyExcluded(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/build.yaml"),
		[]byte("on:\n  workflow_call:\n"), 0644))

	triggers := []string{"**", "!**/*.md", "!docs/**"}
	gen := NewGenerator(newNegPathsConfig(triggers), tmpDir)
	emitted, err := gen.Generate()
	require.NoError(t, err)

	// Confirm the emitted GHA filter carries the full negation-aware list, so
	// GHA evaluates the same exclusions the CLI does.
	for _, pattern := range triggers {
		require.Contains(t, emitted, "      - '"+pattern+"'\n",
			"emitted filter missing %q", pattern)
	}

	cases := []struct {
		name        string
		changed     []string
		wantTrigger bool
	}{
		{"docs-only change is excluded", []string{"docs/README.md"}, false},
		{"source change triggers", []string{"src/main.go"}, true},
		{"mixed change triggers via source file", []string{"docs/README.md", "src/main.go"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// CLI-side detection.
			cli := config.MatchAnyTrigger(triggers, tc.changed)
			assert.Equal(t, tc.wantTrigger, cli,
				"CLI MatchAnyTrigger disagrees for %v", tc.changed)

			// GHA-side equivalent: a path set triggers iff at least one changed
			// file matches a positive pattern and is not excluded by a "!"
			// pattern. We model GHA's evaluation directly from the emitted
			// pattern list to prove the two implementations cannot drift.
			gha := ghaPathsTriggers(triggers, tc.changed)
			assert.Equal(t, tc.wantTrigger, gha,
				"modeled GHA paths filter disagrees for %v", tc.changed)

			// Both implementations must reach the same verdict.
			assert.Equal(t, cli, gha,
				"CLI detection and GHA paths filter must agree for %v", tc.changed)
		})
	}
}

// pathsBlock extracts the indented entries of the on.push.paths: filter from a
// generated workflow so assertions can target just the paths list rather than
// the whole document.
func pathsBlock(workflow string) string {
	const marker = "    paths:\n"
	start := strings.Index(workflow, marker)
	if start < 0 {
		return ""
	}
	rest := workflow[start+len(marker):]
	var b strings.Builder
	for _, line := range strings.Split(rest, "\n") {
		// paths entries are indented six spaces ("      - '...'").
		if strings.HasPrefix(line, "      ") {
			b.WriteString(line)
			b.WriteString("\n")
			continue
		}
		break
	}
	return b.String()
}

// ghaPathsTriggers models GitHub Actions' combined paths/paths-ignore evaluation
// over a changed-file set, independent of cascade's MatchAnyTrigger, so the test
// proves the two implementations agree rather than tautologically calling the
// same function. A path set triggers when any changed file matches at least one
// positive pattern and is not excluded by a later "!" pattern.
func ghaPathsTriggers(patterns, changedFiles []string) bool {
	for _, file := range changedFiles {
		matchedPositive := false
		excluded := false
		for _, p := range patterns {
			if strings.HasPrefix(p, "!") {
				if config.MatchGlobPattern(p, file) {
					excluded = true
				}
				continue
			}
			if config.MatchGlobPattern(p, file) {
				matchedPositive = true
			}
		}
		if matchedPositive && !excluded {
			return true
		}
	}
	return false
}
