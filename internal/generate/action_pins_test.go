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

// thirdPartyActions is every marketplace action cascade emits and governs with
// the pin policy. The test asserts the policy is applied to each of them.
var thirdPartyActions = []string{
	actionCheckout,
	actionGithubScript,
	actionDownloadArtifact,
	actionUploadArtifact,
}

func TestActionRef_DefaultTagModeUnchanged(t *testing.T) {
	// Nil pin_mode and empty config = today's behavior: explicit major tags,
	// never @latest, for every third-party action.
	cfg := &config.TrunkConfig{}

	for _, action := range thirdPartyActions {
		ref := actionRef(cfg, action)
		pin := defaultActionPins[action]
		assert.Equalf(t, action+"@"+pin.tag, ref, "tag mode ref for %s", action)
		assert.NotContains(t, ref, "@latest", "%s must never emit @latest", action)
		assert.NotContains(t, ref, "#", "tag mode must not emit a SHA comment for %s", action)
	}
}

func TestActionRef_ExplicitTagModeMatchesDefault(t *testing.T) {
	// Setting pin_mode: tag explicitly is identical to the default.
	implicit := &config.TrunkConfig{}
	explicit := &config.TrunkConfig{PinMode: config.PinModeTag}

	for _, action := range thirdPartyActions {
		assert.Equalf(t, actionRef(implicit, action), actionRef(explicit, action),
			"explicit tag mode must match default for %s", action)
	}
}

func TestActionRef_SHAModePinsEveryActionWithComment(t *testing.T) {
	cfg := &config.TrunkConfig{PinMode: config.PinModeSHA}

	for _, action := range thirdPartyActions {
		ref := actionRef(cfg, action)
		pin := defaultActionPins[action]

		// <action>@<40-hex-sha> # <version>
		require.Containsf(t, ref, "@"+pin.sha, "sha mode ref for %s", action)
		assert.Lenf(t, pin.sha, 40, "%s pin must be a 40-char commit SHA", action)
		assert.Containsf(t, ref, "# "+pin.shaVersion, "sha mode must annotate %s with its version", action)
		assert.NotContainsf(t, ref, "@v", "sha mode must not emit a mutable tag for %s", action)
	}
}

func TestActionRef_ActionPinsOverrideRegardlessOfMode(t *testing.T) {
	const forkSHA = "00112233445566778899aabbccddeeff00112233"

	// Override applies in tag mode.
	tagCfg := &config.TrunkConfig{
		PinMode:    config.PinModeTag,
		ActionPins: map[string]string{actionCheckout: forkSHA},
	}
	assert.Equal(t, actionCheckout+"@"+forkSHA, actionRef(tagCfg, actionCheckout))
	// Non-overridden actions keep their tag-mode default.
	assert.Equal(t, actionUploadArtifact+"@"+defaultActionPins[actionUploadArtifact].tag,
		actionRef(tagCfg, actionUploadArtifact))

	// Override also wins over sha-mode resolution.
	shaCfg := &config.TrunkConfig{
		PinMode:    config.PinModeSHA,
		ActionPins: map[string]string{actionCheckout: "v4.2.2"},
	}
	assert.Equal(t, actionCheckout+"@v4.2.2", actionRef(shaCfg, actionCheckout))
}

func TestActionRef_EmptyOverrideFallsThrough(t *testing.T) {
	cfg := &config.TrunkConfig{ActionPins: map[string]string{actionCheckout: ""}}
	// An empty override value does not suppress the default ref.
	assert.Equal(t, actionCheckout+"@"+defaultActionPins[actionCheckout].tag, actionRef(cfg, actionCheckout))
}

func TestActionRef_UnknownActionPassthrough(t *testing.T) {
	cfg := &config.TrunkConfig{PinMode: config.PinModeSHA}
	// An action not in the built-in table and not overridden is returned
	// verbatim so callers may pass an already-qualified ref.
	assert.Equal(t, "some/other-action@v1", actionRef(cfg, "some/other-action@v1"))
}

func TestActionRef_NilConfigSafe(t *testing.T) {
	// GetPinMode tolerates nil; actionRef should too and fall back to tag mode.
	assert.Equal(t, actionCheckout+"@"+defaultActionPins[actionCheckout].tag, actionRef(nil, actionCheckout))
}

func TestCLISetupRef_NilConfigSafe(t *testing.T) {
	// actionRef already tolerates a nil config (see TestActionRef_NilConfigSafe);
	// cliSetupRef sits behind the same call sites and must share that contract
	// instead of panicking on a nil dereference. A nil config degrades to the
	// immutable default CLI version, the same ref a zero-value config yields.
	assert.NotPanics(t, func() { cliSetupRef(nil) })
	assert.Equal(t, config.DefaultCLIVersion, cliSetupRef(nil))
}

// TestCLISetupRef covers the self-action ref resolution: the version tag in tag
// mode (today's behavior), the 40-hex commit SHA with a trailing version comment
// when pin_mode is sha and cli_version_sha is set, graceful fallback to the tag
// when the SHA is absent, and the beta -> master escape hatch.
//
// There is no act+gitea e2e scenario for this feature. The e2e harness localizes
// cross-repo self-references (stablekernel/cascade/.github/actions/setup-cli@<ref>
// -> ./.github/actions/setup-cli) before running act, so the pinned ref is erased
// before the workflow executes and no in-container assertion can observe it. Unit
// coverage here (cliSetupRef) plus the regenerated cascade orchestrate.yaml /
// promote.yaml showing @9dc69a1f... # v0.1.0 are the proof of correctness.
func TestCLISetupRef(t *testing.T) {
	const sha = "9dc69a1f66753a3865c38c34eca5a931f677c803"

	tests := []struct {
		name string
		cfg  *config.TrunkConfig
		want string
	}{
		{
			name: "tag default when cli_version unset",
			cfg:  &config.TrunkConfig{},
			want: config.DefaultCLIVersion,
		},
		{
			name: "explicit tag in tag mode",
			cfg:  &config.TrunkConfig{CLIVersion: "v1.2.3", PinMode: config.PinModeTag},
			want: "v1.2.3",
		},
		{
			name: "sha mode with sha set emits sha and version comment",
			cfg:  &config.TrunkConfig{CLIVersion: "v1.2.3", PinMode: config.PinModeSHA, CLIVersionSHA: sha},
			want: sha + " # v1.2.3",
		},
		{
			name: "sha mode without sha falls back to tag",
			cfg:  &config.TrunkConfig{CLIVersion: "v1.2.3", PinMode: config.PinModeSHA},
			want: "v1.2.3",
		},
		{
			name: "tag mode ignores a set sha",
			cfg:  &config.TrunkConfig{CLIVersion: "v1.2.3", PinMode: config.PinModeTag, CLIVersionSHA: sha},
			want: "v1.2.3",
		},
		{
			name: "beta opts into master regardless of pin mode",
			cfg:  &config.TrunkConfig{CLIVersion: "beta", PinMode: config.PinModeSHA, CLIVersionSHA: sha},
			want: "master",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cliSetupRef(tt.cfg))
		})
	}
}

// TestPinPolicy_E2E_SelfActionSHAPinned asserts that when pin_mode is sha and
// cli_version_sha is set, every generated setup-cli self-action ref in the
// orchestrate output is pinned to the 40-hex commit with cli_version carried as a
// trailing comment, and the bare version-tag uses: ref no longer appears. This is
// the generator-level end-to-end proof; the act+gitea harness cannot observe this
// because it localizes the cross-repo self-ref before running act (see TestCLISetupRef
// for a full explanation).
func TestPinPolicy_E2E_SelfActionSHAPinned(t *testing.T) {
	const sha = "9dc69a1f66753a3865c38c34eca5a931f677c803"
	cfg, tmpDir := newPinE2EConfig(t)
	cfg.CLIVersion = "v0.1.0"
	cfg.CLIVersionSHA = sha

	out, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	const action = "stablekernel/cascade/.github/actions/setup-cli"
	assert.Contains(t, out, action+"@"+sha+" # v0.1.0",
		"self-action must be SHA-pinned with a version comment")
	assert.NotContains(t, out, "uses: "+action+"@v0.1.0\n",
		"the bare version tag must not remain as a uses: ref once SHA-pinned")
}

// newPinE2EConfig builds a manifest exercising the orchestrate, build, deploy,
// and release emission paths so the generated workflow contains every
// third-party uses: line the pin policy governs.
func newPinE2EConfig(t *testing.T) (*config.TrunkConfig, string) {
	t.Helper()
	tmpDir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0o755))
	buildWorkflow := `
name: Build App
on:
  workflow_call:
    inputs:
      sha:
        type: string
    outputs:
      image_tag:
        value: ${{ jobs.build.outputs.image_tag }}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0o644))
	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    inputs:
      app_image_tag:
        type: string
      environment:
        type: string
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/deploy.yaml"), []byte(deployWorkflow), 0o644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		PinMode:      config.PinModeSHA,
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "services", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"deploy/**"}, DependsOn: []string{"app"}},
		},
	}
	return cfg, tmpDir
}

// TestPinPolicy_E2E_OrchestrateEmitsPinnedRefs asserts that under pin_mode: sha
// every third-party uses: line in the generated orchestrate workflow is pinned
// to a 40-char SHA with a version comment, while cascade's own setup-cli action
// ref stays version-tag based (excluded from SHA pinning).
func TestPinPolicy_E2E_OrchestrateEmitsPinnedRefs(t *testing.T) {
	cfg, tmpDir := newPinE2EConfig(t)

	out, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	// No mutable third-party tag survives. cascade's own setup-cli ref is
	// intentionally excluded from SHA pinning and carries its version tag.
	for _, action := range thirdPartyActions {
		assert.NotContainsf(t, out, "uses: "+action+"@v", "third-party %s must be SHA-pinned, not tagged", action)
		assert.NotContainsf(t, out, action+"@latest", "third-party %s must never be @latest", action)
	}

	// Every third-party uses: line that names a governed action carries a SHA.
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- uses:") && !strings.HasPrefix(trimmed, "uses:") {
			continue
		}
		for _, action := range thirdPartyActions {
			if strings.Contains(trimmed, action+"@") {
				pin := defaultActionPins[action]
				assert.Containsf(t, trimmed, "@"+pin.sha, "line must pin %s by SHA: %q", action, trimmed)
				assert.Containsf(t, trimmed, "# "+pin.shaVersion, "line must annotate %s: %q", action, trimmed)
			}
		}
	}

	// cascade's own action is NOT SHA-pinned: it stays on the version ref.
	assert.Contains(t, out, "stablekernel/cascade/.github/actions/setup-cli@",
		"cascade-owned action ref must remain present")
	assert.NotContains(t, out, "stablekernel/cascade/.github/actions/setup-cli@"+defaultActionPins[actionCheckout].sha,
		"cascade-owned action must not be SHA-pinned by the third-party pin table")
}

// TestGetCLIRef_DefaultIsPinnedNotMutable asserts that, under a default manifest
// with cli_version unset, the generated self-action ref is the immutable release
// tag (config.DefaultCLIVersion) and never the mutable @latest or @master refs.
// SHA-pinning the self-action is not done at generation time; the version tag is
// the supply-chain pin.
func TestGetCLIRef_DefaultIsPinnedNotMutable(t *testing.T) {
	cfg, tmpDir := newPinE2EConfig(t)
	cfg.PinMode = "" // default manifest, no pin config
	// cli_version intentionally unset.

	out, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	const action = "stablekernel/cascade/.github/actions/setup-cli"
	assert.Contains(t, out, action+"@"+config.DefaultCLIVersion,
		"self-action must be pinned to the immutable release tag by default")
	assert.NotContains(t, out, action+"@latest",
		"self-action default ref must not be the mutable @latest")
	assert.NotContains(t, out, action+"@master",
		"self-action default ref must not be the mutable @master")
}

// TestPinPolicy_E2E_Deterministic confirms re-generating against the same pin
// config yields byte-identical output (no network, table-driven).
func TestPinPolicy_E2E_Deterministic(t *testing.T) {
	cfg, tmpDir := newPinE2EConfig(t)

	first, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	second, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	assert.Equal(t, first, second, "pinned generation must be deterministic")
}

// TestPinPolicy_E2E_TagModeDefaultUnchanged confirms an existing manifest with
// no pin config still emits the explicit tag refs (non-breaking default).
func TestPinPolicy_E2E_TagModeDefaultUnchanged(t *testing.T) {
	cfg, tmpDir := newPinE2EConfig(t)
	cfg.PinMode = "" // simulate a pre-pinning manifest

	out, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, out, "uses: "+actionCheckout+"@"+defaultActionPins[actionCheckout].tag)
	for _, action := range thirdPartyActions {
		assert.NotContainsf(t, out, action+"@"+defaultActionPins[action].sha,
			"tag mode must not emit a SHA for %s", action)
	}
}

// TestActionRef_OverridePreservesVersionComment locks the seam a reconciled sha
// adoption relies on: an action_pins override already emits verbatim as
// "<action>@<override>", so writing "<sha> # <version>" into action_pins keeps
// the version comment with no generator change. Charset validation (see
// validateActionPins in internal/config) is what makes this raw splice safe.
func TestActionRef_OverridePreservesVersionComment(t *testing.T) {
	cfg := &config.TrunkConfig{ActionPins: map[string]string{
		"actions/checkout": "abc123def4567890abc123def4567890abc12345 # v5.1.0",
	}}
	require.Equal(t,
		"actions/checkout@abc123def4567890abc123def4567890abc12345 # v5.1.0",
		actionRef(cfg, "actions/checkout"))
}
