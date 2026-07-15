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
		Environments: config.EnvNames("dev"),
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
		Environments: config.EnvNames("dev"),
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
		Environments: config.EnvNames("prod"),
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
		Environments: config.EnvNames("dev", "prod"),
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
		Environments: config.EnvNames("dev"),
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
		Environments: config.EnvNames("prod"),
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
		Environments: config.EnvNames("dev"),
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
		Environments: config.EnvNames("dev"),
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

// TestStateWriteEmitsSkipCIMarker asserts the generated state-write step stamps
// the [skip ci] marker on the commit message in BOTH the git-push (act/gitea)
// path and the Contents-API (real GitHub) path. The marker is load-bearing: it
// suppresses the tag-push CI trigger on the state commit so the candidate release
// is dispatched explicitly rather than racing a native tag-push trigger. A
// regression dropping it would let a state commit re-trigger the pipeline. The
// marker was previously asserted nowhere on the generator emission.
func TestStateWriteEmitsSkipCIMarker(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}
	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	// git-push path (act/gitea): the shell commit carries the marker.
	assert.Contains(t, content, `git commit -m "chore: update state for $ENVIRONMENT [skip ci]"`,
		"git-push state commit must carry [skip ci] to suppress the tag-push trigger")
	// Contents-API path (real GitHub): the API message carries the marker.
	assert.Contains(t, content, `message=chore: update state for $ENVIRONMENT [skip ci]`,
		"Contents-API state commit must carry [skip ci] to suppress the tag-push trigger")
}

// TestHotfixCherryPickCommitOmitsSkipCIMarker is the negative half of the skip-ci
// contract: a hotfix cherry-pick conflict commit is a real code commit that must
// flow through CI, so it must NOT carry the [skip ci] state-suppression marker.
// This guards against a blanket marker stamp leaking onto a non-state commit.
func TestHotfixCherryPickCommitOmitsSkipCIMarker(t *testing.T) {
	content, err := NewHotfixGenerator(threeEnvHotfixConfig(), "").Generate()
	require.NoError(t, err)
	require.Contains(t, content, "cherry-pick",
		"hotfix workflow should emit the cherry-pick recovery commit")

	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "cherry-pick") && strings.Contains(line, "git commit -m") {
			assert.NotContains(t, line, "[skip ci]",
				"hotfix cherry-pick conflict commit must not carry [skip ci]; it is a real commit that must trigger CI")
		}
	}
}

// TestStateWriteBindsCASTokenToBaseBlob asserts the Contents-API state write
// binds its compare-and-swap token to the exact base blob the content was
// rendered from. The emitter captures BASE_SHA with git rev-parse right after
// the fetch/reset (before applyFn re-renders), then passes sha=$BASE_SHA to the
// PUT. It must NOT read a separate, later current-blob sha through the Contents
// API: a decoupled read refreshes to a sibling component's blob that landed
// after the render, so the PUT would satisfy the CAS with a token unrelated to
// the content's base and land stale bytes as a clean child of the sibling
// commit, silently reverting the sibling leaf. Binding the token to the base
// blob makes a sibling write turn the PUT into a 409 the retry loop converges.
func TestStateWriteBindsCASTokenToBaseBlob(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	orch, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	rel, err := NewReleaseGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	for name, content := range map[string]string{"orchestrate": orch, "release": rel} {
		// (a) The base blob sha is captured from the fetched tip, which is exactly
		// the git blob sha the Contents API `sha` parameter expects.
		assert.Contains(t, content, `BASE_SHA=$(git rev-parse "origin/$BRANCH:$MANIFEST_FILE" 2>/dev/null || true)`,
			"%s must capture the base blob sha right after the reset", name)
		// (b) The PUT's CAS token is the base blob sha, always. A missing base blob
		// refuses the write (see TestStateWriteRefusesUnguardedPutOnMissingBaseBlob)
		// instead of degrading to an sha-less create-or-overwrite PUT.
		assert.Contains(t, content, `-f "sha=$BASE_SHA"`,
			"%s PUT must bind the CAS token to the base blob sha", name)
		assert.NotContains(t, content, `if [[ -n "$BASE_SHA" ]]; then`,
			"%s must not branch into an sha-less PUT when the base blob is missing", name)
		// (c) No decoupled, later current-blob read: that is the clobber.
		assert.NotContains(t, content, "CURRENT_SHA=$(gh api",
			"%s must not read a separate current-blob sha decoupled from the rendered content", name)
		assert.NotContains(t, content, `API_ARGS+=(-f "sha=$CURRENT_SHA")`,
			"%s PUT must not use the decoupled current-blob sha", name)
	}
}

// stateWriteTestConfig returns the minimal manifest config the state-write
// emission tests generate from.
func stateWriteTestConfig(t *testing.T) (*config.TrunkConfig, string) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}, tmpDir
}

// TestStateWriteRefusesUnguardedPutOnMissingBaseBlob asserts the Contents-API
// state write refuses to PUT when the manifest has no blob at the freshly
// fetched trunk tip. An empty BASE_SHA can only mean the manifest is absent
// there (deleted, or the path is wrong): the finalize step exists because that
// same manifest generated it, and the apply function edits the local copy taken
// from that very tip, so there is no legitimate first-write flow. Omitting the
// sha parameter would let the Contents API create or overwrite the file with no
// optimistic lock, the exact unguarded write the internal/statewrite client
// refuses with an empty lock sha. The shell must fail with the real cause
// instead of branching into the weaker write.
func TestStateWriteRefusesUnguardedPutOnMissingBaseBlob(t *testing.T) {
	cfg, tmpDir := stateWriteTestConfig(t)

	orch, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	rel, err := NewReleaseGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	for name, content := range map[string]string{"orchestrate": orch, "release": rel} {
		assert.Contains(t, content, `if [[ -z "$BASE_SHA" ]]; then`,
			"%s must check for a missing base blob before the PUT", name)
		assert.Contains(t, content, "refusing an unguarded state write",
			"%s must state the refusal cause", name)
		assert.NotContains(t, content, `if [[ -n "$BASE_SHA" ]]; then`,
			"%s must not carry the old branch that omits the CAS sha", name)
	}
}

// TestStateWriteFailsFastOnPermanentAPIError asserts the Contents-API retry
// loop classifies PUT failures instead of retrying every shape ten times as
// "likely concurrent run". A 409 is a genuine optimistic-lock conflict and
// stays retryable, and rate limits retry (see
// TestStateWriteRetriesRateLimitedAPIError); any other 4xx (401 revoked
// token, 403 authorization such as a missing bypass, 404 branch or path gone,
// 422 validation) is permanent, so the loop must surface the real cause
// immediately rather than burning the backoff budget and reporting a generic
// exhaustion. Unclassified failures (5xx, transport blips carrying no HTTP
// status) stay retryable, mirroring the transient tolerance of the
// internal/statewrite client.
func TestStateWriteFailsFastOnPermanentAPIError(t *testing.T) {
	cfg, tmpDir := stateWriteTestConfig(t)

	orch, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	rel, err := NewReleaseGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	for name, content := range map[string]string{"orchestrate": orch, "release": rel} {
		// The PUT failure output is captured and classified.
		assert.Contains(t, content, `if API_ERR=$(gh api "${API_ARGS[@]}" 2>&1 >/dev/null); then`,
			"%s must capture the gh api failure output for classification", name)
		// A 409 conflict retries.
		assert.Contains(t, content, `*"HTTP 409"*`,
			"%s must recognize the 409 optimistic-lock conflict as retryable", name)
		// Any other 4xx fails fast with a distinct marker.
		assert.Contains(t, content, `*"HTTP 4"*`,
			"%s must classify non-409 4xx responses", name)
		assert.Contains(t, content, "cascade-state-write: permanent-error",
			"%s must emit the permanent-error marker before failing fast", name)
		// The old blanket mislabel is gone from the API branch.
		assert.NotContains(t, content, "State write attempt $attempt failed (likely concurrent run)",
			"%s must not label every PUT failure as a concurrent run", name)
	}
}

// TestStateWriteRetriesRateLimitedAPIError asserts the Contents-API retry
// loop carves rate-limited responses out of the permanent 4xx arm. GitHub's
// secondary and abuse limits surface as HTTP 403 (with a rate-limit message)
// or HTTP 429, not as auth failures, and are exactly the transient shape the
// bounded backoff loop was sized for (a monorepo wave of components racing
// PUTs to one file). Case arm order matters: the rate-limit arm must sit
// between the 409 arm and the blanket 4xx arm so a rate-limited 403 retries
// while a genuine 401/403-authorization/404/422 still fails fast.
func TestStateWriteRetriesRateLimitedAPIError(t *testing.T) {
	cfg, tmpDir := stateWriteTestConfig(t)

	orch, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	rel, err := NewReleaseGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	for name, content := range map[string]string{"orchestrate": orch, "release": rel} {
		idxConflict := strings.Index(content, `*"HTTP 409"*|*Conflict*)`)
		idxRate := strings.Index(content, `*"HTTP 429"*`)
		idxPermanent := strings.Index(content, `*"HTTP 4"*)`)
		require.GreaterOrEqual(t, idxConflict, 0,
			"%s must keep the 409 optimistic-lock retry arm", name)
		require.GreaterOrEqual(t, idxRate, 0,
			"%s must carry a rate-limit retry arm", name)
		require.GreaterOrEqual(t, idxPermanent, 0,
			"%s must keep the permanent 4xx arm", name)
		assert.Less(t, idxConflict, idxRate,
			"%s must classify the 409 conflict before the rate-limit arm", name)
		assert.Less(t, idxRate, idxPermanent,
			"%s must match rate limits before the blanket 4xx arm so a rate-limited 403 retries", name)
		// The rate-limit arm recognizes every shape GitHub uses for a
		// rate/secondary/abuse limit, not only the 429 status.
		for _, marker := range []string{`*"rate limit"*`, `*"secondary rate"*`, `*"abuse"*`, `*"Retry-After"*`} {
			assert.Contains(t, content, marker,
				"%s rate-limit arm must recognize the %s marker", name, marker)
		}
		assert.Contains(t, content, "rate limited",
			"%s must say the retry is due to a rate limit", name)
	}
}

// TestStateWriteNoEmDash guards the hard project rule that generated output
// contains no em dashes.
func TestStateWriteNoEmDash(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
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
