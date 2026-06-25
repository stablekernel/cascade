package visualize

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
)

// update regenerates the golden files when set. Run: go test -run Mermaid -update.
var update = flag.Bool("update", false, "update golden files")

// representativeConfig returns a manifest exercising the issue's scenario:
// a validate, two builds, two deploys, with one optional dependency. It is the
// fixture behind the golden-file and determinism assertions.
func representativeConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		Validate: &config.ValidateConfig{
			Workflow: ".github/workflows/validate.yaml",
		},
		Builds: []config.BuildConfig{
			{Name: "api", Workflow: ".github/workflows/build-api.yaml"},
			{
				Name:              "web",
				Workflow:          ".github/workflows/build-web.yaml",
				OptionalDependsOn: []string{"api"},
			},
		},
		Deploys: []config.DeployConfig{
			{
				Name:      "staging",
				Workflow:  ".github/workflows/deploy.yaml",
				DependsOn: []string{"build:api", "build:web"},
			},
			{
				Name:      "prod",
				Workflow:  ".github/workflows/deploy.yaml",
				DependsOn: []string{"deploy:staging"},
			},
		},
	}
}

func buildVM(t *testing.T, cfg *config.TrunkConfig) ViewModel {
	t.Helper()
	g := generate.BuildDependencyGraph(cfg)
	vm, err := BuildViewModel(g)
	if err != nil {
		t.Fatalf("BuildViewModel: %v", err)
	}
	return vm
}

func TestBuildViewModel_IsRenderAgnostic(t *testing.T) {
	vm := buildVM(t, representativeConfig())

	if len(vm.Nodes) != 5 {
		t.Fatalf("expected 5 nodes, got %d: %+v", len(vm.Nodes), vm.Nodes)
	}
	// First node is validate, in declaration order.
	if vm.Nodes[0].ID != "validate" || vm.Nodes[0].Kind != NodeValidate {
		t.Errorf("expected validate first, got %+v", vm.Nodes[0])
	}
	// No Mermaid (or other) syntax may leak into the model's labels/ids.
	for _, n := range vm.Nodes {
		if strings.ContainsAny(n.ID+n.Label, "[]()-->") {
			// hyphen is allowed in IDs; check only diagram-syntax tokens.
			if strings.Contains(n.ID, "-->") || strings.Contains(n.Label, "-->") {
				t.Errorf("diagram syntax leaked into view model: %+v", n)
			}
		}
	}
	// The web build carries an optional edge to api.
	var sawOptional bool
	for _, e := range vm.Edges {
		if e.Kind == EdgeOptional && e.From == "build-web" && e.To == "build-api" {
			sawOptional = true
		}
	}
	if !sawOptional {
		t.Errorf("expected optional edge build-web -> build-api, edges: %+v", vm.Edges)
	}
}

func TestMermaidEmitter_Golden(t *testing.T) {
	vm := buildVM(t, representativeConfig())

	got, err := NewMermaidEmitter().Emit(vm, DefaultTheme, WithTitle("pipeline"))
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	golden := filepath.Join("testdata", "representative.mmd")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("emitted Mermaid does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestMermaidEmitter_Deterministic(t *testing.T) {
	cfg := representativeConfig()

	first, err := NewMermaidEmitter().Emit(buildVM(t, cfg), DefaultTheme)
	if err != nil {
		t.Fatalf("first emit: %v", err)
	}
	for i := 0; i < 10; i++ {
		next, err := NewMermaidEmitter().Emit(buildVM(t, cfg), DefaultTheme)
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
		if next != first {
			t.Fatalf("emit %d differs from first run:\n%s\nvs\n%s", i, next, first)
		}
	}
}

// flowchartHeaderRe matches the required Mermaid flowchart header.
var flowchartHeaderRe = regexp.MustCompile(`(?m)^flowchart TD$`)

// edgeRe captures both arrow forms (solid and dotted) and their endpoints.
var edgeRe = regexp.MustCompile(`(?m)^\s+(\w+) -\.?-> (\w+)$`)

// nodeDeclRe captures a node declaration's identifier (any shape bracket).
var nodeDeclRe = regexp.MustCompile(`(?m)^\s+(\w+)[\(\[]`)

func TestMermaidEmitter_StructurallyValid(t *testing.T) {
	out, err := NewMermaidEmitter().Emit(buildVM(t, representativeConfig()), DefaultTheme)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if !flowchartHeaderRe.MatchString(out) {
		t.Fatalf("missing flowchart header in:\n%s", out)
	}

	declared := map[string]bool{}
	for _, m := range nodeDeclRe.FindAllStringSubmatch(out, -1) {
		declared[m[1]] = true
	}
	if len(declared) == 0 {
		t.Fatalf("no node declarations found in:\n%s", out)
	}

	// Every edge endpoint must reference a declared node.
	edges := edgeRe.FindAllStringSubmatch(out, -1)
	if len(edges) == 0 {
		t.Fatalf("no edges found in:\n%s", out)
	}
	for _, e := range edges {
		from, to := e[1], e[2]
		if !declared[from] {
			t.Errorf("edge references undeclared source %q", from)
		}
		if !declared[to] {
			t.Errorf("edge references undeclared target %q", to)
		}
	}
}

func TestBuildViewModel_CyclicGraphErrors(t *testing.T) {
	g := &generate.DependencyGraph{
		Nodes: map[string]generate.CallbackInfo{
			"build-a": {JobID: "build-a", DisplayName: "Build (a)", Type: "build"},
			"build-b": {JobID: "build-b", DisplayName: "Build (b)", Type: "build"},
		},
		Edges: map[string][]string{
			"build-a": {"build-b"},
			"build-b": {"build-a"},
		},
		OptionalEdges: map[string][]string{},
		Order:         []string{"build-a", "build-b"},
	}

	_, err := BuildViewModel(g)
	if err == nil {
		t.Fatal("expected error for cyclic graph, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got: %v", err)
	}
}

func TestBuildViewModel_NilGraphErrors(t *testing.T) {
	if _, err := BuildViewModel(nil); err == nil {
		t.Fatal("expected error for nil graph, got nil")
	}
}
