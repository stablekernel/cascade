package generate

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func twoComponentConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Components: map[string]config.ComponentConfig{
			"api": {Path: "services/api", TagPrefix: "api-"},
			"web": {Path: "services/web", TagPrefix: "web-"},
		},
	}
}

// TestOrchestrateTargets_SingleComponent_ByteIdentical proves a manifest with no
// components: block takes the untouched single-generator path: exactly one target
// at the output path whose content is byte-identical to a directly built
// generator. This is the byte-identical guarantee for existing manifests.
func TestOrchestrateTargets_SingleComponent_ByteIdentical(t *testing.T) {
	cfg := &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev", "prod"}}

	targets, err := orchestrateTargets(cfg, "", ".github/workflows/orchestrate.yaml", nil, false)
	if err != nil {
		t.Fatalf("orchestrateTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Path != ".github/workflows/orchestrate.yaml" {
		t.Errorf("path = %q, want .github/workflows/orchestrate.yaml", targets[0].Path)
	}

	got, err := targets[0].Gen.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want, err := NewGenerator(cfg, "").Generate()
	if err != nil {
		t.Fatalf("baseline Generate: %v", err)
	}
	if got != want {
		t.Errorf("single-component output drifted from baseline generator")
	}
	if strings.Contains(got, "Orchestrate CI/CD (") {
		t.Errorf("single-component output must not carry a component-namespaced name")
	}
}

// TestOrchestrateTargets_Components_FanOut proves a components: manifest fans out
// one path-scoped, concurrency-isolated, namespaced orchestrate file per
// component (sorted), and no repo-wide orchestrate.yaml.
func TestOrchestrateTargets_Components_FanOut(t *testing.T) {
	cfg := twoComponentConfig()

	targets, err := orchestrateTargets(cfg, "", ".github/workflows/orchestrate.yaml", nil, false)
	if err != nil {
		t.Fatalf("orchestrateTargets: %v", err)
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

	api, ok := byPath[".github/workflows/orchestrate-api.yaml"]
	if !ok {
		t.Fatalf("missing orchestrate-api.yaml; got paths %v", keys(byPath))
	}
	web, ok := byPath[".github/workflows/orchestrate-web.yaml"]
	if !ok {
		t.Fatalf("missing orchestrate-web.yaml; got paths %v", keys(byPath))
	}
	if _, ok := byPath[".github/workflows/orchestrate.yaml"]; ok {
		t.Errorf("repo-wide orchestrate.yaml must not be emitted when components are declared")
	}

	// Naming: each file is namespaced to its component.
	if !strings.Contains(api, "name: Orchestrate CI/CD (api)") {
		t.Errorf("api workflow missing namespaced name")
	}
	if !strings.Contains(web, "name: Orchestrate CI/CD (web)") {
		t.Errorf("web workflow missing namespaced name")
	}

	// Path triggers: scoped to each component subtree.
	if !strings.Contains(api, "services/api/**") {
		t.Errorf("api workflow missing services/api/** path filter")
	}
	if strings.Contains(api, "services/web/**") {
		t.Errorf("api workflow must not carry web's path filter (isolation)")
	}

	// Concurrency: per-component group carrying the component identity.
	if !strings.Contains(api, "group: orchestrate-api-${{ github.ref }}") {
		t.Errorf("api workflow missing per-component concurrency group")
	}
	if strings.Contains(api, "orchestrate-web-${{ github.ref }}") {
		t.Errorf("api workflow must not carry web's concurrency group (isolation)")
	}
}

// TestPlan_Components_MatchesGeneratedBytes proves that for a components:
// manifest the verify-side Plan enumeration is byte-identical to what the
// generate command writes: the fan-out (orchestrate-api.yaml + orchestrate-web.yaml,
// no repo-wide orchestrate.yaml) is produced identically by both seams, so verify
// never falsely reports drift or orphans on a multi-component repo.
func TestPlan_Components_MatchesGeneratedBytes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o755))

	manifest := map[string]any{
		config.DefaultManifestKey: config.CICDFile{Config: twoComponentConfig()},
	}
	body, err := yaml.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "manifest.yaml"), body, 0o644))

	chdir(t, dir)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	dir = resolved

	opts := generateOptions{
		configPath:        ".github/manifest.yaml",
		manifestKey:       config.DefaultManifestKey,
		actionFolder:      "manage-release",
		outputPath:        ".github/workflows/orchestrate.yaml",
		promoteOutputPath: ".github/workflows/promote.yaml",
		force:             true,
	}
	require.NoError(t, runGenerateWorkflow(opts))

	written := walkGeneratedFiles(t, dir)
	// The repo-wide orchestrate.yaml must not exist; per-component files must.
	require.NotContains(t, written, filepath.Join(".github", "workflows", "orchestrate.yaml"),
		"components manifest must not emit a repo-wide orchestrate.yaml")
	require.Contains(t, written, filepath.Join(".github", "workflows", "orchestrate-api.yaml"))
	require.Contains(t, written, filepath.Join(".github", "workflows", "orchestrate-web.yaml"))

	planned, err := Plan(PlanOptions{
		ConfigPath:        ".github/manifest.yaml",
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        ".github/workflows/orchestrate.yaml",
		PromoteOutputPath: ".github/workflows/promote.yaml",
	})
	require.NoError(t, err)

	paths := make([]string, len(planned))
	for i, p := range planned {
		paths[i] = p.Path
	}
	sortedPaths := append([]string(nil), paths...)
	sort.Strings(sortedPaths)
	require.Equal(t, sortedPaths, paths, "Plan output must be sorted by Path")

	plannedByRel := make(map[string]string, len(planned))
	for _, p := range planned {
		abs := p.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, abs)
		}
		rel, rerr := filepath.Rel(dir, abs)
		require.NoError(t, rerr)
		plannedByRel[rel] = p.Content
	}

	require.Equal(t, len(written), len(plannedByRel),
		"Plan emitted a different number of files than generate wrote")
	for rel, want := range written {
		got, ok := plannedByRel[rel]
		require.Truef(t, ok, "Plan did not emit %s that generate wrote", rel)
		require.Equalf(t, want, got, "Plan content for %s differs from generated bytes", rel)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
