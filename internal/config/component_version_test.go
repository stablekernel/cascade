package config

import "testing"

func twoComponentTrunk() *TrunkConfig {
	return &TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Components: map[string]ComponentConfig{
			"api": {Path: "services/api", TagPrefix: "api-"},
			"web": {Path: "services/web", TagPrefix: "web-"},
		},
	}
}

func TestGetComponentTagPrefix(t *testing.T) {
	cfg := twoComponentTrunk()

	got, err := cfg.GetComponentTagPrefix("api")
	if err != nil {
		t.Fatalf("GetComponentTagPrefix(api): %v", err)
	}
	if got != "api-" {
		t.Errorf("GetComponentTagPrefix(api) = %q, want %q", got, "api-")
	}

	if _, err := cfg.GetComponentTagPrefix("missing"); err == nil {
		t.Errorf("GetComponentTagPrefix(missing): expected error, got nil")
	}
}

// TestResolvedComponent_TagGrammarSpec_ForcesStrictPrefix proves a component's
// derived grammar carries its own prefix AND forces StrictPrefix true, so the
// component reads only its own tag namespace even when no tag_grammar block set
// strict_prefix. This is the HLD Section 5 isolation invariant.
func TestResolvedComponent_TagGrammarSpec_ForcesStrictPrefix(t *testing.T) {
	cfg := twoComponentTrunk()

	resolved, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}

	spec := resolved.TagGrammarSpec()
	if spec.Prefix != "api-" {
		t.Errorf("spec.Prefix = %q, want %q", spec.Prefix, "api-")
	}
	if !spec.StrictPrefix {
		t.Errorf("spec.StrictPrefix = false, want true (component must read strictly)")
	}

	// The strict api- grammar accepts its own tags and rejects a sibling's.
	if !spec.IsVersionTag("api-1.2.3") {
		t.Errorf("strict api- spec must accept api-1.2.3")
	}
	if spec.IsVersionTag("web-1.2.3") {
		t.Errorf("strict api- spec must reject web-1.2.3 (namespace isolation)")
	}
}
