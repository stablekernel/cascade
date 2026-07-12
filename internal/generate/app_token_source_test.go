package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReleaseGenerator_NoAppTokenByDefault asserts the OFF-state: with no App
// token source configured, no minting step and no minted-token fallback ref are
// emitted, so generated output stays exactly as before.
func TestReleaseGenerator_NoAppTokenByDefault(t *testing.T) {
	cfg := &config.TrunkConfig{TrunkBranch: "main", Environments: config.EnvNames("prod")}
	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assert.NotContains(t, content, "create-github-app-token")
	assert.NotContains(t, content, "cascade-release-app-token")
	assert.NotContains(t, content, "cascade-state-app-token")
	assert.NotContains(t, content, ".outputs.token ||")
}

// TestReleaseGenerator_ReleaseAppTokenMints asserts that a configured release App
// identity injects the minting step into the release-consuming jobs, guards it to
// real GitHub, and rewires the consuming refs to the minted-token fallback while
// keeping the static GITHUB_TOKEN as the act/gitea fallback.
func TestReleaseGenerator_ReleaseAppTokenMints(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
		ReleaseTokenApp: &config.AppTokenSource{
			AppID:      "CASCADE_APP_ID",
			PrivateKey: "CASCADE_APP_PRIVATE_KEY",
		},
	}
	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "- name: Mint release app token")
	assert.Contains(t, content, "id: cascade-release-app-token")
	assert.Contains(t, content, "if: ${{ github.server_url == 'https://github.com' }}")
	assert.Contains(t, content, "uses: actions/create-github-app-token@")
	assert.Contains(t, content, "app-id: ${{ secrets.CASCADE_APP_ID }}")
	assert.Contains(t, content, "private-key: ${{ secrets.CASCADE_APP_PRIVATE_KEY }}")

	// Consuming refs prefer the minted token, with the static token as fallback.
	assert.Contains(t, content,
		"${{ steps.cascade-release-app-token.outputs.token || secrets.GITHUB_TOKEN }}")

	// No state mint step is injected when only the release seam is App-backed.
	assert.NotContains(t, content, "cascade-state-app-token")
}

// TestReleaseGenerator_StateAppTokenMints asserts the state seam mints its own
// step with the distinct state id in the finalize job.
func TestReleaseGenerator_StateAppTokenMints(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
		StateTokenApp: &config.AppTokenSource{
			AppID:      "CASCADE_APP_ID",
			PrivateKey: "CASCADE_APP_PRIVATE_KEY",
		},
	}
	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "- name: Mint state app token")
	assert.Contains(t, content, "id: cascade-state-app-token")
	assert.Contains(t, content, "if: ${{ github.server_url == 'https://github.com' }}")
	assert.Contains(t, content,
		"${{ steps.cascade-state-app-token.outputs.token || secrets.GITHUB_TOKEN }}")
}

// TestMintStep_FallbackPrefersConfiguredStaticToken asserts the fallback inlines
// a configured static token expression, not just GITHUB_TOKEN.
func TestMintStep_FallbackPrefersConfiguredStaticToken(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
		ReleaseToken: "MY_RELEASE_PAT",
		ReleaseTokenApp: &config.AppTokenSource{
			AppID:      "CASCADE_APP_ID",
			PrivateKey: "CASCADE_APP_PRIVATE_KEY",
		},
	}
	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assert.Contains(t, content,
		"${{ steps.cascade-release-app-token.outputs.token || secrets.MY_RELEASE_PAT }}")
}

// TestResolveTokenRef_OffStateIdentity asserts the resolver returns today's
// static expression byte-for-byte when no App source is set.
func TestResolveTokenRef_OffStateIdentity(t *testing.T) {
	cfg := &config.TrunkConfig{TrunkBranch: "main"}
	assert.Equal(t, cfg.GetReleaseToken(), resolveReleaseTokenRef(cfg))
	assert.Equal(t, cfg.GetStateToken(), resolveStateTokenRef(cfg))
}

// TestReleaseGenerator_ReleaseTokenDefaultsToStateToken asserts that when an
// adopter configures a trigger-capable state_token but leaves release_token
// unset, the rc-creating release steps reference the state token, not the bare
// GITHUB_TOKEN. This is the rc-to-release dead-chain fix: a tag created with
// GITHUB_TOKEN fires no downstream workflows, so the release token must inherit
// the trigger-capable state token the adopter already supplied.
func TestReleaseGenerator_ReleaseTokenDefaultsToStateToken(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
		StateToken:   "${{ secrets.CASCADE_BOT_TOKEN }}",
	}
	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)

	// Release operations reference the state token, not GITHUB_TOKEN.
	assert.Contains(t, content, "token: ${{ secrets.CASCADE_BOT_TOKEN }}")
	assert.NotContains(t, content, "token: ${{ secrets.GITHUB_TOKEN }}")
}

// TestReleaseGenerator_BothTokensUnsetKeepsGithubToken asserts the OFF/back-
// compat state: with neither release_token nor state_token set, release steps
// keep emitting the historical GITHUB_TOKEN default byte-for-byte.
func TestReleaseGenerator_BothTokensUnsetKeepsGithubToken(t *testing.T) {
	cfg := &config.TrunkConfig{TrunkBranch: "main", Environments: config.EnvNames("prod")}
	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "token: ${{ secrets.GITHUB_TOKEN }}")
	assert.NotContains(t, content, "CASCADE_BOT_TOKEN")
}

// TestMintStepIndentation asserts the minting step is emitted at the standard
// 6-space step indent so it nests correctly under the job's steps: block.
func TestMintStepIndentation(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
		ReleaseTokenApp: &config.AppTokenSource{
			AppID:      "CASCADE_APP_ID",
			PrivateKey: "CASCADE_APP_PRIVATE_KEY",
		},
	}
	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)
	assert.True(t, strings.Contains(content, "\n      - name: Mint release app token\n"),
		"mint step must be emitted at the 6-space step indent")
}

// TestAppTokenSource_Actionlint runs actionlint over a release workflow that has
// both seams App-backed, proving the injected minting steps and the rewired
// token refs are valid GitHub Actions YAML. Skipped when actionlint is absent so
// the unit suite stays hermetic.
func TestAppTokenSource_Actionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
		ReleaseTokenApp: &config.AppTokenSource{
			AppID:      "CASCADE_APP_ID",
			PrivateKey: "CASCADE_APP_PRIVATE_KEY",
		},
		StateTokenApp: &config.AppTokenSource{
			AppID:      "CASCADE_APP_ID",
			PrivateKey: "CASCADE_APP_PRIVATE_KEY",
		},
	}
	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	wfPath := filepath.Join(wfDir, "cascade-release.yaml")
	require.NoError(t, os.WriteFile(wfPath, []byte(content), 0644))

	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = dir
	require.NoError(t, gitInit.Run(), "git init for actionlint project root")
	writeReusableWorkflowStubs(t, dir, content)

	// Disable shellcheck: inline run: bodies trip style nits orthogonal to the
	// minting step and token-ref structure this test governs.
	cmd := exec.Command(bin, "-shellcheck=", wfPath)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	assert.NoError(t, runErr, "actionlint reported issues:\n%s", string(out))
}
