package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stateWriteGitHubBranch is the marker line that opens the API path of the
// generated state-write logic.
const stateWriteServerCheck = `if [[ "$GITHUB_SERVER_URL" != "https://github.com" ]]; then`

// assertDualStateWrite asserts the rendered workflow contains both the GitHub
// REST API state-write path and the gitea/act git-push path.
func assertDualStateWrite(t *testing.T, content string) {
	t.Helper()

	// The environment detection that splits the two paths, reused from the
	// "Only dispatch on real GitHub" dispatch pattern.
	assert.Contains(t, content, stateWriteServerCheck,
		"state write must branch on GITHUB_SERVER_URL to detect act/gitea vs real GitHub")

	// gitea/act path: the existing git push to the trunk branch must be preserved
	// so e2e keeps passing (gitea enforces neither protection nor signatures).
	assert.Contains(t, content, `git push origin "HEAD:$BRANCH"`,
		"gitea/act path must still push state with plain git")

	// Real GitHub path: write through the Contents REST API so the commit is
	// signed and can bypass branch protection.
	assert.Contains(t, content, `gh api "${API_ARGS[@]}"`,
		"real GitHub path must write state through the Contents REST API")
	assert.Contains(t, content, "/contents/$MANIFEST_FILE",
		"API path must target the manifest via the Contents API")
	assert.Contains(t, content, "-X PUT",
		"Contents API write must be a PUT")
}

// TestOrchestrateFinalizeDualStateWrite verifies the orchestrate Update Manifest
// step emits both the API and git-push paths and authenticates the API with the
// state token.
func TestOrchestrateFinalizeDualStateWrite(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	assertDualStateWrite(t, content)

	// The Update Manifest step must expose GH_TOKEN for the API call, defaulting
	// to the standard token.
	assert.Contains(t, content, "GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}",
		"Update Manifest must default GH_TOKEN to GITHUB_TOKEN")
}

// TestOrchestrateFinalizeStateTokenOverride verifies the configurable state
// token flows into the orchestrate API auth.
func TestOrchestrateFinalizeStateTokenOverride(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		StateToken:   "${{ secrets.CASCADE_BOT_TOKEN }}",
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "GH_TOKEN: ${{ secrets.CASCADE_BOT_TOKEN }}",
		"configured state_token must authenticate the API state write")
}

// TestReleaseFinalizeDualStateWrite verifies the release finalize job emits both
// state-write paths for latest_release.
func TestReleaseFinalizeDualStateWrite(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"prod"},
	}

	gen := NewReleaseGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assertDualStateWrite(t, content)
	assert.Contains(t, content, "GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}",
		"Update Latest Release State must default GH_TOKEN to GITHUB_TOKEN")
}

// TestPromoteFinalizeStateTokenAuth verifies the promote finalize step authes
// the CLI's API state write with the state token.
func TestPromoteFinalizeStateTokenAuth(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		StateToken:   "${{ secrets.CASCADE_BOT_TOKEN }}",
	}

	gen := NewPromoteGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// The promote finalize runs `cascade promote finalize --commit-push`, whose
	// CLI performs the API write on real GitHub; GH_TOKEN must carry the state
	// token so the API call can bypass branch protection.
	require.Contains(t, content, "cascade promote finalize")
	assert.Contains(t, content, "GH_TOKEN: ${{ secrets.CASCADE_BOT_TOKEN }}",
		"Finalize Promotion must auth the API state write with state_token")
}

// assertAPIAuthorStamp asserts the Contents API state-write path stamps both the
// author and the committer with the given identity, so an API-created state
// commit is attributed to the bot rather than the token owner GitHub would
// otherwise default to.
func assertAPIAuthorStamp(t *testing.T, content, name, email string) {
	t.Helper()
	for _, want := range []string{
		`-f "author[name]=` + name + `"`,
		`-f "author[email]=` + email + `"`,
		`-f "committer[name]=` + name + `"`,
		`-f "committer[email]=` + email + `"`,
	} {
		assert.Contains(t, content, want,
			"Contents API state write must stamp the bot identity on author and committer")
	}
}

// TestOrchestrateFinalizeStampsBotAuthor verifies the orchestrate state-write API
// path attributes the commit to the github-actions[bot] default rather than the
// token owner.
func TestOrchestrateFinalizeStampsBotAuthor(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assertAPIAuthorStamp(t, content, "github-actions[bot]", "github-actions[bot]@users.noreply.github.com")
}

// TestReleaseFinalizeStampsBotAuthor verifies the release latest_release state
// write attributes the API commit to the bot default.
func TestReleaseFinalizeStampsBotAuthor(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"prod"},
	}

	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assertAPIAuthorStamp(t, content, "github-actions[bot]", "github-actions[bot]@users.noreply.github.com")
}

// TestStateWriteHonorsCustomGitIdentity verifies a manifest git config override
// flows into the Contents API author and committer fields, so operators can
// attribute automated state commits to a custom identity.
func TestStateWriteHonorsCustomGitIdentity(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Git: &config.GitConfig{
			Mode:      config.GitModeCustom,
			UserName:  "release-bot",
			UserEmail: "release-bot@example.com",
		},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assertAPIAuthorStamp(t, content, "release-bot", "release-bot@example.com")
}

// TestStateWriteRetryCeilingAndConvergenceMarker asserts the emitted state-write
// loop retries ten times on both the git-push and Contents-API branches, emits
// the greppable "cascade-state-write: attempt=N/10" marker per attempt, an "ok"
// marker on success and an "exhausted" marker on failure, and has dropped the old
// five-attempt bound and fixed RANDOM sleep. The marker lets a live concurrency
// proof grep every lane for convergence without any exhaustion.
func TestStateWriteRetryCeilingAndConvergenceMarker(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	// Both the git-push (act/gitea) and the Contents-API (real GitHub) branches
	// carry the raised loop bound.
	assert.Equal(t, 2, strings.Count(content, "for attempt in 1 2 3 4 5 6 7 8 9 10"),
		"both state-write branches must retry ten times")
	// The greppable convergence markers, on both branches.
	assert.Equal(t, 2, strings.Count(content, `cascade-state-write: attempt=$attempt/10`),
		"each branch must emit the per-attempt convergence marker")
	assert.Contains(t, content, `cascade-state-write: ok`,
		"a successful write must emit the ok convergence marker")
	assert.Equal(t, 2, strings.Count(content, `cascade-state-write: exhausted attempts=10`),
		"each branch must emit the exhaustion marker so a live proof asserts its absence")

	// The old five-attempt bound and fixed RANDOM sleep must be gone.
	assert.NotContains(t, content, "after 5 attempts", "the old five-attempt failure text must be gone")
	assert.NotContains(t, content, "RANDOM % 5 + 2", "the fixed RANDOM sleep must be replaced by exponential jittered backoff")
}

// TestStateWriteNoEmDash guards the hard project rule that generated output
// contains no em dashes.
func TestStateWriteNoEmDash(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	orch, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	prom, err := NewPromoteGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	rel, err := NewReleaseGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	for name, content := range map[string]string{"orchestrate": orch, "promote": prom, "release": rel} {
		assert.False(t, strings.ContainsRune(content, '—'), "%s output must contain no em dashes", name)
	}
}
