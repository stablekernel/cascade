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

	vm := ViewModel{Kind: DiagramFlowchart}

	var prev string
	for _, c := range presentStages(cfg) {
		vm.Nodes = append(vm.Nodes, Node{ID: c.id, Label: c.label, Kind: NodeStage})
		if prev != "" {
			vm.Edges = append(vm.Edges, Edge{From: prev, To: c.id, Kind: EdgeStage})
		}
		prev = c.id
	}

	return vm, nil
}

// stageDef is one coarse lifecycle stage with its display label.
type stageDef struct {
	id    string
	label string
}

// presentStages returns the lifecycle stages the manifest exercises, in order.
// Trunk and release are unconditional bookends; build, deploy, and promote are
// each gated on whether the manifest declares the corresponding work. The cross-
// repo projection reuses it to render the primary repo's pipeline lane, so the
// stage gating stays defined in one place.
func presentStages(cfg *config.TrunkConfig) []stageDef {
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

	var stages []stageDef
	for _, c := range candidates {
		if c.present {
			stages = append(stages, stageDef{id: c.id, label: c.label})
		}
	}
	return stages
}
