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

// ownRepoReconcileConfig builds the TrunkConfig cascade's own self-heal
// companion is generated from: reconcile enabled, sha pin mode (so the emitted
// action refs are SHA-pinned like the committed workflow), and the
// trigger-capable state token that carries the branch write.
func ownRepoReconcileConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch: "main",
		PinMode:     config.PinModeSHA,
		StateToken:  "${{ secrets.CASCADE_STATE_TOKEN }}",
		Reconcile:   &config.ReconcileConfig{Enabled: true},
	}
}

// TestReconcileGenerator_OwnRepoCompanion_StableSelector is the regression test
// for the self-install defect: cascade's own companion must install the latest
// NON-prerelease release, never the newest tag (which can be an rc or a draft).
// It fails before the own-repo variant exists and passes after.
func TestReconcileGenerator_OwnRepoCompanion_StableSelector(t *testing.T) {
	content, err := NewReconcileGenerator(ownRepoReconcileConfig(), t.TempDir(), WithOwnRepo()).GenerateCompanion()
	require.NoError(t, err)

	// The install selector filters out prereleases and drafts.
	assert.Contains(t, content, "--exclude-pre-releases")
	assert.Contains(t, content, "--exclude-drafts")

	// It must NOT install via an unfiltered `-L 1` selector that would pick up
	// the newest rc or draft tag.
	assert.NotContains(t, content, "gh release list -R stablekernel/cascade -L 1 --json",
		"own-repo companion must not install the newest tag unfiltered")

	// The versioned asset glob is preserved.
	assert.Contains(t, content, "cascade_*_linux_amd64.tar.gz")
}

// TestReconcileGenerator_OwnRepoCompanion_ScansBothTrees proves the own-repo
// companion diffs the composite-action tree as well as the workflow tree, since
// cascade governs pins in both, and wires the own-repo reconcile invocation.
func TestReconcileGenerator_OwnRepoCompanion_ScansBothTrees(t *testing.T) {
	content, err := NewReconcileGenerator(ownRepoReconcileConfig(), t.TempDir(), WithOwnRepo()).GenerateCompanion()
	require.NoError(t, err)

	assert.Contains(t, content, "-- .github/workflows/ .github/actions/",
		"own-repo companion must scan both the workflow and composite-action trees")
	assert.Contains(t, content, "cascade reconcile --own-repo --action-pins internal/generate/action_pins.yaml \"${args[@]}\"")
	assert.Contains(t, content, "--changed-file")

	// The commit stages the manifest-first allowlist rather than a blanket add.
	assert.Contains(t, content, "git add internal/generate/action_pins.yaml '.github/workflows/*.yaml'")
	assert.NotContains(t, content, "git add -A\n")
}

// TestReconcileGenerator_OwnRepoCompanion_SecurityPosture asserts the own-repo
// companion keeps the same trusted-metadata, same-repo-only, state-token posture
// as the emitted user companion.
func TestReconcileGenerator_OwnRepoCompanion_SecurityPosture(t *testing.T) {
	content, err := NewReconcileGenerator(ownRepoReconcileConfig(), t.TempDir(), WithOwnRepo()).GenerateCompanion()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(content, OwnRepoGeneratedFileMarker),
		"own-repo companion must carry the distinct own-repo generated marker")
	assert.NotContains(t, content, GeneratedFileMarker,
		"own-repo companion must not carry the plain marker, so verify's orphan scan skips it")
	assert.Contains(t, content, "on:\n  workflow_run:")
	assert.Contains(t, content, `workflows: ["PR Validation"]`)
	assert.Contains(t, content, "permissions: {}")
	assert.Contains(t, content, "if: github.event.workflow_run.event == 'pull_request'")

	// Trusted-metadata PR resolution, never off the fork-controlled artifact.
	assert.Contains(t, content, "const run = context.payload.workflow_run;")
	assert.Contains(t, content, "prNumber = run.pull_requests[0].number;")

	// Same-repo only: a fork head is skipped, never handed the write token.
	assert.Contains(t, content, "the self-heal push is same-repo only")

	// The branch write uses the trigger-capable state token, not the default one.
	assert.Contains(t, content, "token: ${{ secrets.CASCADE_STATE_TOKEN }}")

	// A pinned release binary, never a build off the PR head tree.
	assert.NotContains(t, content, "go run")
	assert.NotContains(t, content, "go build")
}

// TestReconcileGenerator_UserCompanion_UnchangedByOwnRepoVariant proves the
// own-repo option does not leak into the default user emission: the user
// companion still installs via setup-cli and scans only the workflows tree.
func TestReconcileGenerator_UserCompanion_UnchangedByOwnRepoVariant(t *testing.T) {
	content, err := NewReconcileGenerator(reconcileConfig(), t.TempDir()).GenerateCompanion()
	require.NoError(t, err)

	assert.Contains(t, content, "setup-cli@", "user companion still installs via setup-cli")
	assert.NotContains(t, content, "--exclude-pre-releases",
		"user companion must not carry the own-repo install selector")
	assert.NotContains(t, content, "--own-repo", "user companion must not run own-repo reconcile")
	assert.NotContains(t, content, "-- .github/workflows/ .github/actions/",
		"user companion scans only the workflows tree, not the composite-action tree")
	assert.Contains(t, content, `workflows: ["Cascade Reconcile Check"]`)
}

// TestReconcileGenerator_OwnRepoCompanion_Actionlint runs actionlint over the
// generated own-repo companion. Skipped when actionlint is not installed.
func TestReconcileGenerator_OwnRepoCompanion_Actionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	companion, err := NewReconcileGenerator(ownRepoReconcileConfig(), t.TempDir(), WithOwnRepo()).GenerateCompanion()
	require.NoError(t, err)

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	path := filepath.Join(wfDir, "pin-reconcile.yaml")
	require.NoError(t, os.WriteFile(path, []byte(companion), 0644))

	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = dir
	require.NoError(t, gitInit.Run(), "git init for actionlint project root")

	cmd := exec.Command(bin, "-shellcheck=", path)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	assert.NoError(t, runErr, "actionlint reported issues:\n%s", string(out))
}

// TestOwnRepoReconcileCompanionMatchesCommitted is the ongoing drift lock: the
// generated own-repo companion must equal the committed pin-reconcile.yaml
// byte-for-byte, so a future hand-edit of that file fails the suite. Run with
// UPDATE_GOLDEN=1 to regenerate the committed file after an intentional change.
func TestOwnRepoReconcileCompanionMatchesCommitted(t *testing.T) {
	companion, err := NewReconcileGenerator(ownRepoReconcileConfig(), t.TempDir(), WithOwnRepo()).GenerateCompanion()
	require.NoError(t, err)

	committedPath := filepath.Join(repoGitHubDir(t), "workflows", "pin-reconcile.yaml")

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(committedPath, []byte(companion), 0644))
		t.Logf("updated %s from the generator", committedPath)
		return
	}

	committed, err := os.ReadFile(committedPath) //nolint:gosec // fixed path under the repo's own .github tree.
	require.NoError(t, err)
	assert.Equal(t, string(committed), companion,
		"pin-reconcile.yaml drifted from the own-repo generator; regenerate with UPDATE_GOLDEN=1")
}

// TestPRWorkflowEmbedsReconcileDetectorShape locks the reconcile-detector shape
// inside the pr.yaml workflow-drift job. The detector is intentionally NOT a
// standalone generated file: cascade's own detector shares the workflow-drift
// job with the source-built verify step (it runs /tmp/cascade, not a release
// binary, under a set +e advisory frame), so generating the whole multi-purpose
// job would destabilize the required PR gate. Instead this test asserts the
// job carries the reconcile-detector invariants: it scans both governed trees,
// runs `reconcile --check --check-output` over --changed-file args, and uploads
// the pin-reconcile-result artifact.
func TestPRWorkflowEmbedsReconcileDetectorShape(t *testing.T) {
	prPath := filepath.Join(repoGitHubDir(t), "workflows", "pr.yaml")
	raw, err := os.ReadFile(prPath) //nolint:gosec // fixed path under the repo's own .github tree.
	require.NoError(t, err)
	pr := string(raw)

	assert.Contains(t, pr, "-- .github/workflows/ .github/actions/",
		"pr.yaml reconcile detector must scan both governed trees")
	assert.Contains(t, pr, "reconcile --check --check-output pin-reconcile-result.json",
		"pr.yaml must run the read-only reconcile detector")
	assert.Contains(t, pr, "--changed-file", "detector must pass the changed files through")
	assert.Contains(t, pr, "name: pin-reconcile-result",
		"pr.yaml must upload the pin-reconcile-result artifact for the companion")
}
