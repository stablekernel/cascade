package visualize

import (
	"fmt"
	"strings"
)

// MermaidEmitter renders a ViewModel as Mermaid flowchart source that GitHub
// renders natively in Markdown. It is the first concrete Emitter. Output is
// deterministic: it ranges the model's ordered slices and never iterates a map,
// so the same model always yields byte-identical source.
type MermaidEmitter struct{}

// NewMermaidEmitter returns a ready MermaidEmitter. The type is stateless, so
// the constructor exists only to give callers a stable construction point.
func NewMermaidEmitter() *MermaidEmitter { return &MermaidEmitter{} }

// compile-time assertion that MermaidEmitter satisfies the Emitter seam.
var _ Emitter = (*MermaidEmitter)(nil)

// Emit renders vm as Mermaid source. The diagram family follows the model's
// Kind: a state machine (env projection) becomes a stateDiagram-v2, every other
// model becomes a top-down flowchart. theme styles the output: it is rendered
// into a leading init directive and trailing classDef and class lines, so a
// theme swap restyles the header block while leaving the structure unchanged. A
// title option, if set, is emitted as a Mermaid title in the frontmatter block.
func (MermaidEmitter) Emit(vm ViewModel, theme Theme, opts ...Option) (string, error) {
	o := applyOptions(opts)

	var b strings.Builder

	if o.Title != "" {
		// Mermaid reads a title from a YAML frontmatter block. Quote it so
		// punctuation in the title cannot break the parse. The block must be the
		// first content, ahead of any init directive.
		b.WriteString("---\n")
		fmt.Fprintf(&b, "title: %q\n", o.Title)
		b.WriteString("---\n")
	}

	// The init directive carries the theme's base and edge color. It sits after
	// the frontmatter and before the diagram keyword, where Mermaid reads it.
	writeThemeInit(&b, theme)

	if vm.Kind == DiagramState {
		return emitState(&b, vm, theme)
	}
	return emitFlowchart(&b, vm, theme)
}

// emitFlowchart renders a directed-graph model (jobs and stages) as a top-down
// flowchart. Nodes are declared first in model order with a shape per kind, then
// edges in model order, then the theme's class styling, so dependency or stage
// flow reads in a stable order and a reader can tell the node kinds apart.
func emitFlowchart(b *strings.Builder, vm ViewModel, theme Theme) (string, error) {
	if err := writeFlowchartNodes(b, vm); err != nil {
		return "", err
	}

	for _, e := range vm.Edges {
		switch e.Kind {
		case EdgeOptional:
			// Dotted arrow marks an ordering-only optional dependency.
			fmt.Fprintf(b, "    %s -.-> %s\n", mermaidID(e.From), mermaidID(e.To))
		case EdgeHard, EdgeStage:
			fmt.Fprintf(b, "    %s --> %s\n", mermaidID(e.From), mermaidID(e.To))
		case EdgeExternal:
			// Thick labeled arrow marks the primary coordinating a dependent repo,
			// styled distinctly from the intra-repo solid and dotted edges.
			writeLabeledEdge(b, "%s ==> %s", "%s ==>|%s| %s", e)
		case EdgeNotify:
			// Dotted arrow with an inline caption marks a satellite notifying its
			// primary, reading back up the cross-repo flow.
			writeLabeledEdge(b, "%s -.-> %s", "%s -. %s .-> %s", e)
		default:
			return "", fmt.Errorf("visualize: mermaid: unknown flowchart edge kind %q", e.Kind)
		}
	}

	writeThemeClasses(b, vm, theme)

	return b.String(), nil
}

// writeFlowchartNodes declares the model's nodes. When the model carries groups,
// grouped nodes are wrapped in a Mermaid subgraph per lane (in group order, then
// member order) and any ungrouped node is declared after the lanes; without
// groups every node is declared flat in model order, matching the original
// single-repo flowchart output.
func writeFlowchartNodes(b *strings.Builder, vm ViewModel) error {
	b.WriteString("flowchart TD\n")

	if len(vm.Groups) == 0 {
		for _, n := range vm.Nodes {
			writeFlowchartNode(b, n, "    ")
		}
		return nil
	}

	byID := make(map[string]Node, len(vm.Nodes))
	for _, n := range vm.Nodes {
		byID[n.ID] = n
	}

	grouped := make(map[string]bool, len(vm.Nodes))
	for _, g := range vm.Groups {
		fmt.Fprintf(b, "    subgraph %s[%s]\n", mermaidID(g.ID), mermaidSubgraphLabel(g.Label))
		for _, id := range g.NodeIDs {
			n, ok := byID[id]
			if !ok {
				return fmt.Errorf("visualize: mermaid: group %q references unknown node %q", g.ID, id)
			}
			writeFlowchartNode(b, n, "        ")
			grouped[id] = true
		}
		b.WriteString("    end\n")
	}

	for _, n := range vm.Nodes {
		if !grouped[n.ID] {
			writeFlowchartNode(b, n, "    ")
		}
	}
	return nil
}

// writeFlowchartNode declares one node with the shape its kind selects, indented
// by indent (deeper inside a subgraph). The label is escaped so a display name
// with brackets cannot terminate the node early.
func writeFlowchartNode(b *strings.Builder, n Node, indent string) {
	open, close := nodeBrackets(n.Kind)
	fmt.Fprintf(b, "%s%s%s%s%s\n", indent, mermaidID(n.ID), open, mermaidLabel(n.Label), close)
}

// writeLabeledEdge emits a flowchart edge, choosing the plain format when the
// edge has no caption and the labeled format otherwise, so cross-repo edges read
// with their deploy or notify caption without a stray empty label segment.
func writeLabeledEdge(b *strings.Builder, plain, labeled string, e Edge) {
	if e.Label == "" {
		fmt.Fprintf(b, "    "+plain+"\n", mermaidID(e.From), mermaidID(e.To))
		return
	}
	fmt.Fprintf(b, "    "+labeled+"\n", mermaidID(e.From), mermaidEdgeLabel(e.Label), mermaidID(e.To))
}

// emitState renders a state-machine model (the env projection) as a
// stateDiagram-v2. Start and end markers render as Mermaid's [*] pseudo-state
// rather than declared states; every other node is declared with a renamed
// label when its label differs from its id. Transitions follow model order and
// carry their optional caption, so promote, diverge, and rejoin read distinctly.
func emitState(b *strings.Builder, vm ViewModel, theme Theme) (string, error) {
	b.WriteString("stateDiagram-v2\n")

	// Map each node id to the token it renders as, so edges can resolve a start
	// or end marker to [*] without the builder ever encoding diagram syntax.
	token := make(map[string]string, len(vm.Nodes))
	for _, n := range vm.Nodes {
		switch n.Kind {
		case NodeStart, NodeEnd:
			token[n.ID] = "[*]"
		default:
			token[n.ID] = mermaidID(n.ID)
		}
	}

	for _, n := range vm.Nodes {
		if n.Kind == NodeStart || n.Kind == NodeEnd {
			continue // pseudo-states are referenced as [*], never declared
		}
		// A state whose label matches its id needs no rename; otherwise declare
		// the display name with a quoted alias so labels with punctuation are safe.
		if n.Label != "" && n.Label != n.ID {
			fmt.Fprintf(b, "    state %s as %s\n", mermaidStateLabel(n.Label), mermaidID(n.ID))
		}
	}

	for _, e := range vm.Edges {
		from, ok := token[e.From]
		if !ok {
			return "", fmt.Errorf("visualize: mermaid: transition from undeclared state %q", e.From)
		}
		to, ok := token[e.To]
		if !ok {
			return "", fmt.Errorf("visualize: mermaid: transition to undeclared state %q", e.To)
		}
		if e.Label != "" {
			fmt.Fprintf(b, "    %s --> %s : %s\n", from, to, mermaidTransitionLabel(e.Label))
		} else {
			fmt.Fprintf(b, "    %s --> %s\n", from, to)
		}
	}

	writeThemeClasses(b, vm, theme)

	return b.String(), nil
}

// writeThemeInit emits the Mermaid init directive carrying the theme's base and
// edge color. It writes nothing when the theme sets neither, so an unstyled
// theme produces no directive. The directive must follow any frontmatter block
// and precede the diagram keyword.
func writeThemeInit(b *strings.Builder, theme Theme) {
	if theme.Base == "" && theme.LineColor == "" {
		return
	}
	base := theme.Base
	if base == "" {
		base = "base"
	}
	fmt.Fprintf(b, "%%%%{init: {%q: %q", "theme", base)
	if theme.LineColor != "" {
		fmt.Fprintf(b, ", %q: {%q: %q}", "themeVariables", "lineColor", theme.LineColor)
	}
	b.WriteString("}}%%\n")
}

// writeThemeClasses emits the theme's per-kind classDef lines followed by the
// class assignments that bind each node to its kind's class. Kinds appear in
// first-encounter model order so the output is deterministic, pseudo-states
// (start and end) are skipped because they render as [*] rather than declared
// nodes, and a kind with no style is left unclassed so the renderer default
// applies. It writes nothing when the theme defines no node styles.
func writeThemeClasses(b *strings.Builder, vm ViewModel, theme Theme) {
	if len(theme.NodeStyles) == 0 {
		return
	}

	var order []NodeKind
	members := make(map[NodeKind][]string)
	for _, n := range vm.Nodes {
		if n.Kind == NodeStart || n.Kind == NodeEnd {
			continue
		}
		style, ok := theme.NodeStyles[n.Kind]
		if !ok || style.isZero() {
			continue
		}
		if _, seen := members[n.Kind]; !seen {
			order = append(order, n.Kind)
		}
		members[n.Kind] = append(members[n.Kind], mermaidID(n.ID))
	}

	for _, k := range order {
		fmt.Fprintf(b, "    classDef %s %s\n", className(k), classDefBody(theme.NodeStyles[k]))
	}
	for _, k := range order {
		fmt.Fprintf(b, "    class %s %s\n", strings.Join(members[k], ","), className(k))
	}
}

// nodeBrackets returns the opening and closing Mermaid shape delimiters for a
// node kind. Distinct shapes let a reader tell validate, build, and deploy jobs
// apart at a glance.
func nodeBrackets(kind NodeKind) (string, string) {
	switch kind {
	case NodeValidate:
		return "([", "])" // stadium
	case NodeDeploy:
		return "[[", "]]" // subroutine
	case NodeStage:
		return "(", ")" // rounded
	case NodeBuild:
		return "[", "]" // rectangle
	default:
		return "[", "]"
	}
}

// mermaidID sanitizes a job ID into a Mermaid node identifier. Cascade job IDs
// are already prefixed slugs (validate, build-app), so only the hyphen needs
// folding to an underscore to stay inside Mermaid's identifier rules.
func mermaidID(id string) string {
	return strings.ReplaceAll(id, "-", "_")
}

// mermaidStateLabel renders a state's display name as a quoted Mermaid string
// for the `state "name" as id` rename form, escaping any embedded quote so the
// alias declaration cannot be broken by punctuation in the name.
func mermaidStateLabel(label string) string {
	return `"` + strings.ReplaceAll(label, `"`, "&quot;") + `"`
}

// mermaidTransitionLabel sanitizes a transition caption. A colon would otherwise
// be read as a second label separator and a newline would split the transition,
// so both are folded to keep the caption on one line and inside one segment.
func mermaidTransitionLabel(label string) string {
	r := strings.NewReplacer(
		":", "&#58;",
		"\n", " ",
		"\r", " ",
	)
	return r.Replace(label)
}

// mermaidSubgraphLabel renders a lane caption as a quoted Mermaid string for the
// `subgraph id["label"]` form, escaping any embedded quote so a repo name with
// punctuation cannot break the subgraph header.
func mermaidSubgraphLabel(label string) string {
	return `"` + strings.ReplaceAll(label, `"`, "&quot;") + `"`
}

// mermaidEdgeLabel sanitizes a flowchart edge caption. A pipe would close the
// `|label|` segment early and brackets or quotes confuse the parser, so each is
// folded to an entity Mermaid renders verbatim, keeping the caption on one line.
func mermaidEdgeLabel(label string) string {
	r := strings.NewReplacer(
		"|", "&#124;",
		"[", "&#91;",
		"]", "&#93;",
		`"`, "&quot;",
		"\n", " ",
		"\r", " ",
	)
	return r.Replace(label)
}

// mermaidLabel escapes a display label for use inside a Mermaid node shape.
// Brackets would otherwise close the shape early and quotes would confuse the
// parser, so both are replaced with HTML entities Mermaid renders verbatim.
func mermaidLabel(label string) string {
	r := strings.NewReplacer(
		"[", "&#91;",
		"]", "&#93;",
		"(", "&#40;",
		")", "&#41;",
		`"`, "&quot;",
	)
	return r.Replace(label)
}
