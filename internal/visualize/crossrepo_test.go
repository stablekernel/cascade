package visualize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// primaryWithDependents is a primary manifest that coordinates two external
// satellite repos and also notifies an upstream primary, so the projection must
// carry a lane per repo and cross-repo edges in both directions.
func primaryWithDependents() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Builds: []config.BuildConfig{
			{Name: "api", Workflow: ".github/workflows/build-api.yaml"},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
		External: []config.ExternalRepoConfig{
			{
				Repo: "org/cdk-infra",
				Deploys: []config.ExternalDeployConfig{
					{Name: "cdk", Workflow: ".github/workflows/cdk.yaml"},
				},
			},
			{
				Repo: "org/web-edge",
				Deploys: []config.ExternalDeployConfig{
					{Name: "edge", Workflow: ".github/workflows/edge.yaml"},
				},
			},
		},
		Notify: &config.NotifyConfig{Repo: "org/platform"},
	}
}

func TestBuildCrossRepoViewModel_NilConfig(t *testing.T) {
	if _, err := BuildCrossRepoViewModel(nil); err == nil {
		t.Fatal("expected an error for a nil config, got nil")
	}
}

func TestBuildCrossRepoViewModel_RendersLanesAndEdges(t *testing.T) {
	vm, err := BuildCrossRepoViewModel(primaryWithDependents())
	if err != nil {
		t.Fatalf("BuildCrossRepoViewModel: %v", err)
	}
	if vm.Kind != DiagramFlowchart {
		t.Errorf("expected DiagramFlowchart, got %q", vm.Kind)
	}

	// One lane for the primary plus one per dependent and one for the notified
	// upstream: four groups in stable construction order.
	var groupLabels []string
	for _, g := range vm.Groups {
		groupLabels = append(groupLabels, g.Label)
	}
	want := "primary,org/cdk-infra,org/web-edge,org/platform"
	if strings.Join(groupLabels, ",") != want {
		t.Fatalf("group lanes mismatch:\n got %v\nwant %s", groupLabels, want)
	}

	// The primary lane holds the stage pipeline; the dependent lanes hold the
	// external deploy nodes; the notify lane holds the upstream repo node.
	nodeKindByID := make(map[string]NodeKind, len(vm.Nodes))
	for _, n := range vm.Nodes {
		nodeKindByID[n.ID] = n.Kind
	}

	// Cross-repo edges: primary -> each dependent deploy (external), and the
	// satellite -> primary notify edge.
	var external, notify int
	for _, e := range vm.Edges {
		switch e.Kind {
		case EdgeExternal:
			external++
			if e.Label == "" {
				t.Errorf("external edge %+v must carry a label", e)
			}
		case EdgeNotify:
			notify++
			if e.Label != "notify" {
				t.Errorf("notify edge label = %q, want %q", e.Label, "notify")
			}
		}
	}
	if external != 2 {
		t.Errorf("expected 2 external edges (one per dependent deploy), got %d", external)
	}
	if notify != 1 {
		t.Errorf("expected 1 notify edge, got %d", notify)
	}
}

func TestBuildCrossRepoViewModel_NoExternals_PrimaryOnly(t *testing.T) {
	// A manifest with no external coordination and no notify renders just the
	// primary stage pipeline with no cross-repo lanes or edges.
	vm, err := BuildCrossRepoViewModel(&config.TrunkConfig{
		TrunkBranch: "main",
		Deploys:     []config.DeployConfig{{Name: "app", Workflow: ".github/workflows/deploy.yaml"}},
	})
	if err != nil {
		t.Fatalf("BuildCrossRepoViewModel: %v", err)
	}
	if len(vm.Groups) != 0 {
		t.Errorf("expected no cross-repo lanes for a manifest with no externals, got %d", len(vm.Groups))
	}
	for _, e := range vm.Edges {
		if e.Kind == EdgeExternal || e.Kind == EdgeNotify {
			t.Errorf("unexpected cross-repo edge in a no-externals manifest: %+v", e)
		}
	}
	if len(vm.Nodes) == 0 {
		t.Error("expected the primary stage pipeline to still render")
	}
}

func TestBuildCrossRepoViewModel_SatelliteOnly_NotifiesNamedPrimary(t *testing.T) {
	// A satellite manifest has a notify config but no external coordination; it
	// must still render its notify edge to the named primary repo.
	vm, err := BuildCrossRepoViewModel(&config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Deploys:      []config.DeployConfig{{Name: "app", Workflow: ".github/workflows/deploy.yaml"}},
		Notify:       &config.NotifyConfig{Repo: "org/my-backend"},
	})
	if err != nil {
		t.Fatalf("BuildCrossRepoViewModel: %v", err)
	}

	var foundNotify bool
	for _, e := range vm.Edges {
		if e.Kind == EdgeNotify {
			foundNotify = true
		}
	}
	if !foundNotify {
		t.Fatal("expected a notify edge to the named primary, found none")
	}

	var namedPrimary bool
	for _, n := range vm.Nodes {
		if n.Kind == NodeRepo && n.Label == "org/my-backend" {
			namedPrimary = true
		}
	}
	if !namedPrimary {
		t.Error("expected a node for the named primary org/my-backend")
	}
}

func TestMermaidEmitter_CrossRepoGolden(t *testing.T) {
	vm, err := BuildCrossRepoViewModel(primaryWithDependents())
	if err != nil {
		t.Fatalf("BuildCrossRepoViewModel: %v", err)
	}

	got, err := NewMermaidEmitter().Emit(vm, DefaultTheme, WithTitle("cross-repo"))
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Structural anchors a reader relies on: a flowchart with a subgraph per
	// repo and the cross-repo edges in both directions.
	for _, want := range []string{
		"flowchart TD",
		`subgraph primary["primary"]`,
		`subgraph repo_org_cdk_infra["org/cdk-infra"]`,
		`subgraph repo_org_web_edge["org/web-edge"]`,
		`subgraph repo_org_platform["org/platform"]`,
		"==>|cdk|",
		"==>|edge|",
		"-. notify .->",
		"end",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("emitted cross-repo Mermaid missing %q:\n%s", want, got)
		}
	}

	golden := filepath.Join("testdata", "crossrepo.mmd")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	wantBytes, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if got != string(wantBytes) {
		t.Errorf("emitted cross-repo Mermaid does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, wantBytes)
	}
}

func TestMermaidEmitter_CrossRepoDeterministic(t *testing.T) {
	build := func() (string, error) {
		vm, err := BuildCrossRepoViewModel(primaryWithDependents())
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
