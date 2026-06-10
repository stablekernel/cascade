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
	// intentionally excluded and may still carry @latest (its CLI version).
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
