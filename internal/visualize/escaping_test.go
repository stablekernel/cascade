package visualize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// TestMermaidLabels_FoldNewlines proves a newline in a manifest-derived display
// name cannot split a node, state, or subgraph declaration across lines: a raw
// newline inside any label would terminate the Mermaid statement mid-declaration
// and the remainder would parse as diagram syntax.
func TestMermaidLabels_FoldNewlines(t *testing.T) {
	t.Run("flowchart node label", func(t *testing.T) {
		vm := ViewModel{
			Kind:  DiagramFlowchart,
			Nodes: []Node{{ID: "build-app", Label: "line one\nline two", Kind: NodeBuild}},
		}
		out, err := NewMermaidEmitter().Emit(vm, Theme{})
		if err != nil {
			t.Fatalf("Emit: %v", err)
		}
		if strings.Contains(out, "line one\nline two") {
			t.Errorf("node label newline must be folded, got:\n%s", out)
		}
		if !strings.Contains(out, "line one line two") {
			t.Errorf("folded label text missing, got:\n%s", out)
		}
	})

	t.Run("subgraph label", func(t *testing.T) {
		vm := ViewModel{
			Kind:  DiagramFlowchart,
			Nodes: []Node{{ID: "build-app", Label: "app", Kind: NodeBuild}},
			Groups: []Group{
				{ID: "lane", Label: "repo\nend\nevil --> injected", NodeIDs: []string{"build-app"}},
			},
		}
		out, err := NewMermaidEmitter().Emit(vm, Theme{})
		if err != nil {
			t.Fatalf("Emit: %v", err)
		}
		if strings.Contains(out, "repo\nend") {
			t.Errorf("subgraph label newline must be folded, got:\n%s", out)
		}
	})

	t.Run("state label", func(t *testing.T) {
		vm := ViewModel{
			Kind:  DiagramState,
			Nodes: []Node{{ID: "dev", Label: "dev\nevil --> injected", Kind: NodeEnv}},
		}
		out, err := NewMermaidEmitter().Emit(vm, Theme{})
		if err != nil {
			t.Fatalf("Emit: %v", err)
		}
		if strings.Contains(out, "dev\nevil") {
			t.Errorf("state label newline must be folded, got:\n%s", out)
		}
	})
}

// TestRepoSlug_DistinctReposStayDistinct proves two repos that fold to the same
// alphanumeric slug (org/api-v2 vs org/api.v2) still produce distinct lane and
// node identities, so neither lane is silently dropped or merged.
func TestRepoSlug_DistinctReposStayDistinct(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Builds: []config.BuildConfig{
			{Name: "api", Workflow: ".github/workflows/build-api.yaml"},
		},
		External: []config.ExternalRepoConfig{
			{Repo: "org/api-v2", Deploys: []config.ExternalDeployConfig{
				{Name: "svc", Workflow: ".github/workflows/svc.yaml"},
			}},
			{Repo: "org/api.v2", Deploys: []config.ExternalDeployConfig{
				{Name: "svc", Workflow: ".github/workflows/svc.yaml"},
			}},
		},
	}

	vm, err := BuildCrossRepoViewModel(cfg)
	if err != nil {
		t.Fatalf("BuildCrossRepoViewModel: %v", err)
	}

	groupIDs := make(map[string]bool)
	for _, g := range vm.Groups {
		if groupIDs[g.ID] {
			t.Fatalf("duplicate group id %q: distinct repos must not collide", g.ID)
		}
		groupIDs[g.ID] = true
	}

	nodeIDs := make(map[string]bool)
	for _, n := range vm.Nodes {
		if nodeIDs[n.ID] {
			t.Fatalf("duplicate node id %q: distinct repos must not collide", n.ID)
		}
		nodeIDs[n.ID] = true
	}
}

// TestRepoSlug_PlainSlugStaysReadable proves a repo already made of safe runes
// keeps a readable slug with no hash suffix, so common diagrams stay clean.
func TestRepoSlug_PlainSlugStaysReadable(t *testing.T) {
	if got := repoSlug("orgrepo"); got != "orgrepo" {
		t.Errorf("repoSlug(orgrepo) = %q, want orgrepo", got)
	}
}

// TestThemeInit_ValuesAreJSONEncoded proves the init directive is real JSON:
// a theme value carrying a quote or backslash must be encoded, not spliced,
// or the directive breaks (and a crafted value could inject directive keys).
func TestThemeInit_ValuesAreJSONEncoded(t *testing.T) {
	vm := ViewModel{
		Kind:  DiagramFlowchart,
		Nodes: []Node{{ID: "build-app", Label: "app", Kind: NodeBuild}},
	}
	theme := Theme{Base: `ba"se`, LineColor: "#57606a\\"}

	out, err := NewMermaidEmitter().Emit(vm, theme)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	start := strings.Index(out, "%%{init: ")
	if start == -1 {
		t.Fatalf("init directive missing:\n%s", out)
	}
	rest := out[start+len("%%{init: "):]
	end := strings.Index(rest, "}%%")
	if end == -1 {
		t.Fatalf("init directive not closed:\n%s", out)
	}
	payload := rest[:end]

	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("init payload is not valid JSON: %v\npayload: %s", err, payload)
	}
	if decoded["theme"] != `ba"se` {
		t.Errorf("theme value did not round-trip, got %v", decoded["theme"])
	}
}

// TestClassDefBody_SanitizesUserThemeValues proves a user-theme color value
// cannot smuggle extra classDef attributes or statements: the comma, semicolon,
// and newline that would act as separators are stripped from the value.
func TestClassDefBody_SanitizesUserThemeValues(t *testing.T) {
	body := classDefBody(NodeStyle{
		Fill:   "red,stroke:#f00",
		Stroke: "blue;classDef evil fill:#000",
		Text:   "white\nlinkStyle default stroke:#000",
	})
	for _, forbidden := range []string{",stroke:#f00", ";", "\n", "\r"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("classDef body must not contain %q, got %q", forbidden, body)
		}
	}
}
