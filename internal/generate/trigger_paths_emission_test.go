package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// triggerEmissionConfig builds a minimal orchestrate config whose build
// callbacks carry the supplied trigger lists.
func triggerEmissionConfig(t *testing.T, triggerLists ...[]string) (*config.TrunkConfig, string) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
	}
	names := []string{"app", "web", "api"}
	for i, triggers := range triggerLists {
		cfg.Builds = append(cfg.Builds, config.BuildConfig{
			Name:     names[i],
			Workflow: ".github/workflows/build.yaml",
			Triggers: triggers,
		})
	}
	return cfg, tmpDir
}

// TestGenerator_PathsFilterPreservesPatternOrder asserts the emitted paths
// filter keeps the manifest's pattern order. GitHub evaluates paths filters
// in order (last-match-wins), so re-sorting the list changes its meaning: a
// sorted list puts every "!" exclusion first, where it negates nothing.
func TestGenerator_PathsFilterPreservesPatternOrder(t *testing.T) {
	cfg, tmpDir := triggerEmissionConfig(t, []string{"src/**", "!src/vendor/**", "src/vendor/keep/**"})

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, result,
		"    paths:\n"+
			"      - 'src/**'\n"+
			"      - '!src/vendor/**'\n"+
			"      - 'src/vendor/keep/**'\n",
		"paths filter must preserve manifest pattern order (exclusion after inclusion, re-inclusion after exclusion)")
}

// TestGenerator_NegationOnlyTriggersEmitPathsIgnore asserts a negation-only
// trigger list is emitted as paths-ignore. GitHub rejects a paths filter made
// only of "!" patterns (the workflow would never run); paths-ignore is the
// documented construct for exclude-only filtering and matches the CLI
// evaluator's semantics for these lists exactly.
func TestGenerator_NegationOnlyTriggersEmitPathsIgnore(t *testing.T) {
	cfg, tmpDir := triggerEmissionConfig(t, []string{"!docs/**", "!**/*.md"})

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, result,
		"    paths-ignore:\n"+
			"      - 'docs/**'\n"+
			"      - '**/*.md'\n",
		"negation-only trigger list must emit paths-ignore with the bare patterns")
	assert.NotContains(t, result, "    paths:\n",
		"negation-only trigger list must not emit a paths filter with no positive entry")
}

// TestGenerator_UnionAcrossCallbacksDropsForeignNegations asserts that when
// distinct callback trigger lists are unioned into one workflow-level filter,
// "!" exclusions are dropped: under last-match-wins a negation from one
// callback would veto a sibling callback's positive match and the workflow
// would silently never fire for that sibling's change. Over-firing is safe
// (per-callback CLI detection still applies each list exactly); under-firing
// silently skips builds.
func TestGenerator_UnionAcrossCallbacksDropsForeignNegations(t *testing.T) {
	cfg, tmpDir := triggerEmissionConfig(t,
		[]string{"**/*.md"},
		[]string{"docs/**", "!docs/api/**"},
	)

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, result,
		"    paths:\n"+
			"      - '**/*.md'\n"+
			"      - 'docs/**'\n",
		"union across callbacks must keep every callback's positive patterns")
	assert.NotContains(t, result, "!docs/api/**",
		"a callback-scoped exclusion must not reach the unioned workflow-level filter")
}

// TestGenerator_NegationOnlyListAmongDistinctListsOmitsFilter asserts that
// when one callback's list is negation-only ("everything except X") and a
// sibling's differs, no flat filter can express the union, so the workflow
// runs on every push and defers to CLI-side change detection.
func TestGenerator_NegationOnlyListAmongDistinctListsOmitsFilter(t *testing.T) {
	cfg, tmpDir := triggerEmissionConfig(t,
		[]string{"src/**"},
		[]string{"!docs/**"},
	)

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "  push:\n", "push trigger must remain")
	assert.NotContains(t, result, "    paths:\n",
		"an inexpressible union must omit the paths filter rather than under-fire")
	assert.NotContains(t, result, "    paths-ignore:\n",
		"an inexpressible union must omit paths-ignore too")
}
