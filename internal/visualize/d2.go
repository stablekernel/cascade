package visualize

import (
	"fmt"
	"strings"
)

// D2Emitter renders a ViewModel as D2 source in cascade's branded "crucible"
// visual style: a dark teal canvas, 3d state slabs, hexagon deploy invokes,
// diamond guards, rounded dashed environment plates, and small entry / double
// terminal markers. It is a second concrete Emitter behind the same seam as the
// Mermaid emitter, selected with `cascade graph --format d2`, and it is the
// source text a sidecar renderer turns into SVG or PNG.
//
// Output is deterministic: it ranges the model's ordered slices and never
// iterates a map, so the same model always yields byte-identical source.
//
// The palette is the branded crucible style rather than the passed Theme's
// colors: the Mermaid emitter is the theme-driven, GitHub-native path, while D2
// is the high-fidelity branded renderer whose look is fixed. The theme's Name is
// recorded in a leading comment for traceability, so a caller can still tell
// which theme was requested without the colors changing.
type D2Emitter struct{}

// NewD2Emitter returns a ready D2Emitter. The type is stateless, so the
// constructor exists only to give callers a stable construction point that
// mirrors NewMermaidEmitter.
func NewD2Emitter() *D2Emitter { return &D2Emitter{} }

// compile-time assertion that D2Emitter satisfies the Emitter seam.
var _ Emitter = (*D2Emitter)(nil)

// d2Header is the fixed preamble every diagram carries: the ELK layout selector
// (the crucible layout engine), the class catalog that defines the crucible
// shape and color vocabulary, and the dark canvas fill. It is emitted verbatim
// so the class definitions stay in one place and the output is stable.
const d2Header = `vars: {
  d2-config: {
    layout-engine: elk
  }
}

classes: {
  state: {
    shape: rectangle
    style: {
      fill: "#222a30"
      stroke: "#36d0c4"
      font-color: "#f4f7f8"
      stroke-width: 2
      3d: true
    }
  }
  invoke: {
    shape: hexagon
    style: {
      fill: "#06343a"
      stroke: "#36d0c4"
      font-color: "#8af0e8"
      stroke-width: 2
    }
  }
  region: {
    shape: rectangle
    style: {
      fill: "#1a2228"
      stroke: "#1f9b92"
      font-color: "#8af0e8"
      stroke-width: 2
      stroke-dash: 3
      border-radius: 8
    }
  }
  lane: {
    style: {
      fill: "#1a2228"
      stroke: "#1f9b92"
      font-color: "#8af0e8"
      stroke-dash: 3
      border-radius: 8
    }
  }
  repo: {
    shape: rectangle
    style: {
      fill: "#333b42"
      stroke: "#57606a"
      font-color: "#e2e8ec"
      border-radius: 8
    }
  }
  hotfix: {
    shape: rectangle
    style: {
      fill: "#82071e"
      stroke: "#cf222e"
      font-color: "#f4f7f8"
      stroke-width: 2
      3d: true
    }
  }
  init: {
    shape: circle
    width: 26
    style: {
      fill: "#36d0c4"
      stroke: "#8af0e8"
      font-color: "#151b20"
    }
  }
  final: {
    shape: circle
    style: {
      fill: "#151b20"
      stroke: "#36d0c4"
      font-color: "#8af0e8"
      stroke-width: 3
      multiple: true
    }
  }
  flow: {
    style: {
      stroke: "#36d0c4"
      font-color: "#e2e8ec"
      stroke-width: 2
    }
  }
  promote: {
    style: {
      stroke: "#36d0c4"
      font-color: "#8af0e8"
      stroke-width: 3
      bold: true
    }
  }
  optional: {
    style: {
      stroke: "#57606a"
      font-color: "#8a929c"
      stroke-dash: 4
    }
  }
  external: {
    style: {
      stroke: "#36d0c4"
      font-color: "#8af0e8"
      stroke-width: 3
      bold: true
    }
  }
  notify: {
    style: {
      stroke: "#57606a"
      font-color: "#8a929c"
      stroke-dash: 4
    }
  }
  hotfix_edge: {
    style: {
      stroke: "#cf222e"
      font-color: "#cf222e"
      stroke-dash: 4
    }
  }
}

style.fill: "#151b20"
`

// Emit renders vm as D2 source. A start/end-bracketed state model (the env
// projection) and a directed-graph model (jobs, stages, cross-repo) share one
// emit path because D2 expresses both as nodes and connections; the node and
// edge classes carry the visual distinction. When the model declares groups (the
// cross-repo projection's per-repo lanes) each group is rendered as a D2
// container and edge endpoints are qualified with their container path. theme is
// recorded in a leading comment but does not recolor the branded palette. A
// title option, if set, is emitted as a leading comment.
func (D2Emitter) Emit(vm ViewModel, theme Theme, opts ...Option) (string, error) {
	o := applyOptions(opts)

	var b strings.Builder

	if o.Title != "" {
		fmt.Fprintf(&b, "# %s\n", d2Comment(o.Title))
	}
	if theme.Name != "" {
		fmt.Fprintf(&b, "# theme: %s\n", d2Comment(theme.Name))
	}

	b.WriteString(d2Header)
	b.WriteString("\n")

	// container maps each grouped node id to the group id that holds it, so an
	// edge endpoint can be qualified with its container path. An ungrouped node
	// (or any node when the model has no groups) resolves to a bare id.
	container := make(map[string]string, len(vm.Nodes))
	for _, g := range vm.Groups {
		for _, id := range g.NodeIDs {
			container[id] = g.ID
		}
	}

	byID := make(map[string]Node, len(vm.Nodes))
	for _, n := range vm.Nodes {
		byID[n.ID] = n
	}

	if err := writeD2Nodes(&b, vm, byID, container); err != nil {
		return "", err
	}
	if err := writeD2Edges(&b, vm, container); err != nil {
		return "", err
	}

	return b.String(), nil
}

// writeD2Nodes declares the model's nodes. Grouped nodes are wrapped in a D2
// container per lane (in group order, then member order) so the cross-repo
// projection reads a lane per repo; any ungrouped node is declared flat after
// the lanes. Without groups every node is declared flat in model order.
func writeD2Nodes(b *strings.Builder, vm ViewModel, byID map[string]Node, container map[string]string) error {
	if len(vm.Groups) == 0 {
		for _, n := range vm.Nodes {
			writeD2Node(b, n, "")
		}
		return nil
	}

	for _, g := range vm.Groups {
		fmt.Fprintf(b, "%s: %s {\n", d2ID(g.ID), d2Label(g.Label))
		b.WriteString("  class: lane\n")
		for _, id := range g.NodeIDs {
			n, ok := byID[id]
			if !ok {
				return fmt.Errorf("visualize: d2: group %q references unknown node %q", g.ID, id)
			}
			writeD2Node(b, n, "  ")
		}
		b.WriteString("}\n")
	}

	for _, n := range vm.Nodes {
		if _, grouped := container[n.ID]; !grouped {
			writeD2Node(b, n, "")
		}
	}
	return nil
}

// writeD2Node declares one node with the class its kind selects, indented by
// indent (deeper inside a container). The label is quoted so a display name with
// spaces or punctuation cannot break the declaration.
func writeD2Node(b *strings.Builder, n Node, indent string) {
	fmt.Fprintf(b, "%s%s: %s {class: %s}\n", indent, d2ID(n.ID), d2Label(n.Label), nodeClass(n.Kind))
}

// writeD2Edges declares the model's connections in model order. Each endpoint is
// resolved to its container-qualified path so a cross-repo edge crosses lanes
// correctly, and the edge's kind selects a class so promotion, optional,
// divergence, external, and notify edges read distinctly. A labeled edge carries
// its caption as a quoted string.
func writeD2Edges(b *strings.Builder, vm ViewModel, container map[string]string) error {
	for _, e := range vm.Edges {
		class, ok := edgeClass(e.Kind)
		if !ok {
			return fmt.Errorf("visualize: d2: unknown edge kind %q", e.Kind)
		}
		from := d2Path(e.From, container)
		to := d2Path(e.To, container)
		if e.Label != "" {
			fmt.Fprintf(b, "%s -> %s: %s {class: %s}\n", from, to, d2Label(e.Label), class)
		} else {
			fmt.Fprintf(b, "%s -> %s: {class: %s}\n", from, to, class)
		}
	}
	return nil
}

// nodeClass maps a NodeKind to its crucible class name. validate, build, and
// stage share the state slab; deploy is a hexagon invoke; env is a rounded
// dashed plate; hotfix is the red slab; start and end are the entry and terminal
// markers; a repo is an opaque external-repo node. An unknown kind falls back to
// the state slab so an unexpected node still renders rather than breaking the
// diagram.
func nodeClass(kind NodeKind) string {
	switch kind {
	case NodeDeploy:
		return "invoke"
	case NodeEnv:
		return "region"
	case NodeHotfix:
		return "hotfix"
	case NodeStart:
		return "init"
	case NodeEnd:
		return "final"
	case NodeRepo:
		return "repo"
	case NodeValidate, NodeBuild, NodeStage:
		return "state"
	default:
		return "state"
	}
}

// edgeClass maps an EdgeKind to its crucible edge class. Hard, stage, and
// bookend transitions are the plain flow; promote is the bold promotion line;
// optional and notify are thin dashed lines; external is the bold cross-repo
// line; diverge and rejoin are the dashed red hotfix line. The bool is false for
// an unknown kind so the caller can surface a clear error rather than emit an
// unclassed edge.
func edgeClass(kind EdgeKind) (string, bool) {
	switch kind {
	case EdgeHard, EdgeStage, EdgeTransition:
		return "flow", true
	case EdgePromote:
		return "promote", true
	case EdgeOptional:
		return "optional", true
	case EdgeExternal:
		return "external", true
	case EdgeNotify:
		return "notify", true
	case EdgeDiverge, EdgeRejoin:
		return "hotfix_edge", true
	default:
		return "", false
	}
}

// d2Path resolves a node id to its container-qualified reference: a grouped node
// renders as "container.id" so an edge lands inside the right lane, while an
// ungrouped node renders as a bare id.
func d2Path(id string, container map[string]string) string {
	if group, ok := container[id]; ok {
		return d2ID(group) + "." + d2ID(id)
	}
	return d2ID(id)
}

// d2ID sanitizes an identifier into a D2 key: every rune that is not a letter,
// digit, or underscore is folded to an underscore so a hyphenated job id
// (build-app) or a slugged group id stays a single valid key. The human-facing
// name is carried on the label, so only the identity token is folded.
func d2ID(id string) string {
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// d2Label renders a display name as a quoted D2 string. An embedded double quote
// is folded to a single quote and a newline to a space so the label stays on one
// line and cannot terminate the quoted string early. An empty label is emitted
// as an empty quoted string, which D2 renders as an unlabeled shape (used by the
// entry and terminal markers).
func d2Label(label string) string {
	r := strings.NewReplacer(
		`"`, "'",
		"\n", " ",
		"\r", " ",
	)
	return `"` + r.Replace(label) + `"`
}

// d2Comment sanitizes a string for a single-line D2 comment, folding any newline
// to a space so the comment cannot spill onto a second line and change the
// parse.
func d2Comment(s string) string {
	r := strings.NewReplacer("\n", " ", "\r", " ")
	return r.Replace(s)
}
