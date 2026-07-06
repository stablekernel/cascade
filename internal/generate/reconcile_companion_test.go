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
