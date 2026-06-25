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

// NodeKind classifies a pipeline node for rendering. The set mirrors the
// callback types cascade already models (validate, build, deploy). It is a
// rendering hint only; an emitter may map several kinds to one visual style.
type NodeKind string

// Node kinds, one per cascade callback type.
const (
	NodeValidate NodeKind = "validate"
	NodeBuild    NodeKind = "build"
	NodeDeploy   NodeKind = "deploy"
)

// EdgeKind classifies a dependency edge. Hard edges come from Edges (they both
// order a job and skip-gate it); optional edges come from OptionalEdges (they
// order only). Emitters render the two with visually distinct styling so a
// reader can tell a blocking dependency from an ordering-only one.
type EdgeKind string

// Edge kinds.
const (
	EdgeHard     EdgeKind = "hard"
	EdgeOptional EdgeKind = "optional"
)

// Node is one pipeline job in the view. ID is the stable, prefixed job ID
// (validate, build-app, deploy-app) used as the diagram node identity. Label is
// the human-facing display name. Kind drives styling.
type Node struct {
	ID    string
	Label string
	Kind  NodeKind
}

// Edge is one dependency from a job to one of its dependencies. From is the
// dependent job ID, To is the job it depends on, and Kind separates hard from
// optional dependencies.
type Edge struct {
	From string
	To   string
	Kind EdgeKind
}

// ViewModel is the deterministic, render-agnostic description of a pipeline's
// job DAG. Nodes follow manifest declaration order (the graph's Order seed) and
// edges follow node order then dependency-list order, so two builds of the same
// manifest produce byte-identical emitter output. The model holds no diagram
// syntax.
type ViewModel struct {
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
