package visualize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// stagesConfig exercises every stage: a multi-env manifest with builds and
// deploys, so the projection includes trunk, build, deploy, promote, and
// release.
func stagesConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Builds: []config.BuildConfig{
			{Name: "api", Workflow: ".github/workflows/build-api.yaml"},
			{Name: "web", Workflow: ".github/workflows/build-web.yaml"},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}
}

func TestBuildStagesViewModel_CollapsesCallbacks(t *testing.T) {
	vm, err := BuildStagesViewModel(stagesConfig())
	if err != nil {
		t.Fatalf("BuildStagesViewModel: %v", err)
	}
	if vm.Kind != DiagramFlowchart {
		t.Errorf("expected DiagramFlowchart, got %q", vm.Kind)
	}

	var ids []string
	for _, n := range vm.Nodes {
		if n.Kind != NodeStage {
			t.Errorf("stages projection must hold only stage nodes, got %+v", n)
		}
		ids = append(ids, n.ID)
	}
	// The five coarse stages, in lifecycle order, with no per-callback nodes.
	if strings.Join(ids, ",") != "trunk,build,deploy,promote,release" {
		t.Errorf("expected the five stages in order, got %v", ids)
	}

	// No per-callback node names may leak into the stage rollup.
	for _, n := range vm.Nodes {
		if strings.Contains(n.ID, "api") || strings.Contains(n.ID, "web") || strings.Contains(n.ID, "app") {
			t.Errorf("per-callback node leaked into stages projection: %+v", n)
		}
	}
}

func TestBuildStagesViewModel_OmitsAbsentStages(t *testing.T) {
	// A single-env library project with no builds or deploys collapses to just
	// the trunk and release bookends.
	vm, err := BuildStagesViewModel(&config.TrunkConfig{TrunkBranch: "main"})
	if err != nil {
		t.Fatalf("BuildStagesViewModel: %v", err)
	}
	var ids []string
	for _, n := range vm.Nodes {
		ids = append(ids, n.ID)
	}
	if strings.Join(ids, ",") != "trunk,release" {
		t.Errorf("expected trunk,release for a no-callback project, got %v", ids)
	}
}

func TestMermaidEmitter_StagesGolden(t *testing.T) {
	vm, err := BuildStagesViewModel(stagesConfig())
	if err != nil {
		t.Fatalf("BuildStagesViewModel: %v", err)
	}

	got, err := NewMermaidEmitter().Emit(vm, DefaultTheme, WithTitle("stages"))
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if !strings.Contains(got, "flowchart TD") {
		t.Errorf("expected a flowchart header, got:\n%s", got)
	}

	golden := filepath.Join("testdata", "stages.mmd")
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
		t.Errorf("emitted stages Mermaid does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestMermaidEmitter_StagesDeterministic(t *testing.T) {
	build := func() (string, error) {
		vm, err := BuildStagesViewModel(stagesConfig())
		if err != nil {
			return "", err
		}
		return NewMermaidEmitter().Emit(vm, DefaultTheme)
	}
	first, err := build()
	if err != nil {
		t.Fatalf("first emit: %v", err)
	}
	for i := 0; i < 10; i++ {
		next, err := build()
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
		if next != first {
			t.Fatalf("emit %d differs from first run", i)
		}
	}
}
