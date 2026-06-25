package visualize

import (
	"fmt"

	"github.com/stablekernel/cascade/internal/config"
)

// startNodeID and endNodeID are the reserved identities of the state machine's
// entry and terminal pseudo-states. They never collide with an environment name
// because cascade env names are simple slugs; the emitter renders nodes of kind
// NodeStart and NodeEnd as the renderer's initial and final markers, so these
// IDs are model-internal and never appear in the diagram text.
const (
	startNodeID = "__start__"
	endNodeID   = "__end__"
)

// BuildEnvViewModel projects the promotion ladder into a render-agnostic state
// machine. The configured environments become a linear chain of env states from
// the first environment to the last, bracketed by a start and an end marker.
// Each environment whose state has diverged onto an integration branch (a hotfix
// Ref or applied patches) gains a hotfix state that branches off that
// environment and rejoins the ladder at the next environment downstream, or at
// the terminal when the diverged environment is last. The model carries no
// diagram syntax; the emitter turns it into a state diagram.
func BuildEnvViewModel(cfg *config.TrunkConfig, state map[string]*config.EnvState) (ViewModel, error) {
	if cfg == nil {
		return ViewModel{}, fmt.Errorf("visualize: nil config")
	}
	if len(cfg.Environments) == 0 {
		return ViewModel{}, fmt.Errorf("visualize: config declares no environments to render")
	}

	vm := ViewModel{Kind: DiagramState}

	// Bookend markers frame the ladder. They are declared first so the start
	// transition reads before the env states in the model.
	vm.Nodes = append(vm.Nodes,
		Node{ID: startNodeID, Kind: NodeStart},
		Node{ID: endNodeID, Kind: NodeEnd},
	)

	for _, env := range cfg.Environments {
		vm.Nodes = append(vm.Nodes, Node{ID: env, Label: env, Kind: NodeEnv})
	}

	// The entry transition lands on the first environment; the exit transition
	// leaves the last one.
	first := cfg.Environments[0]
	last := cfg.Environments[len(cfg.Environments)-1]
	vm.Edges = append(vm.Edges, Edge{From: startNodeID, To: first, Kind: EdgeTransition})

	// Promotion transitions chain each environment to the next in order.
	for i := 0; i+1 < len(cfg.Environments); i++ {
		vm.Edges = append(vm.Edges, Edge{
			From:  cfg.Environments[i],
			To:    cfg.Environments[i+1],
			Kind:  EdgePromote,
			Label: "promote",
		})
	}
	vm.Edges = append(vm.Edges, Edge{From: last, To: endNodeID, Kind: EdgeTransition})

	// Divergence branches are appended after the linear ladder so the chain reads
	// top to bottom before the hotfix detours, keeping the model order stable.
	for i, env := range cfg.Environments {
		es := state[env]
		if !es.IsDiverged() {
			continue
		}
		hotfixID := env + "_hotfix"
		vm.Nodes = append(vm.Nodes, Node{ID: hotfixID, Label: hotfixLabel(es), Kind: NodeHotfix})
		vm.Edges = append(vm.Edges, Edge{From: env, To: hotfixID, Kind: EdgeDiverge, Label: "diverge"})

		rejoinTo := endNodeID
		if i+1 < len(cfg.Environments) {
			rejoinTo = cfg.Environments[i+1]
		}
		vm.Edges = append(vm.Edges, Edge{From: hotfixID, To: rejoinTo, Kind: EdgeRejoin, Label: "rejoin"})
	}

	return vm, nil
}

// hotfixLabel names a divergence state. It prefers the integration branch ref so
// a reader sees which branch the environment tracks, and falls back to a generic
// label when the divergence is patch-only with no recorded ref.
func hotfixLabel(es *config.EnvState) string {
	if es != nil && es.Ref != "" {
		return es.Ref
	}
	return "hotfix"
}
