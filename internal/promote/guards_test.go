package promote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
)

// writeGuardConfig writes a CICDFile to a temp manifest and returns its path.
func writeGuardConfig(t *testing.T, environments []string, state map[string]*config.EnvState) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cicd.yaml")

	cicdFile := &config.CICDFile{
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: environments,
		},
		State: state,
	}
	wrapper := map[string]interface{}{"ci": cicdFile}
	data, err := yaml.Marshal(wrapper)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	return configPath
}

// containsAllOf reports whether s contains every substring in subs.
func containsAllOf(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// allContainAncestor is a stub ancestor function that always reports the
// ancestor is contained in the descendant (every patch is present).
func allContainAncestor(_, _ string) (bool, error) { return true, nil }

// noneContainAncestor is a stub ancestor function that always reports the
// ancestor is missing from the descendant (a patch would be regressed).
func noneContainAncestor(_, _ string) (bool, error) { return false, nil }

// --- Rule 1: refuse promotion FROM a diverged env ---

func TestPromote_FromDivergedEnv_Blocked(t *testing.T) {
	state := map[string]*config.EnvState{
		// test is diverged on an integration branch carrying a hotfix patch.
		"test": {
			SHA:     "mergesha",
			Version: "v1.4.0-rc.2.hotfix.1",
			Ref:     "env/test",
			BaseSHA: "basesha",
			Patches: []string{"patchsha1"},
		},
	}
	configPath := writeGuardConfig(t, []string{"dev", "test", "uat", "prod"}, state)

	p, err := NewPromoter(PromoterOptions{ConfigPath: configPath, DryRun: true, Actor: "test"})
	if err != nil {
		t.Fatalf("NewPromoter: %v", err)
	}

	result, err := p.Promote(ModeDefault, "")
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected promotion FROM diverged env to be blocked, got success")
	}
	if !containsAllOf(result.Error, "test", "patchsha1") {
		t.Errorf("error should name the diverged env and patches, got: %q", result.Error)
	}
}

func TestPromote_FromDivergedEnv_Blocked_Cascade(t *testing.T) {
	state := map[string]*config.EnvState{
		"test": {
			SHA:     "mergesha",
			Version: "v1.4.0-rc.2.hotfix.1",
			Ref:     "env/test",
			BaseSHA: "basesha",
			Patches: []string{"patchsha1"},
		},
	}
	configPath := writeGuardConfig(t, []string{"dev", "test", "uat", "prod"}, state)

	p, err := NewPromoter(PromoterOptions{ConfigPath: configPath, DryRun: true, Actor: "test"})
	if err != nil {
		t.Fatalf("NewPromoter: %v", err)
	}

	result, err := p.Promote(ModeCascade, "test-to-prod")
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected cascade promotion FROM diverged env to be blocked, got success")
	}
	if !containsAllOf(result.Error, "test", "patchsha1") {
		t.Errorf("error should name the diverged env and patches, got: %q", result.Error)
	}
}

// --- Rule 2: promotion INTO a diverged env requires patch containment ---

func TestPromote_IntoDivergedEnv_MissingPatch_Blocked(t *testing.T) {
	// uat is diverged. Promoting dev (which lacks the patch) into uat must fail.
	state := map[string]*config.EnvState{
		"uat": {
			SHA:     "uatmerge",
			Version: "v1.4.0-rc.1.hotfix.1",
			Ref:     "env/uat",
			BaseSHA: "uatbase",
			Patches: []string{"patchsha1"},
		},
		"test": {SHA: "incoming", Version: "v1.5.0-rc.0"},
	}
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: []string{"dev", "test", "uat", "prod"},
		},
		State: state,
	}

	pf := NewPreflighter(PreflighterOptions{
		Config: cfg,
		Mode:   ModeCascade,
		Target: "test-to-uat",
	}, WithAncestorFunc(noneContainAncestor))

	_, err := pf.Run()
	if err == nil {
		t.Fatalf("expected preflight to block promotion into diverged env missing a patch")
	}
	if !containsAllOf(err.Error(), "patchsha1") {
		t.Errorf("error should name the missing patch, got: %q", err.Error())
	}
}

func TestPromote_IntoDivergedEnv_PatchesContained_Allowed(t *testing.T) {
	state := map[string]*config.EnvState{
		"uat": {
			SHA:     "uatmerge",
			Version: "v1.4.0-rc.1.hotfix.1",
			Ref:     "env/uat",
			BaseSHA: "uatbase",
			Patches: []string{"patchsha1"},
		},
		"test": {SHA: "incoming", Version: "v1.5.0-rc.0"},
	}
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: []string{"dev", "test", "uat", "prod"},
		},
		State: state,
	}

	pf := NewPreflighter(PreflighterOptions{
		Config: cfg,
		Mode:   ModeCascade,
		Target: "test-to-uat",
	}, WithAncestorFunc(allContainAncestor))

	result, err := pf.Run()
	if err != nil {
		t.Fatalf("expected promotion with contained patches to be allowed, got: %v", err)
	}
	if !result.CanProceed {
		t.Errorf("expected CanProceed=true when all patches are contained")
	}
}

func TestPromote_IntoDivergedEnv_Force_OverridesWithWarning(t *testing.T) {
	state := map[string]*config.EnvState{
		"uat": {
			SHA:     "uatmerge",
			Version: "v1.4.0-rc.1.hotfix.1",
			Ref:     "env/uat",
			BaseSHA: "uatbase",
			Patches: []string{"patchsha1"},
		},
		"test": {SHA: "incoming", Version: "v1.5.0-rc.0"},
	}
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: []string{"dev", "test", "uat", "prod"},
		},
		State: state,
	}

	pf := NewPreflighter(PreflighterOptions{
		Config: cfg,
		Mode:   ModeCascade,
		Target: "test-to-uat",
		Force:  true,
	}, WithAncestorFunc(noneContainAncestor))

	result, err := pf.Run()
	if err != nil {
		t.Fatalf("force should override the patch-containment block, got: %v", err)
	}
	if result == nil || !result.CanProceed {
		t.Fatalf("expected CanProceed=true under force override")
	}
	if !containsAllOf(strings.Join(result.Warnings, "\n"), "patchsha1") {
		t.Errorf("force override should emit a loud warning naming the regressed patch, got warnings: %v", result.Warnings)
	}
}

// --- Rule 3: publish-path assertion ---

func TestPublish_DivergedSource_Asserts(t *testing.T) {
	// prerelease env (uat) is diverged; advancing to the publish boundary must
	// refuse because a non-trunk SHA must never reach publish.
	state := map[string]*config.EnvState{
		"uat": {
			SHA:     "uatmerge",
			Version: "v1.4.0-rc.2.hotfix.1",
			Ref:     "env/uat",
			BaseSHA: "uatbase",
			Patches: []string{"patchsha1"},
		},
	}
	configPath := writeGuardConfig(t, []string{"dev", "test", "uat", "prod"}, state)

	p, err := NewPromoter(PromoterOptions{ConfigPath: configPath, DryRun: true, Actor: "test"})
	if err != nil {
		t.Fatalf("NewPromoter: %v", err)
	}

	result, err := p.Promote(ModeDefault, "")
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected publish from a diverged source to be asserted/blocked, got success")
	}
	if !containsAllOf(result.Error, "uat") {
		t.Errorf("publish assertion should name the diverged source env, got: %q", result.Error)
	}
}

func TestPublish_DivergedSource_Asserts_NoEnvironment(t *testing.T) {
	// Library/CLI mode: prerelease state is diverged; publish must refuse.
	state := map[string]*config.EnvState{
		"prerelease": {
			SHA:     "mergesha",
			Version: "v1.4.0-rc.2.hotfix.1",
			Ref:     "env/prerelease",
			BaseSHA: "basesha",
			Patches: []string{"patchsha1"},
		},
	}
	configPath := writeGuardConfig(t, nil, state)

	p, err := NewPromoter(PromoterOptions{ConfigPath: configPath, DryRun: true, Actor: "test"})
	if err != nil {
		t.Fatalf("NewPromoter: %v", err)
	}

	result, err := p.Promote(ModeDefault, "")
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected library-mode publish from diverged prerelease to be blocked, got success")
	}
	if !containsAllOf(result.Error, "prerelease") {
		t.Errorf("publish assertion should name the diverged source, got: %q", result.Error)
	}
}

// --- Additivity: a manifest with no divergence fields exercises zero new paths ---

func TestPromote_NoDivergence_GuardsInert(t *testing.T) {
	state := map[string]*config.EnvState{
		"dev":  {SHA: "sha3", Version: "v1.3.0-rc.0"},
		"test": {SHA: "sha2", Version: "v1.2.0-rc.0"},
		"uat":  {SHA: "sha1", Version: "v1.1.0-rc.0"},
	}
	configPath := writeGuardConfig(t, []string{"dev", "test", "uat", "prod"}, state)

	p, err := NewPromoter(PromoterOptions{ConfigPath: configPath, DryRun: true, Actor: "test"})
	if err != nil {
		t.Fatalf("NewPromoter: %v", err)
	}

	result, err := p.Promote(ModeDefault, "")
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("non-diverged manifest must promote normally, got error: %q", result.Error)
	}
}
