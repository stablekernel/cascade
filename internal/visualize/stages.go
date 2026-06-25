package visualize

import (
	"fmt"

	"github.com/stablekernel/cascade/internal/config"
)

// BuildStagesViewModel projects the manifest into the coarse lifecycle flow:
// trunk, build, deploy, promote, release. It collapses every callback into its
// stage, so the diagram shows the shape of the pipeline without per-callback
// detail. A stage appears only when the manifest exercises it: build when the
// manifest declares builds, deploy when it declares deploys, and promote when it
// declares more than one environment. Trunk and release always frame the flow.
// The present stages are chained in lifecycle order with a single edge between
// each consecutive pair. The model carries no diagram syntax.
func BuildStagesViewModel(cfg *config.TrunkConfig) (ViewModel, error) {
	if cfg == nil {
		return ViewModel{}, fmt.Errorf("visualize: nil config")
	}

	// Candidate stages in lifecycle order, each gated on whether the manifest
	// actually exercises it. Trunk and release are unconditional bookends.
	candidates := []struct {
		id      string
		label   string
		present bool
	}{
		{id: "trunk", label: "Trunk", present: true},
		{id: "build", label: "Build", present: len(cfg.Builds) > 0},
		{id: "deploy", label: "Deploy", present: len(cfg.Deploys) > 0},
		{id: "promote", label: "Promote", present: len(cfg.Environments) > 1},
		{id: "release", label: "Release", present: true},
	}

	vm := ViewModel{Kind: DiagramFlowchart}

	var prev string
	for _, c := range candidates {
		if !c.present {
			continue
		}
		vm.Nodes = append(vm.Nodes, Node{ID: c.id, Label: c.label, Kind: NodeStage})
		if prev != "" {
			vm.Edges = append(vm.Edges, Edge{From: prev, To: c.id, Kind: EdgeStage})
		}
		prev = c.id
	}

	return vm, nil
}
