package visualize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// envConfig returns a three-environment promotion ladder used by the env-state
// machine tests.
func envConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "staging", "prod"},
	}
}

// divergedState marks staging as tracking a hotfix integration branch, so the
// env projection must draw a divergence branch off staging that rejoins at prod.
func divergedState() map[string]*config.EnvState {
	return map[string]*config.EnvState{
		"dev":     {},
		"staging": {Ref: "hotfix/login"},
		"prod":    {},
	}
}

func TestBuildEnvViewModel_IsStateMachine(t *testing.T) {
	vm, err := BuildEnvViewModel(envConfig(), divergedState())
	if err != nil {
		t.Fatalf("BuildEnvViewModel: %v", err)
	}
	if vm.Kind != DiagramState {
		t.Errorf("expected DiagramState, got %q", vm.Kind)
	}

	// The ladder must carry an env node per configured environment, in order.
	var envIDs []string
	for _, n := range vm.Nodes {
		if n.Kind == NodeEnv {
			envIDs = append(envIDs, n.ID)
		}
	}
	if strings.Join(envIDs, ",") != "dev,staging,prod" {
		t.Errorf("expected env ladder dev,staging,prod, got %v", envIDs)
	}

	// A start marker enters the first env and an end marker leaves the last env.
	var sawStart, sawEnd bool
	for _, n := range vm.Nodes {
		switch n.Kind {
		case NodeStart:
			sawStart = true
		case NodeEnd:
			sawEnd = true
		}
	}
	if !sawStart || !sawEnd {
		t.Errorf("expected start and end markers, start=%v end=%v", sawStart, sawEnd)
	}

	// The diverged staging env must have a hotfix node and a diverge plus rejoin
	// edge, with the rejoin landing on the downstream prod env.
	var sawHotfix, sawDiverge, sawRejoin bool
	for _, n := range vm.Nodes {
		if n.Kind == NodeHotfix {
			sawHotfix = true
		}
	}
	for _, e := range vm.Edges {
		if e.Kind == EdgeDiverge && e.From == "staging" {
			sawDiverge = true
		}
		if e.Kind == EdgeRejoin && e.To == "prod" {
			sawRejoin = true
		}
	}
	if !sawHotfix || !sawDiverge || !sawRejoin {
		t.Errorf("expected hotfix divergence and rejoin, hotfix=%v diverge=%v rejoin=%v", sawHotfix, sawDiverge, sawRejoin)
	}
}

func TestBuildEnvViewModel_NoEnvironmentsErrors(t *testing.T) {
	if _, err := BuildEnvViewModel(&config.TrunkConfig{}, nil); err == nil {
		t.Fatal("expected error for a config with no environments, got nil")
	}
}

func TestBuildEnvViewModel_NilConfigErrors(t *testing.T) {
	if _, err := BuildEnvViewModel(nil, nil); err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
}

func TestMermaidEmitter_EnvGolden(t *testing.T) {
	vm, err := BuildEnvViewModel(envConfig(), divergedState())
	if err != nil {
		t.Fatalf("BuildEnvViewModel: %v", err)
	}

	got, err := NewMermaidEmitter().Emit(vm, DefaultTheme, WithTitle("environments"))
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if !strings.Contains(got, "stateDiagram-v2") {
		t.Errorf("expected a stateDiagram-v2 header, got:\n%s", got)
	}

	golden := filepath.Join("testdata", "env.mmd")
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
		t.Errorf("emitted env Mermaid does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestMermaidEmitter_EnvDeterministic(t *testing.T) {
	first, err := func() (string, error) {
		vm, err := BuildEnvViewModel(envConfig(), divergedState())
		if err != nil {
			return "", err
		}
		return NewMermaidEmitter().Emit(vm, DefaultTheme)
	}()
	if err != nil {
		t.Fatalf("first emit: %v", err)
	}
	for i := 0; i < 10; i++ {
		vm, err := BuildEnvViewModel(envConfig(), divergedState())
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
		next, err := NewMermaidEmitter().Emit(vm, DefaultTheme)
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
		if next != first {
			t.Fatalf("emit %d differs from first run:\n%s\nvs\n%s", i, next, first)
		}
	}
}
