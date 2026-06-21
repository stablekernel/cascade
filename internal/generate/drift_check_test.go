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

func driftCheckConfig(comment bool) *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		DriftCheck:   &config.DriftCheckConfig{Enabled: true, Comment: comment},
	}
}

func TestDriftCheckGenerator_Enabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.TrunkConfig
		want bool
	}{
		{"nil drift_check", &config.TrunkConfig{}, false},
		{"present but disabled", &config.TrunkConfig{DriftCheck: &config.DriftCheckConfig{}}, false},
		{"enabled", &config.TrunkConfig{DriftCheck: &config.DriftCheckConfig{Enabled: true}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NewDriftCheckGenerator(tt.cfg, t.TempDir()).Enabled())
		})
	}
}

func TestDriftCheckGenerator_Generate_Disabled(t *testing.T) {
	_, err := NewDriftCheckGenerator(&config.TrunkConfig{}, t.TempDir()).Generate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "drift_check is not enabled")
}

// TestDriftCheckGenerator_CheckJob_ReadOnly proves the pull_request lane is
// strictly read-only: it carries the cascade-owned marker, runs cascade verify,
// re-exits on drift, and never grants write permissions, references secrets, or
// posts a comment itself.
func TestDriftCheckGenerator_CheckJob_ReadOnly(t *testing.T) {
	content, err := NewDriftCheckGenerator(driftCheckConfig(true), t.TempDir()).Generate()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(content, GeneratedFileMarker),
		"generated file must start with the cascade-owned marker")
	assert.Contains(t, content, "on:\n  pull_request:")
	assert.Contains(t, content, "permissions:\n  contents: read")
	assert.Contains(t, content, "cascade verify --config .github/manifest.yaml")
	assert.Contains(t, content, `exit "$CODE"`, "must re-exit on drift to keep the gate red")

	// Security invariant: the pull_request lane must NOT carry write scope, must
	// NOT reference secrets, and must NOT post a comment itself.
	assert.NotContains(t, content, "pull-requests: write",
		"pull_request job must never have write permissions")
	assert.NotContains(t, content, "secrets.", "pull_request job must not reference secrets")
	assert.NotContains(t, content, "createComment", "pull_request job must not post a comment")
	assert.NotContains(t, content, "github-script", "pull_request job must not run github-script")
}

// TestDriftCheckGenerator_NoComment_OmitsArtifact proves that when the comment
// companion is disabled the check lane does not upload the artifact (there is no
// consumer), keeping the OFF-comment output minimal.
func TestDriftCheckGenerator_NoComment_OmitsArtifact(t *testing.T) {
	content, err := NewDriftCheckGenerator(driftCheckConfig(false), t.TempDir()).Generate()
	require.NoError(t, err)
	assert.NotContains(t, content, "upload-artifact")

	_, err = NewDriftCheckGenerator(driftCheckConfig(false), t.TempDir()).GenerateComment()
	require.Error(t, err, "comment companion must not generate when comment is disabled")
}

// TestDriftCheckGenerator_Comment_TrustedMetadata is the keystone security test.
// It proves the workflow_run comment companion derives the target PR number ONLY
// from trusted workflow_run run metadata, never from the fork-controlled
// artifact, and never checks out PR head code.
func TestDriftCheckGenerator_Comment_TrustedMetadata(t *testing.T) {
	content, err := NewDriftCheckGenerator(driftCheckConfig(true), t.TempDir()).GenerateComment()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(content, GeneratedFileMarker))
	assert.Contains(t, content, "on:\n  workflow_run:")
	assert.Contains(t, content, `workflows: ["Cascade Drift Check"]`)
	assert.Contains(t, content, "permissions: {}", "top-level permissions must be empty")
	assert.Contains(t, content, "pull-requests: write")
	assert.Contains(t, content, "actions: read")
	assert.Contains(t, content, "if: github.event.workflow_run.event == 'pull_request'")

	// Trusted-metadata derivation: PR number comes from run.pull_requests or the
	// head-SHA lookup, NEVER from the downloaded artifact.
	assert.Contains(t, content, "const run = context.payload.workflow_run;")
	assert.Contains(t, content, "prNumber = run.pull_requests[0].number;")
	assert.Contains(t, content, "listPullRequestsAssociatedWithCommit")
	assert.Contains(t, content, "commit_sha: run.head_sha")
	assert.Contains(t, content, "pr.head.sha === run.head_sha")

	// The artifact must only be read as data (report + exit flag), never used to
	// select the PR. issue_number must resolve from the trusted prNumber, and the
	// companion must never check out or execute PR head code.
	assert.Contains(t, content, "issue_number: prNumber")
	assert.NotContains(t, content, "actions/checkout", "comment job must never check out PR head")
	assert.NotContains(t, content, "ref:", "comment job must never reference a PR ref to check out")
}

// TestDriftCheckGenerator_Deterministic proves byte-stability across repeated
// generation, guarding the determinism the verify path depends on.
func TestDriftCheckGenerator_Deterministic(t *testing.T) {
	cfg := driftCheckConfig(true)
	g := NewDriftCheckGenerator(cfg, t.TempDir())

	check1, err := g.Generate()
	require.NoError(t, err)
	check2, err := g.Generate()
	require.NoError(t, err)
	assert.Equal(t, check1, check2)

	comment1, err := g.GenerateComment()
	require.NoError(t, err)
	comment2, err := g.GenerateComment()
	require.NoError(t, err)
	assert.Equal(t, comment1, comment2)
}

// TestDriftCheckGenerator_PinModeSHA proves third-party actions are SHA-pinned
// when pin_mode is sha, matching how cascade pins actions elsewhere.
func TestDriftCheckGenerator_PinModeSHA(t *testing.T) {
	cfg := driftCheckConfig(true)
	cfg.PinMode = config.PinModeSHA
	g := NewDriftCheckGenerator(cfg, t.TempDir())

	check, err := g.Generate()
	require.NoError(t, err)
	assert.Contains(t, check, "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02")

	comment, err := g.GenerateComment()
	require.NoError(t, err)
	assert.Contains(t, comment, "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093")
	assert.Contains(t, comment, "actions/github-script@f28e40c7f34bde8b3046d885e986cb6290c5673b")
}

// TestDriftCheckGenerator_Actionlint runs actionlint over both generated files.
// Skipped when actionlint is not installed so the suite stays hermetic.
func TestDriftCheckGenerator_Actionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	g := NewDriftCheckGenerator(driftCheckConfig(true), t.TempDir())
	check, err := g.Generate()
	require.NoError(t, err)
	comment, err := g.GenerateComment()
	require.NoError(t, err)

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	checkPath := filepath.Join(wfDir, "cascade-drift-check.yaml")
	commentPath := filepath.Join(wfDir, "cascade-drift-comment.yaml")
	require.NoError(t, os.WriteFile(checkPath, []byte(check), 0644))
	require.NoError(t, os.WriteFile(commentPath, []byte(comment), 0644))

	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = dir
	require.NoError(t, gitInit.Run(), "git init for actionlint project root")

	cmd := exec.Command(bin, "-shellcheck=", checkPath, commentPath)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	assert.NoError(t, runErr, "actionlint reported issues:\n%s", string(out))
}
