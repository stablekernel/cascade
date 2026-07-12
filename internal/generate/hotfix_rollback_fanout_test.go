package generate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// lifecycleMultiComponentConfig returns a two-component, multi-environment
// manifest suitable for exercising the hotfix and rollback fan-out. It has no
// builds or deploys, so generation reads no stub files from disk.
func lifecycleMultiComponentConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Components: map[string]config.ComponentConfig{
			"api": {Path: "services/api", TagGrammar: &config.TagGrammarConfig{Prefix: strptr("api-")}},
			"web": {Path: "services/web", TagGrammar: &config.TagGrammarConfig{Prefix: strptr("web-")}},
		},
	}
}

// TestHotfixTargets_SingleComponent_ByteIdentical proves a manifest with no
// components: block takes the untouched single-generator path: exactly one target
// at the output path whose content is byte-identical to a directly built hotfix
// generator, with no component-namespaced name and no --component flag.
func TestHotfixTargets_SingleComponent_ByteIdentical(t *testing.T) {
	cfg := &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev", "prod"}}

	targets, err := hotfixTargets(cfg, "")
	if err != nil {
		t.Fatalf("hotfixTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Path != ".github/workflows/cascade-hotfix.yaml" {
		t.Errorf("path = %q, want .github/workflows/cascade-hotfix.yaml", targets[0].Path)
	}

	got, err := targets[0].Gen.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want, err := NewHotfixGenerator(cfg, "").Generate()
	if err != nil {
		t.Fatalf("baseline Generate: %v", err)
	}
	if got != want {
		t.Errorf("single-component hotfix output drifted from baseline generator")
	}
	if strings.Contains(got, "Cascade Hotfix (") {
		t.Errorf("single-component hotfix output must not carry a component-namespaced name")
	}
	if strings.Contains(got, "--component") {
		t.Errorf("single-component hotfix output must not carry a --component flag")
	}
	if strings.Contains(got, "state.components.") {
		t.Errorf("single-component hotfix output must read the flat state.<env> path, not state.components.<name>")
	}
}

// TestHotfixTargets_Components_FanOut proves a components: manifest fans out one
// cascade-hotfix-<name>.yaml per component (sorted), no repo-wide
// cascade-hotfix.yaml, each namespaced, each scoped with its own --component and
// no sibling's, and each reading its own component state subtree.
func TestHotfixTargets_Components_FanOut(t *testing.T) {
	cfg := lifecycleMultiComponentConfig()

	targets, err := hotfixTargets(cfg, "")
	if err != nil {
		t.Fatalf("hotfixTargets: %v", err)
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

	api, ok := byPath[".github/workflows/cascade-hotfix-api.yaml"]
	if !ok {
		t.Fatalf("missing cascade-hotfix-api.yaml; got paths %v", keys(byPath))
	}
	web, ok := byPath[".github/workflows/cascade-hotfix-web.yaml"]
	if !ok {
		t.Fatalf("missing cascade-hotfix-web.yaml; got paths %v", keys(byPath))
	}
	if _, ok := byPath[".github/workflows/cascade-hotfix.yaml"]; ok {
		t.Errorf("repo-wide cascade-hotfix.yaml must not be emitted when components are declared")
	}

	// Naming: each file is namespaced to its component.
	if !strings.Contains(api, "name: Cascade Hotfix (api)") {
		t.Errorf("api hotfix workflow missing namespaced name")
	}
	if !strings.Contains(web, "name: Cascade Hotfix (web)") {
		t.Errorf("web hotfix workflow missing namespaced name")
	}

	// Component scoping: each workflow passes its own --component on the hotfix
	// CLI steps (plan and finalize) so state is recorded under that component's
	// subtree.
	if got := strings.Count(api, "--component api"); got < 2 {
		t.Errorf("api hotfix workflow emitted --component api %d times, want >= 2 (plan and finalize)", got)
	}
	if strings.Contains(api, "--component web") {
		t.Errorf("api hotfix workflow must not carry web's --component (isolation)")
	}
	if !strings.Contains(web, "--component web") {
		t.Errorf("web hotfix workflow missing --component web")
	}

	// The context job's raw manifest read of the rollback (N-1) SHA must resolve
	// the component's own state subtree, not the flat state.<env> node.
	if !strings.Contains(api, "state.components.api.") {
		t.Errorf("api hotfix context job must read state.components.api.<env>")
	}
	if strings.Contains(api, "state.components.web.") {
		t.Errorf("api hotfix context job must not read web's state subtree (isolation)")
	}

	// Concurrency isolation: each component's group carries its own identity and
	// no two components share a lane.
	apiGroup := concurrencyGroupLine(t, api)
	webGroup := concurrencyGroupLine(t, web)
	if apiGroup == webGroup {
		t.Errorf("api and web hotfix concurrency groups must differ; both are %q", apiGroup)
	}
	if !strings.Contains(apiGroup, "api") {
		t.Errorf("api hotfix concurrency group must carry the component identity: %q", apiGroup)
	}
	if strings.Contains(apiGroup, "web") {
		t.Errorf("api hotfix concurrency group must not carry web's identity: %q", apiGroup)
	}
}

// TestRollbackTargets_SingleComponent_ByteIdentical proves a manifest with no
// components: block takes the untouched single-generator path, byte-identical to
// a directly built rollback generator, with no namespaced name and no --component.
func TestRollbackTargets_SingleComponent_ByteIdentical(t *testing.T) {
	cfg := &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev", "prod"}}

	targets, err := rollbackTargets(cfg, "")
	if err != nil {
		t.Fatalf("rollbackTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Path != ".github/workflows/cascade-rollback.yaml" {
		t.Errorf("path = %q, want .github/workflows/cascade-rollback.yaml", targets[0].Path)
	}

	got, err := targets[0].Gen.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want, err := NewRollbackGenerator(cfg, "").Generate()
	if err != nil {
		t.Fatalf("baseline Generate: %v", err)
	}
	if got != want {
		t.Errorf("single-component rollback output drifted from baseline generator")
	}
	if strings.Contains(got, "name: Rollback (") {
		t.Errorf("single-component rollback output must not carry a component-namespaced name")
	}
	if strings.Contains(got, "--component") {
		t.Errorf("single-component rollback output must not carry a --component flag")
	}
}

// TestRollbackTargets_Components_FanOut proves a components: manifest fans out one
// cascade-rollback-<name>.yaml per component (sorted), no repo-wide
// cascade-rollback.yaml, each namespaced, each scoped with its own --component.
func TestRollbackTargets_Components_FanOut(t *testing.T) {
	cfg := lifecycleMultiComponentConfig()

	targets, err := rollbackTargets(cfg, "")
	if err != nil {
		t.Fatalf("rollbackTargets: %v", err)
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

	api, ok := byPath[".github/workflows/cascade-rollback-api.yaml"]
	if !ok {
		t.Fatalf("missing cascade-rollback-api.yaml; got paths %v", keys(byPath))
	}
	web, ok := byPath[".github/workflows/cascade-rollback-web.yaml"]
	if !ok {
		t.Fatalf("missing cascade-rollback-web.yaml; got paths %v", keys(byPath))
	}
	if _, ok := byPath[".github/workflows/cascade-rollback.yaml"]; ok {
		t.Errorf("repo-wide cascade-rollback.yaml must not be emitted when components are declared")
	}

	if !strings.Contains(api, "name: Rollback (api)") {
		t.Errorf("api rollback workflow missing namespaced name")
	}
	if !strings.Contains(web, "name: Rollback (web)") {
		t.Errorf("web rollback workflow missing namespaced name")
	}

	// Component scoping on both the preflight and finalize CLI invocations.
	if got := strings.Count(api, "--component api"); got < 2 {
		t.Errorf("api rollback workflow emitted --component api %d times, want >= 2 (preflight and finalize)", got)
	}
	if strings.Contains(api, "--component web") {
		t.Errorf("api rollback workflow must not carry web's --component (isolation)")
	}
	if !strings.Contains(web, "--component web") {
		t.Errorf("web rollback workflow missing --component web")
	}

	// Concurrency isolation: the default per-workflow group differs by the
	// component-namespaced workflow name, so no two components share a lane.
	apiGroup := concurrencyGroupLine(t, api)
	webGroup := concurrencyGroupLine(t, web)
	if apiGroup == webGroup {
		t.Errorf("api and web rollback concurrency groups must differ; both are %q", apiGroup)
	}
}

// TestRollbackTargets_GlobalConcurrencyGroupDoesNotCollapse proves an explicit
// manifest-global concurrency.group does not collapse every component's rollback
// onto one literal lane: the component identity is composed into each group.
func TestRollbackTargets_GlobalConcurrencyGroupDoesNotCollapse(t *testing.T) {
	cfg := lifecycleMultiComponentConfig()
	cfg.Concurrency = &config.ConcurrencyConfig{Group: "shared-lane"}

	targets, err := rollbackTargets(cfg, "")
	if err != nil {
		t.Fatalf("rollbackTargets: %v", err)
	}
	byPath := map[string]string{}
	for _, tg := range targets {
		content, gerr := tg.Gen.Generate()
		if gerr != nil {
			t.Fatalf("Generate(%s): %v", tg.Path, gerr)
		}
		byPath[tg.Path] = content
	}
	apiGroup := concurrencyGroupLine(t, byPath[".github/workflows/cascade-rollback-api.yaml"])
	webGroup := concurrencyGroupLine(t, byPath[".github/workflows/cascade-rollback-web.yaml"])

	if apiGroup == "  group: shared-lane" {
		t.Errorf("api rollback group collapsed onto the bare global literal: %q", apiGroup)
	}
	if apiGroup == webGroup {
		t.Errorf("global concurrency.group collapsed both components onto one lane: %q", apiGroup)
	}
	if !strings.Contains(apiGroup, "api") {
		t.Errorf("api rollback group must retain the component identity: %q", apiGroup)
	}
	if !strings.Contains(webGroup, "web") {
		t.Errorf("web rollback group must retain the component identity: %q", webGroup)
	}
}
