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

// Emit renders vm as a top-down Mermaid flowchart. Nodes are declared first in
// model order with class-tagged shapes per kind, then hard edges (solid arrows)
// and optional edges (dotted arrows) in model order, so the two dependency
// kinds are visually distinct. theme is accepted for interface conformance and
// future styling; the current output does not vary by theme. A title option, if
// set, is emitted as a Mermaid title in the frontmatter block.
func (MermaidEmitter) Emit(vm ViewModel, _ Theme, opts ...Option) (string, error) {
	o := applyOptions(opts)

	var b strings.Builder

	if o.Title != "" {
		// Mermaid reads a title from a YAML frontmatter block. Quote it so
		// punctuation in the title cannot break the parse.
		b.WriteString("---\n")
		fmt.Fprintf(&b, "title: %q\n", o.Title)
		b.WriteString("---\n")
	}

	b.WriteString("flowchart TD\n")

	for _, n := range vm.Nodes {
		// Node shape per kind: stadium for validate, rectangle for build,
		// subroutine for deploy. The label is bracket-escaped so a display name
		// with brackets cannot terminate the node early.
		open, close := nodeBrackets(n.Kind)
		fmt.Fprintf(&b, "    %s%s%s%s\n", mermaidID(n.ID), open, mermaidLabel(n.Label), close)
	}

	for _, e := range vm.Edges {
		switch e.Kind {
		case EdgeOptional:
			// Dotted arrow marks an ordering-only optional dependency.
			fmt.Fprintf(&b, "    %s -.-> %s\n", mermaidID(e.From), mermaidID(e.To))
		case EdgeHard:
			fmt.Fprintf(&b, "    %s --> %s\n", mermaidID(e.From), mermaidID(e.To))
		default:
			return "", fmt.Errorf("visualize: mermaid: unknown edge kind %q", e.Kind)
		}
	}

	return b.String(), nil
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
