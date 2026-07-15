package visualize

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// NodeStyle holds the presentation attributes an emitter renders for one node
// kind. Each field maps to a Mermaid classDef attribute (fill, stroke, color);
// an empty field is omitted so a theme can style only the slots it cares about.
type NodeStyle struct {
	// Fill is the node background color, for example "#1f6feb".
	Fill string `json:"fill,omitempty"`
	// Stroke is the node border color.
	Stroke string `json:"stroke,omitempty"`
	// Text is the node label color.
	Text string `json:"text,omitempty"`
}

// isZero reports whether the style carries no attribute worth emitting, so the
// emitter can skip an empty classDef rather than write a no-op line.
func (s NodeStyle) isZero() bool {
	return s.Fill == "" && s.Stroke == "" && s.Text == ""
}

// Theme carries the styling the Mermaid emitter renders into a diagram's header.
// It is render data only: the emitter turns it into an init directive and
// classDef lines, and the view model never sees it, so a theme swap restyles a
// diagram without reshaping the model. The zero value emits an unstyled diagram,
// so the field is always safe to thread through an emit path.
type Theme struct {
	// Name identifies the theme. The built-in values use "cascade" and "bland";
	// a user-supplied theme file sets its own.
	Name string `json:"name"`
	// Base selects the Mermaid base theme rendered in the init directive (for
	// example "base" or "neutral"). Empty omits the directive unless LineColor is
	// set, in which case it falls back to "base".
	Base string `json:"base,omitempty"`
	// LineColor styles edges and transitions through the init directive's
	// themeVariables. Empty leaves edge color to the renderer default.
	LineColor string `json:"lineColor,omitempty"`
	// NodeStyles maps a NodeKind to its classDef style. A kind with no entry, or
	// an empty entry, renders without a class so the renderer default applies.
	NodeStyles map[NodeKind]NodeStyle `json:"nodeStyles,omitempty"`
}

// CascadeTheme is the branded default palette: a distinct accent per node kind
// over a mid-gray edge color. It is the theme cascade graph renders when no
// theme is requested.
var CascadeTheme = Theme{
	Name:      "cascade",
	Base:      "base",
	LineColor: "#57606a",
	NodeStyles: map[NodeKind]NodeStyle{
		NodeValidate: {Fill: "#1f6feb", Stroke: "#0b3d91", Text: "#ffffff"},
		NodeBuild:    {Fill: "#2da44e", Stroke: "#116329", Text: "#ffffff"},
		NodeDeploy:   {Fill: "#8250df", Stroke: "#512a97", Text: "#ffffff"},
		NodeStage:    {Fill: "#bf8700", Stroke: "#7d4e00", Text: "#ffffff"},
		NodeEnv:      {Fill: "#1f6feb", Stroke: "#0b3d91", Text: "#ffffff"},
		NodeHotfix:   {Fill: "#cf222e", Stroke: "#82071e", Text: "#ffffff"},
		NodeRepo:     {Fill: "#6e7781", Stroke: "#424a53", Text: "#ffffff"},
	},
}

// BlandTheme is a monotone grayscale palette for low-distraction or print
// contexts: every node kind sits on a light-gray fill with a gray border, and
// edges are a muted gray. It is intentionally distinct from CascadeTheme.
var BlandTheme = Theme{
	Name:      "bland",
	Base:      "neutral",
	LineColor: "#8c959f",
	NodeStyles: map[NodeKind]NodeStyle{
		NodeValidate: {Fill: "#f6f8fa", Stroke: "#6e7781", Text: "#24292f"},
		NodeBuild:    {Fill: "#eaeef2", Stroke: "#6e7781", Text: "#24292f"},
		NodeDeploy:   {Fill: "#d0d7de", Stroke: "#57606a", Text: "#24292f"},
		NodeStage:    {Fill: "#eaeef2", Stroke: "#6e7781", Text: "#24292f"},
		NodeEnv:      {Fill: "#f6f8fa", Stroke: "#6e7781", Text: "#24292f"},
		NodeHotfix:   {Fill: "#d0d7de", Stroke: "#57606a", Text: "#24292f"},
		NodeRepo:     {Fill: "#d0d7de", Stroke: "#57606a", Text: "#24292f"},
	},
}

// DefaultTheme is the theme used when a caller does not request one. It is the
// branded cascade palette, so an unparameterized emit is styled by default.
var DefaultTheme = CascadeTheme

// LookupTheme returns the built-in theme registered under name. "default" is an
// alias for the cascade theme so existing callers keep working. The bool is
// false for any name that is not a built-in, signalling the caller to treat the
// value as a theme-file path instead.
func LookupTheme(name string) (Theme, bool) {
	switch name {
	case "", "default", CascadeTheme.Name:
		return CascadeTheme, true
	case BlandTheme.Name:
		return BlandTheme, true
	default:
		return Theme{}, false
	}
}

// LoadTheme reads a user-supplied theme definition from a JSON file and returns
// it. It surfaces a clear error when the file cannot be read, does not parse as
// JSON, or omits the required name, so a malformed theme fails fast rather than
// rendering a silently broken diagram.
func LoadTheme(path string) (Theme, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Theme{}, fmt.Errorf("reading theme file %q: %w", path, err)
	}

	var theme Theme
	if err := json.Unmarshal(data, &theme); err != nil {
		return Theme{}, fmt.Errorf("parsing theme file %q: %w", path, err)
	}
	if theme.Name == "" {
		return Theme{}, fmt.Errorf("theme file %q: missing required field %q", path, "name")
	}
	return theme, nil
}

// className renders a node kind as its Mermaid classDef name. The "node_" prefix
// keeps the class name from colliding with a node id of the same text (a node
// named "validate" styled by class "node_validate").
func className(kind NodeKind) string {
	return "node_" + string(kind)
}

// classDefBody renders a NodeStyle as the attribute list of a Mermaid classDef,
// emitting only the set fields in a fixed order so the output is deterministic.
// Values are sanitized before splicing: a user-theme color carrying a comma,
// semicolon, or newline would otherwise smuggle extra classDef attributes or
// whole statements into the diagram.
func classDefBody(s NodeStyle) string {
	parts := make([]string, 0, 3)
	if s.Fill != "" {
		parts = append(parts, "fill:"+classDefValue(s.Fill))
	}
	if s.Stroke != "" {
		parts = append(parts, "stroke:"+classDefValue(s.Stroke))
	}
	if s.Text != "" {
		parts = append(parts, "color:"+classDefValue(s.Text))
	}
	return joinComma(parts)
}

// classDefValueReplacer strips the runes that act as classDef separators (comma
// between attributes, semicolon and newline between statements) from a style
// value, so a theme file controls only the one attribute it names.
var classDefValueReplacer = strings.NewReplacer(",", "", ";", "", "\n", "", "\r", "")

func classDefValue(v string) string {
	return classDefValueReplacer.Replace(v)
}

// joinComma joins parts with a comma. It exists so classDefBody stays free of an
// import for a single call and keeps the attribute separator in one place.
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}
