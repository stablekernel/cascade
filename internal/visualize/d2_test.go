package visualize

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// d2Golden emits vm through the D2 emitter and compares the result against the
// named golden file, rewriting it when -update is set. It centralizes the
// golden-file mechanics so each granularity's case stays a one-liner, mirroring
// the Mermaid golden test.
func d2Golden(t *testing.T, name string, vm ViewModel, opts ...Option) {
	t.Helper()

	got, err := NewD2Emitter().Emit(vm, DefaultTheme, opts...)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	golden := filepath.Join("testdata", name)
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
		t.Errorf("emitted D2 does not match %s.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestD2Emitter_GoldenJobs(t *testing.T) {
	d2Golden(t, "d2_jobs.d2", buildVM(t, representativeConfig()), WithTitle("pipeline"))
}

func TestD2Emitter_GoldenStages(t *testing.T) {
	vm, err := BuildStagesViewModel(representativeConfig())
	if err != nil {
		t.Fatalf("BuildStagesViewModel: %v", err)
	}
	d2Golden(t, "d2_stages.d2", vm)
}

func TestD2Emitter_GoldenEnv(t *testing.T) {
	vm, err := BuildEnvViewModel(envConfig(), divergedState())
	if err != nil {
		t.Fatalf("BuildEnvViewModel: %v", err)
	}
	d2Golden(t, "d2_env.d2", vm)
}

func TestD2Emitter_GoldenCrossRepo(t *testing.T) {
	vm, err := BuildCrossRepoViewModel(primaryWithDependents())
	if err != nil {
		t.Fatalf("BuildCrossRepoViewModel: %v", err)
	}
	d2Golden(t, "d2_crossrepo.d2", vm)
}

func TestD2Emitter_Deterministic(t *testing.T) {
	cfg := representativeConfig()

	first, err := NewD2Emitter().Emit(buildVM(t, cfg), DefaultTheme)
	if err != nil {
		t.Fatalf("first emit: %v", err)
	}
	for i := 0; i < 10; i++ {
		next, err := NewD2Emitter().Emit(buildVM(t, cfg), DefaultTheme)
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
		if next != first {
			t.Fatalf("emit %d differs from first run:\n%s\nvs\n%s", i, next, first)
		}
	}
}

// d2NodeDeclRe captures a node or lane declaration's key (the token before the
// colon), at any indent, for both the "id: label {..}" node form and the
// "id: label {" container-opening form.
var d2NodeDeclRe = regexp.MustCompile(`(?m)^\s*(\w+): .*\{`)

// d2EdgeRe captures both endpoints of a connection, allowing a container-
// qualified "lane.node" reference on either side.
var d2EdgeRe = regexp.MustCompile(`(?m)^([\w.]+) -> ([\w.]+):`)

func TestD2Emitter_StructurallyValid(t *testing.T) {
	vm, err := BuildCrossRepoViewModel(primaryWithDependents())
	if err != nil {
		t.Fatalf("BuildCrossRepoViewModel: %v", err)
	}
	out, err := NewD2Emitter().Emit(vm, DefaultTheme)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if !strings.Contains(out, "layout-engine: elk") {
		t.Fatalf("missing elk layout selector in:\n%s", out)
	}
	if !strings.Contains(out, `style.fill: "#151b20"`) {
		t.Fatalf("missing crucible canvas fill in:\n%s", out)
	}

	// Collect every declared key. A container-qualified edge endpoint resolves
	// when its trailing segment is a declared key, so the leaf token is what the
	// endpoint check needs.
	declared := map[string]bool{}
	for _, m := range d2NodeDeclRe.FindAllStringSubmatch(out, -1) {
		declared[m[1]] = true
	}
	if len(declared) == 0 {
		t.Fatalf("no node declarations found in:\n%s", out)
	}

	edges := d2EdgeRe.FindAllStringSubmatch(out, -1)
	if len(edges) == 0 {
		t.Fatalf("no edges found in:\n%s", out)
	}
	for _, e := range edges {
		from := leafSegment(e[1])
		to := leafSegment(e[2])
		if !declared[from] {
			t.Errorf("edge references undeclared source %q (in %q)", from, e[1])
		}
		if !declared[to] {
			t.Errorf("edge references undeclared target %q (in %q)", to, e[2])
		}
	}
}

// leafSegment returns the final dotted segment of a D2 reference, so a
// container-qualified "lane.node" endpoint is checked against its declared leaf
// key.
func leafSegment(ref string) string {
	if i := strings.LastIndex(ref, "."); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

func TestD2Emitter_UnknownEdgeKindErrors(t *testing.T) {
	vm := ViewModel{
		Kind:  DiagramFlowchart,
		Nodes: []Node{{ID: "a", Label: "A", Kind: NodeBuild}, {ID: "b", Label: "B", Kind: NodeBuild}},
		Edges: []Edge{{From: "a", To: "b", Kind: EdgeKind("bogus")}},
	}
	if _, err := NewD2Emitter().Emit(vm, DefaultTheme); err == nil {
		t.Fatal("expected an error for an unknown edge kind, got nil")
	}
}
