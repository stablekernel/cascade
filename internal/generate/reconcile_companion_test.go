package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

func reconcileConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Reconcile:    &config.ReconcileConfig{Enabled: true},
	}
}

func TestReconcileGenerator_Enabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.TrunkConfig
		want bool
	}{
		{"nil reconcile", &config.TrunkConfig{}, false},
		{"present but disabled", &config.TrunkConfig{Reconcile: &config.ReconcileConfig{}}, false},
		{"enabled", &config.TrunkConfig{Reconcile: &config.ReconcileConfig{Enabled: true}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NewReconcileGenerator(tt.cfg, t.TempDir()).Enabled())
		})
	}
}

func TestReconcileGenerator_Generate_Disabled(t *testing.T) {
	_, err := NewReconcileGenerator(&config.TrunkConfig{}, t.TempDir()).Generate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reconcile is not enabled")
}

// TestReconcileGenerator_CheckJob_ReadOnly proves the pull_request detector is
// strictly read-only: it carries the cascade-owned marker, runs the real
// `cascade reconcile --check` command, uploads the data-only relevance
// artifact, and never grants write permissions, references secrets, or pushes
// anything itself.
func TestReconcileGenerator_CheckJob_ReadOnly(t *testing.T) {
	content, err := NewReconcileGenerator(reconcileConfig(), t.TempDir()).Generate()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(content, GeneratedFileMarker),
		"generated file must start with the cascade-owned marker")
	assert.Contains(t, content, "on:\n  pull_request:")
	assert.Contains(t, content, "permissions:\n  contents: read")
	assert.Contains(t, content, "cascade reconcile --check")
	assert.Contains(t, content, "pin-reconcile-result")

	// Security invariant: the pull_request detector must NOT carry write scope,
	// must NOT reference secrets, and must NOT push or commit anything.
	assert.NotContains(t, content, "pull-requests: write",
		"pull_request job must never have write permissions")
	assert.NotContains(t, content, "contents: write",
		"pull_request job must never have write permissions")
	assert.NotContains(t, content, "secrets.", "pull_request job must not reference secrets")
	assert.NotContains(t, content, "git push", "pull_request job must never push")
	assert.NotContains(t, content, "git commit", "pull_request job must never commit")
}

// TestReconcileGenerator_Deterministic proves byte-stability across repeated
// generation, guarding the determinism the verify path depends on.
func TestReconcileGenerator_Deterministic(t *testing.T) {
	cfg := reconcileConfig()
	g := NewReconcileGenerator(cfg, t.TempDir())

	first, err := g.Generate()
	require.NoError(t, err)
	second, err := g.Generate()
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

// TestReconcileGenerator_SetupCLIPassesToken asserts that the setup-cli step
// passes github.token so that gh release download can authenticate on a cold
// tool-cache. Without the token: input the composite action's GH_TOKEN is
// empty and gh exits non-zero.
func TestReconcileGenerator_SetupCLIPassesToken(t *testing.T) {
	gen := NewReconcileGenerator(reconcileConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "token: ${{ github.token }}",
		"setup-cli step must pass github.token so gh release download succeeds on a cold cache")
}

// TestReconcileGenerator_NoGoRun proves the detector obtains a pinned release
// binary via the setup-cli composite action rather than building or running
// off the repository's own (possibly stale) source tree.
func TestReconcileGenerator_NoGoRun(t *testing.T) {
	content, err := NewReconcileGenerator(reconcileConfig(), t.TempDir()).Generate()
	require.NoError(t, err)
	assert.NotContains(t, content, "go run")
	assert.Contains(t, content, "setup-cli@")
}

// TestReconcileGenerator_Actionlint runs actionlint over the generated
// detector file. Skipped when actionlint is not installed so the suite stays
// hermetic.
func TestReconcileGenerator_Actionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	g := NewReconcileGenerator(reconcileConfig(), t.TempDir())
	check, err := g.Generate()
	require.NoError(t, err)

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	checkPath := filepath.Join(wfDir, "cascade-reconcile-check.yaml")
	require.NoError(t, os.WriteFile(checkPath, []byte(check), 0644))

	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = dir
	require.NoError(t, gitInit.Run(), "git init for actionlint project root")

	cmd := exec.Command(bin, "-shellcheck=", checkPath)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	assert.NoError(t, runErr, "actionlint reported issues:\n%s", string(out))
}

// TestReconcileGenerator_GenerateCompanion_Disabled mirrors Generate_Disabled
// for the workflow_run companion.
func TestReconcileGenerator_GenerateCompanion_Disabled(t *testing.T) {
	_, err := NewReconcileGenerator(&config.TrunkConfig{}, t.TempDir()).GenerateCompanion()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reconcile is not enabled")
}

// TestReconcileGenerator_Companion_TrustedMetadata is the keystone security
// test. It proves the workflow_run companion derives the target PR number
// ONLY from trusted workflow_run run metadata, never from the fork-controlled
// artifact, and obtains a pinned release binary rather than building or
// running off the repository's own (possibly malicious) source tree.
func TestReconcileGenerator_Companion_TrustedMetadata(t *testing.T) {
	content, err := NewReconcileGenerator(reconcileConfig(), t.TempDir()).GenerateCompanion()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(content, GeneratedFileMarker))
	assert.Contains(t, content, "on:\n  workflow_run:")
	assert.Contains(t, content, `workflows: ["Cascade Reconcile Check"]`)
	assert.Contains(t, content, "permissions: {}", "top-level permissions must be empty")
	assert.Contains(t, content, "if: github.event.workflow_run.event == 'pull_request'")

	// Trusted-metadata derivation: PR number comes from run.pull_requests or the
	// head-SHA lookup, NEVER from the downloaded artifact.
	assert.Contains(t, content, "const run = context.payload.workflow_run;")
	assert.Contains(t, content, "prNumber = run.pull_requests[0].number;")
	assert.Contains(t, content, "listPullRequestsAssociatedWithCommit")
	assert.Contains(t, content, "commit_sha: run.head_sha")

	// A pinned release binary, never a go run off the repository's own tree.
	assert.NotContains(t, content, "go run")
	assert.Contains(t, content, "setup-cli@")

	// Head files are fetched as data via the trusted refs/pull/<n>/head ref on
	// the base repo (never a direct checkout of the fork repository), so
	// nothing from a fork's own checkout configuration is ever executed.
	assert.Contains(t, content, "refs/pull/${{ steps.resolve.outputs.pr_number }}/head")
	assert.NotContains(t, content, "repository: ${{ steps.resolve.outputs.head_repo }}")

	// Pushes with the trigger-capable state token, never the default token.
	assert.Contains(t, content, resolveStateTokenRef(reconcileConfig()))
}

// TestReconcileGenerator_Companion_LoopGuards asserts all three loop-
// termination guards the companion relies on to avoid an unbounded reconcile
// loop:
//
//	(a) the write round-trips through the real, idempotent `cascade reconcile`
//	    command rather than a hand-rolled shell/yq edit;
//	(b) the push step is skipped when nothing actually changed;
//	(c) the push re-checks the branch's fresh tip and aborts rather than
//	    force-pushing over commits made since this run started.
func TestReconcileGenerator_Companion_LoopGuards(t *testing.T) {
	content, err := NewReconcileGenerator(reconcileConfig(), t.TempDir()).GenerateCompanion()
	require.NoError(t, err)

	// (a) idempotent typed-command write: the real command, not raw yq/sed.
	assert.Contains(t, content, "cascade reconcile \"${args[@]}\"",
		"must invoke the real idempotent reconcile command")
	assert.NotContains(t, content, "yq eval", "must not hand-edit the manifest with yq")

	// (b) push-only-if-git-diff-nonempty.
	assert.Contains(t, content, "git diff --quiet")
	assert.Contains(t, content, "nothing to push")

	// (c) reconcile-against-fresh-tip with non-fast-forward abort: never force.
	assert.Contains(t, content, "origin/$HEAD_REF")
	assert.Contains(t, content, "aborting rather than overwrite")
	assert.NotContains(t, content, "--force", "must never force-push over newer commits")
	assert.NotContains(t, content, "git push -f")
}

// TestReconcileGenerator_Companion_Deterministic proves byte-stability across
// repeated generation.
func TestReconcileGenerator_Companion_Deterministic(t *testing.T) {
	g := NewReconcileGenerator(reconcileConfig(), t.TempDir())
	first, err := g.GenerateCompanion()
	require.NoError(t, err)
	second, err := g.GenerateCompanion()
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

// TestReconcileGenerator_Companion_Actionlint runs actionlint over the
// generated companion file. Skipped when actionlint is not installed.
func TestReconcileGenerator_Companion_Actionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	g := NewReconcileGenerator(reconcileConfig(), t.TempDir())
	companion, err := g.GenerateCompanion()
	require.NoError(t, err)

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	companionPath := filepath.Join(wfDir, "cascade-reconcile-companion.yaml")
	require.NoError(t, os.WriteFile(companionPath, []byte(companion), 0644))

	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = dir
	require.NoError(t, gitInit.Run(), "git init for actionlint project root")

	cmd := exec.Command(bin, "-shellcheck=", companionPath)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	assert.NoError(t, runErr, "actionlint reported issues:\n%s", string(out))
}
