package generate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

func strptr(s string) *string { return &s }

// promoteMultiComponentConfig returns a two-component, multi-environment manifest
// suitable for exercising the promote fan-out. It has no builds or deploys, so
// generation reads no stub files from disk.
func promoteMultiComponentConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Components: map[string]config.ComponentConfig{
			"api": {Path: "services/api", TagGrammar: &config.TagGrammarConfig{Prefix: strptr("api-")}},
			"web": {Path: "services/web", TagGrammar: &config.TagGrammarConfig{Prefix: strptr("web-")}},
		},
	}
}

// TestPromoteTargets_SingleComponent_ByteIdentical proves a manifest with no
// components: block takes the untouched single-generator path: exactly one target
// at the output path whose content is byte-identical to a directly built promote
// generator, with no component-namespaced name and no --component flag.
func TestPromoteTargets_SingleComponent_ByteIdentical(t *testing.T) {
	cfg := &config.TrunkConfig{TrunkBranch: "main", Environments: config.EnvNames("dev", "prod")}

	targets, err := promoteTargets(cfg, "", ".github/workflows/promote.yaml", nil)
	if err != nil {
		t.Fatalf("promoteTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Path != ".github/workflows/promote.yaml" {
		t.Errorf("path = %q, want .github/workflows/promote.yaml", targets[0].Path)
	}

	got, err := targets[0].Gen.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want, err := NewPromoteGenerator(cfg, "").Generate()
	if err != nil {
		t.Fatalf("baseline Generate: %v", err)
	}
	if got != want {
		t.Errorf("single-component promote output drifted from baseline generator")
	}
	if strings.Contains(got, "Promote (") {
		t.Errorf("single-component promote output must not carry a component-namespaced name")
	}
	if strings.Contains(got, "--component") {
		t.Errorf("single-component promote output must not carry a --component flag")
	}
}

// TestPromoteTargets_Components_FanOut proves a components: manifest fans out one
// promote-<name>.yaml per component (sorted), no repo-wide promote.yaml, each
// namespaced, each scoped with its own --component and no sibling's.
func TestPromoteTargets_Components_FanOut(t *testing.T) {
	cfg := promoteMultiComponentConfig()

	targets, err := promoteTargets(cfg, "", ".github/workflows/promote.yaml", nil)
	if err != nil {
		t.Fatalf("promoteTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}

	byPath := map[string]string{}
	for _, tg := range targets {
		content, gerr := tg.Gen.Generate()
		if gerr != nil {
			t.Fatalf("Generate(%s): %v", tg.Path, gerr)
		}
		byPath[tg.Path] = content
	}

	api, ok := byPath[".github/workflows/promote-api.yaml"]
	if !ok {
		t.Fatalf("missing promote-api.yaml; got paths %v", keys(byPath))
	}
	web, ok := byPath[".github/workflows/promote-web.yaml"]
	if !ok {
		t.Fatalf("missing promote-web.yaml; got paths %v", keys(byPath))
	}
	if _, ok := byPath[".github/workflows/promote.yaml"]; ok {
		t.Errorf("repo-wide promote.yaml must not be emitted when components are declared")
	}

	// Naming: each file is namespaced to its component.
	if !strings.Contains(api, "name: Promote (api)") {
		t.Errorf("api promote workflow missing namespaced name")
	}
	if !strings.Contains(web, "name: Promote (web)") {
		t.Errorf("web promote workflow missing namespaced name")
	}

	// Component scoping: each workflow passes its own --component on the promotion
	// CLI steps so state is recorded under that component's subtree.
	if !strings.Contains(api, "--component api") {
		t.Errorf("api promote workflow missing --component api")
	}
	if strings.Contains(api, "--component web") {
		t.Errorf("api promote workflow must not carry web's --component (isolation)")
	}
	if !strings.Contains(web, "--component web") {
		t.Errorf("web promote workflow missing --component web")
	}
	// --component must land on both the preflight and finalize invocations.
	if got := strings.Count(api, "--component api"); got < 2 {
		t.Errorf("api promote workflow emitted --component api %d times, want >= 2 (preflight and finalize)", got)
	}
}

// TestPromoteTargets_ConcurrencyIsolation proves each component's promote group
// carries the component identity, lives in the promote namespace (never the
// orchestrate lane, Trap A), and no two components share a lane.
func TestPromoteTargets_ConcurrencyIsolation(t *testing.T) {
	cfg := promoteMultiComponentConfig()

	targets, err := promoteTargets(cfg, "", ".github/workflows/promote.yaml", nil)
	if err != nil {
		t.Fatalf("promoteTargets: %v", err)
	}

	byPath := map[string]string{}
	for _, tg := range targets {
		content, gerr := tg.Gen.Generate()
		if gerr != nil {
			t.Fatalf("Generate(%s): %v", tg.Path, gerr)
		}
		byPath[tg.Path] = content
	}
	api := byPath[".github/workflows/promote-api.yaml"]
	web := byPath[".github/workflows/promote-web.yaml"]

	apiGroup := concurrencyGroupLine(t, api)
	webGroup := concurrencyGroupLine(t, web)

	if apiGroup != "  group: promote-api" {
		t.Errorf("api promote group = %q, want %q", apiGroup, "  group: promote-api")
	}
	if webGroup != "  group: promote-web" {
		t.Errorf("web promote group = %q, want %q", webGroup, "  group: promote-web")
	}
	// Trap A: the promote group must never reuse the orchestrate lane, which
	// GitHub would serialize against repo-globally.
	if strings.Contains(apiGroup, "orchestrate-") {
		t.Errorf("api promote group must not reuse the orchestrate lane: %q", apiGroup)
	}
	// Isolation: a component's promote group must not carry a sibling's identity.
	if strings.Contains(apiGroup, "web") {
		t.Errorf("api promote group must not carry web's identity: %q", apiGroup)
	}
	if apiGroup == webGroup {
		t.Errorf("api and web promote groups must differ; both are %q", apiGroup)
	}
}

// TestPromoteTargets_GlobalConcurrencyGroupDoesNotCollapse proves Trap B: a
// manifest-global concurrency.group does not collapse every component's promote
// onto one literal lane. The component identity is still composed into each
// group, so the groups differ and neither is the bare global literal.
func TestPromoteTargets_GlobalConcurrencyGroupDoesNotCollapse(t *testing.T) {
	cfg := promoteMultiComponentConfig()
	cfg.Concurrency = &config.ConcurrencyConfig{Group: "shared-lane"}

	targets, err := promoteTargets(cfg, "", ".github/workflows/promote.yaml", nil)
	if err != nil {
		t.Fatalf("promoteTargets: %v", err)
	}

	byPath := map[string]string{}
	for _, tg := range targets {
		content, gerr := tg.Gen.Generate()
		if gerr != nil {
			t.Fatalf("Generate(%s): %v", tg.Path, gerr)
		}
		byPath[tg.Path] = content
	}
	apiGroup := concurrencyGroupLine(t, byPath[".github/workflows/promote-api.yaml"])
	webGroup := concurrencyGroupLine(t, byPath[".github/workflows/promote-web.yaml"])

	if apiGroup == "  group: shared-lane" {
		t.Errorf("api promote group collapsed onto the bare global literal: %q", apiGroup)
	}
	if apiGroup == webGroup {
		t.Errorf("global concurrency.group collapsed both components onto one lane: %q", apiGroup)
	}
	// The component identity must remain present so isolation holds.
	if !strings.Contains(apiGroup, "promote-api") {
		t.Errorf("api promote group must retain the component identity: %q", apiGroup)
	}
	if !strings.Contains(webGroup, "promote-web") {
		t.Errorf("web promote group must retain the component identity: %q", webGroup)
	}
}

// TestPromoteConcurrencyGroup_DistinctFromOrchestrate proves the promote and
// orchestrate concurrency namespaces never collide for the same component, so a
// promote run and an orchestrate run for one component never serialize against
// each other on a shared repo-global lane.
func TestPromoteConcurrencyGroup_DistinctFromOrchestrate(t *testing.T) {
	const name = "api"
	promote := config.PromoteConcurrencyGroup(name)
	orchestrate := config.ComponentConcurrencyGroup(name)
	if promote == orchestrate {
		t.Fatalf("promote and orchestrate groups must differ for %q; both are %q", name, promote)
	}
	if !strings.HasPrefix(promote, "promote-") {
		t.Errorf("promote group %q must live in the promote- namespace", promote)
	}
}
