package simulate

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/promote"
)

var update = flag.Bool("update", false, "update golden files")

// knownResult builds a fixed simulation result for golden rendering.
func knownResult(t *testing.T) *Result {
	t.Helper()

	path := seedManifest(t)
	engine, err := NewEngine(path, WithActor("golden-actor"))
	require.NoError(t, err)
	result, err := engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)
	return result
}

func TestRenderHuman_Golden(t *testing.T) {
	result := knownResult(t)

	var buf bytes.Buffer
	require.NoError(t, result.RenderHuman(&buf))

	goldenPath := filepath.Join("testdata", "promote_human.golden")
	checkGolden(t, goldenPath, buf.Bytes())
}

func TestRenderJSON_Golden(t *testing.T) {
	result := knownResult(t)

	var buf bytes.Buffer
	require.NoError(t, result.RenderJSON(&buf))

	goldenPath := filepath.Join("testdata", "promote_json.golden")
	checkGolden(t, goldenPath, buf.Bytes())
}

// shaChangeResult builds a Result whose single environment moves between two
// real 40-character shas, for exercising the shortened sha rows and the compare
// link.
func shaChangeResult() *Result {
	before := map[string]*config.EnvState{
		"prod": {Version: "web-0.1.0", SHA: "8519f8c1111111111111111111111111111111aa"},
	}
	after := map[string]*config.EnvState{
		"prod": {Version: "web-0.2.0", SHA: "6628220222222222222222222222222222222bbb"},
	}
	return &Result{
		ActionDescribe: "promote (mode=default)",
		Diff:           DiffState(before, after),
	}
}

func TestRenderHuman_StateDiffShortShaAndCompareURL(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	require.NoError(t, shaChangeResult().RenderHuman(&buf, WithRepoURL("https://github.com/stablekernel/cascade")))
	out := buf.String()

	assert.Contains(t, out, "    version:  web-0.1.0 -> web-0.2.0\n")
	assert.Contains(t, out, "    sha:      8519f8c -> 6628220\n")
	assert.Contains(t, out, "    diff:     https://github.com/stablekernel/cascade/compare/8519f8c...6628220\n")
	assert.NotContains(t, out, "8519f8c1111", "full shas must be shortened")
}

func TestRenderHuman_CompareURLOmittedWhenRepoUnknown(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	require.NoError(t, shaChangeResult().RenderHuman(&buf))
	out := buf.String()

	assert.Contains(t, out, "    sha:      8519f8c -> 6628220\n")
	assert.NotContains(t, out, "    diff:", "no compare line when the repository is unknown")
	assert.NotContains(t, out, "compare/")
}

func TestRenderHuman_CompareURLOmittedForInitialDeploy(t *testing.T) {
	t.Parallel()

	before := map[string]*config.EnvState{"prod": {}}
	after := map[string]*config.EnvState{"prod": {SHA: "6628220222222222222222222222222222222bbb"}}
	result := &Result{ActionDescribe: "promote", Diff: DiffState(before, after)}

	var buf bytes.Buffer
	require.NoError(t, result.RenderHuman(&buf, WithRepoURL("https://github.com/stablekernel/cascade")))
	out := buf.String()

	assert.Contains(t, out, "    sha:  (none) -> 6628220\n")
	assert.NotContains(t, out, "compare/", "no compare line when there is no prior sha")
}

func TestRenderHuman_EffectFormatting(t *testing.T) {
	t.Parallel()

	result := &Result{
		ActionDescribe: "promote (mode=default)",
		Effects: []Effect{
			{Disposition: DispositionRun, Action: "deploy", Target: "staging", Detail: "from dev (sha 6628220, version web-0.2.0-rc.1)"},
			{Disposition: DispositionRun, Action: "write state", Target: "staging", Detail: "sha 6628220, version web-0.2.0-rc.1"},
			{Disposition: DispositionRun, Action: "release publish", Target: "web-0.2.0", Detail: "rc web-0.2.0-rc.1, sha 6628220"},
			{Disposition: DispositionGate, Action: "write state", Target: "prod", Detail: `deploy "web" failed, trunk state unchanged`},
			{Disposition: DispositionSkip, Action: "promote", Target: "prod", Detail: "no change required"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, result.RenderHuman(&buf))
	out := buf.String()

	assert.NotContains(t, out, "[run]", "the run disposition carries no tag")
	assert.Contains(t, out, "  1. deploy staging from dev\n")
	assert.Contains(t, out, "  2. write state staging (sha 6628220, version web-0.2.0-rc.1)\n")
	assert.Contains(t, out, "  3. release publish web-0.2.0 (rc web-0.2.0-rc.1, sha 6628220)\n")
	assert.Contains(t, out, `  4. gated write state prod (deploy "web" failed, trunk state unchanged)`+"\n")
	assert.Contains(t, out, "  5. skipped promote prod (no change required)\n")
}

func TestEffectLine_StubDeployKeepsDetail(t *testing.T) {
	t.Parallel()

	// A stubbed deploy callback shares the "deploy" action but its detail does
	// not start with "from ", so it keeps the parenthetical detail rather than
	// being trimmed like the promotion deploy marker.
	line := effectLine(Effect{Disposition: DispositionRun, Action: "deploy", Target: "web", Detail: "simulated success (not executed)"})
	assert.Equal(t, "deploy web (simulated success (not executed))", line)
}

func TestRenderJSON_OmitsCompareURL(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	require.NoError(t, shaChangeResult().RenderJSON(&buf))
	assert.NotContains(t, buf.String(), "compare/", "the compare link is a human-only rendering, never in JSON")
}

// checkGolden compares got against the golden file, rewriting it under -update.
func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()

	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}

	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden file missing; run with -update")
	require.Equal(t, string(want), string(got))
}

// TestRenderHuman_ChainSection renders the multi-environment cherry-pick chain
// as its own numbered section, in effect vocabulary, positioned between the
// state diff and the effects. A no-op environment reads as "skipped".
func TestRenderHuman_ChainSection(t *testing.T) {
	result := &Result{
		ActionName:     "hotfix",
		ActionDescribe: "hotfix (env=prod, commits=1)",
		Chain: []Effect{
			{Disposition: DispositionRun, Action: "cherry-pick", Target: "uat", Detail: "commit fixaaa1"},
			{Disposition: DispositionSkip, Action: "cherry-pick", Target: "prod", Detail: "already present"},
		},
		Effects: []Effect{
			{Disposition: DispositionRun, Action: "apply patch", Target: "prod", Detail: "commit fixaaa1"},
		},
		Note: boundaryNote,
	}

	var buf bytes.Buffer
	require.NoError(t, result.RenderHuman(&buf))
	out := buf.String()

	assert.Contains(t, out, "Cherry-pick chain (in order):")
	assert.Contains(t, out, "1. cherry-pick uat (commit fixaaa1)")
	assert.Contains(t, out, "2. skipped cherry-pick prod (already present)")
	assert.Less(t, indexOf(out, "Cherry-pick chain"), indexOf(out, "Effects (in order):"),
		"the chain section renders before the effects")
}

// TestRenderHuman_NoChainSectionWhenEmpty proves the chain section is omitted
// entirely for an action that produced no chain, so non-hotfix output is
// unchanged.
func TestRenderHuman_NoChainSectionWhenEmpty(t *testing.T) {
	result := &Result{
		ActionName:     "promote",
		ActionDescribe: "promote",
		Effects:        []Effect{{Disposition: DispositionRun, Action: "deploy", Target: "prod", Detail: "from uat"}},
		Note:           boundaryNote,
	}

	var buf bytes.Buffer
	require.NoError(t, result.RenderHuman(&buf))
	assert.NotContains(t, buf.String(), "Cherry-pick chain")
}

// indexOf returns the byte index of sub in s, or -1 when absent.
func indexOf(s, sub string) int { return strings.Index(s, sub) }
