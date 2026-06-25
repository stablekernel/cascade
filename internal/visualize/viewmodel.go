// Package visualize builds a render-agnostic view of a cascade pipeline and
// emits it as a diagram. The view model carries no diagram syntax; concrete
// Emitter implementations (Mermaid is the first) turn it into renderer-specific
// source. The separation keeps the projection independently testable and lets a
// richer renderer be added later without reshaping the model.
package visualize

import (
	"fmt"

	"github.com/stablekernel/cascade/internal/generate"
)

// DiagramKind tells an emitter which family of diagram a ViewModel describes.
// The job and stage projections are directed graphs (a flowchart); the env
// projection is a promotion state machine (a state diagram). The kind lives on
// the model, not in the emitter, so the emitter stays a thin renderer that
// switches on what the model already declares rather than on the granularity.
type DiagramKind string

// Diagram kinds. The zero value renders as a flowchart so a model built without
// setting Kind (the original job projection) keeps its behavior.
const (
	DiagramFlowchart DiagramKind = "flowchart"
	DiagramState     DiagramKind = "state"
)

// NodeKind classifies a node for rendering. The first three mirror the callback
// types cascade models (validate, build, deploy) for the job DAG. NodeStage is
// a coarse lifecycle stage; NodeEnv, NodeHotfix, NodeStart, and NodeEnd describe
// the env state machine. It is a rendering hint only; an emitter may map several
// kinds to one visual style.
type NodeKind string

// Node kinds.
const (
	NodeValidate NodeKind = "validate"
	NodeBuild    NodeKind = "build"
	NodeDeploy   NodeKind = "deploy"
	// NodeStage is one coarse lifecycle stage in the stages projection.
	NodeStage NodeKind = "stage"
	// NodeEnv is one environment state in the env promotion ladder.
	NodeEnv NodeKind = "env"
	// NodeHotfix is a diverged integration-branch state hanging off an env.
	NodeHotfix NodeKind = "hotfix"
	// NodeStart marks the state-machine entry point. An emitter renders it as the
	// renderer's initial pseudo-state rather than a declared node.
	NodeStart NodeKind = "start"
	// NodeEnd marks the state-machine terminal. An emitter renders it as the
	// renderer's final pseudo-state rather than a declared node.
	NodeEnd NodeKind = "end"
)

// EdgeKind classifies an edge. Hard edges come from Edges (they both order a job
// and skip-gate it); optional edges come from OptionalEdges (they order only).
// Stage edges connect lifecycle stages. Promote, diverge, and rejoin edges are
// transitions in the env state machine. Emitters render the kinds with visually
// distinct styling so a reader can tell them apart.
type EdgeKind string

// Edge kinds.
const (
	EdgeHard     EdgeKind = "hard"
	EdgeOptional EdgeKind = "optional"
	// EdgeStage connects two lifecycle stages in the stages projection.
	EdgeStage EdgeKind = "stage"
	// EdgePromote is a promotion transition between two consecutive envs.
	EdgePromote EdgeKind = "promote"
	// EdgeDiverge is the transition from an env onto its hotfix branch.
	EdgeDiverge EdgeKind = "diverge"
	// EdgeRejoin is the transition from a hotfix branch back onto the ladder.
	EdgeRejoin EdgeKind = "rejoin"
	// EdgeTransition is an unlabeled state-machine transition, used for the
	// start and end bookend edges.
	EdgeTransition EdgeKind = "transition"
)

// Node is one pipeline job in the view. ID is the stable, prefixed job ID
// (validate, build-app, deploy-app) used as the diagram node identity. Label is
// the human-facing display name. Kind drives styling.
type Node struct {
	ID    string
	Label string
	Kind  NodeKind
}

// Edge is one connection from a source node to a target node. From and To are
// node IDs, Kind selects the visual style, and Label is an optional transition
// caption an emitter renders when the renderer supports edge labels (the env
// state machine uses it for promote, diverge, and rejoin). An empty Label emits
// no caption, matching the unlabeled job and stage edges.
type Edge struct {
	From  string
	To    string
	Kind  EdgeKind
	Label string
}

// ViewModel is the deterministic, render-agnostic description of one pipeline
// projection. Kind tells the emitter which diagram family to render; Nodes and
// edges follow a stable construction order so two builds of the same manifest
// produce byte-identical emitter output. The model holds no diagram syntax.
type ViewModel struct {
	Kind  DiagramKind
	Nodes []Node
	Edges []Edge
}

// BuildViewModel projects a generated DependencyGraph into a render-agnostic
// ViewModel. It walks the graph in Order (the same deterministic seed
// TopologicalSort uses) so node and edge slices are stable run to run, and it
// rejects a cyclic graph by surfacing the cycle TopologicalSort detects rather
// than emitting a malformed diagram. The returned model depends only on the
// cascade model and carries no Mermaid (or other) syntax.
func BuildViewModel(g *generate.DependencyGraph) (ViewModel, error) {
	if g == nil {
		return ViewModel{}, fmt.Errorf("visualize: nil dependency graph")
	}

	// A cyclic DAG has no valid diagram; reuse the generator's cycle detection
	// so the failure surfaces here at projection time, never as a panic or a
	// misleading partial diagram downstream.
	if _, err := g.TopologicalSort(); err != nil {
		return ViewModel{}, fmt.Errorf("visualize: %w", err)
	}

	vm := ViewModel{
		Kind:  DiagramFlowchart,
		Nodes: make([]Node, 0, len(g.Order)),
	}

	for _, id := range g.Order {
		info, ok := g.Nodes[id]
		if !ok {
			// Order is built alongside Nodes, so a missing entry signals a
			// corrupt graph rather than a recoverable state.
			return ViewModel{}, fmt.Errorf("visualize: order references unknown node %q", id)
		}
		vm.Nodes = append(vm.Nodes, Node{
			ID:    info.JobID,
			Label: info.DisplayName,
			Kind:  nodeKind(info.Type),
		})
	}

	// Emit edges grouped by dependent (in Order), then in dependency-list order
	// within each group, so the slice is deterministic. Hard edges precede
	// optional edges for the same node to keep the visual reading order stable.
	for _, id := range g.Order {
		for _, dep := range g.Edges[id] {
			vm.Edges = append(vm.Edges, Edge{From: id, To: dep, Kind: EdgeHard})
		}
		for _, dep := range g.OptionalEdges[id] {
			vm.Edges = append(vm.Edges, Edge{From: id, To: dep, Kind: EdgeOptional})
		}
	}

	return vm, nil
}

// nodeKind maps a cascade callback type string to a view NodeKind. An unknown
// type falls back to NodeBuild so rendering degrades gracefully rather than
// dropping the node.
func nodeKind(callbackType string) NodeKind {
	switch callbackType {
	case "validate":
		return NodeValidate
	case "deploy":
		return NodeDeploy
	case "build":
		return NodeBuild
	default:
		return NodeBuild
	}
}
