// Package graph drives the cascade visualizer from the command line. It loads a
// manifest, builds the generated-pipeline dependency graph, projects it into a
// render-agnostic view model, and emits a diagram to an output stream. The
// package owns only the orchestration and flag contract; the projection and the
// diagram syntax live in internal/visualize, so a richer renderer or projection
// is added there without reshaping this command.
package graph

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
	"github.com/stablekernel/cascade/internal/visualize"
)

// Granularity selects which projection of the pipeline the graph renders. The
// job DAG is the only projection that exists today; env and stage rollups are
// recognized values that Run rejects with a clear message until their
// projections land, so the flag contract is stable as the projections grow.
type Granularity string

// Granularity values. Only GranularityJobs produces a diagram today.
const (
	GranularityJobs   Granularity = "jobs"
	GranularityStages Granularity = "stages"
	GranularityEnv    Granularity = "env"
)

// formatMermaid is the only diagram format cascade graph emits today. The flag
// validates against it so a future format is additive rather than a silent
// behavior change.
const formatMermaid = "mermaid"

// defaultThemeName is the only theme available today. It mirrors the visualize
// package default so a manifest renders without a theme flag.
var defaultThemeName = visualize.DefaultTheme.Name

// Options carries the inputs to a graph render. ConfigPath and ManifestKey
// locate the manifest the same way the sibling read-only commands do. The empty
// value of each presentation field selects its default, so Run is usable
// directly in tests without a cobra layer applying defaults.
type Options struct {
	// ConfigPath is the manifest path. Empty auto-detects .github/manifest.yaml.
	ConfigPath string
	// ManifestKey is the top-level manifest key holding the CI config. Empty
	// selects the default key.
	ManifestKey string
	// Granularity selects the projection. Empty selects jobs.
	Granularity string
	// Format selects the diagram syntax. Empty selects mermaid.
	Format string
	// Theme selects diagram styling. Empty selects the default theme.
	Theme string
	// JSON, when true, wraps the diagram string in a JSON envelope instead of
	// printing the raw diagram, so callers piping structured output get the
	// format, granularity, theme, and diagram in one object.
	JSON bool
}

// Run renders the manifest's pipeline graph to stdout. It validates the
// presentation flags before touching the filesystem so an unsupported format,
// granularity, or theme fails fast with a clear message and writes nothing. A
// missing or invalid manifest, a cyclic graph, or a render failure surfaces as a
// wrapped error; a well-formed manifest always emits.
func Run(o Options, stdout io.Writer) error {
	format := o.Format
	if format == "" {
		format = formatMermaid
	}
	if format != formatMermaid {
		return fmt.Errorf("unsupported format %q: only %q is supported", format, formatMermaid)
	}

	granularity := o.Granularity
	if granularity == "" {
		granularity = string(GranularityJobs)
	}
	switch Granularity(granularity) {
	case GranularityJobs:
		// The job DAG is the implemented projection.
	case GranularityStages, GranularityEnv:
		return fmt.Errorf("granularity %q is not yet available: only %q is supported", granularity, GranularityJobs)
	default:
		return fmt.Errorf("unknown granularity %q: supported value is %q", granularity, GranularityJobs)
	}

	theme := o.Theme
	if theme == "" {
		theme = defaultThemeName
	}
	if theme != defaultThemeName {
		return fmt.Errorf("unknown theme %q: only %q is available", theme, defaultThemeName)
	}

	configPath := o.ConfigPath
	if configPath == "" {
		configPath = config.FindConfigFile("")
	}
	key := o.ManifestKey
	if key == "" {
		key = config.DefaultManifestKey
	}

	cfg, err := config.ParseWithKey(configPath, key)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}

	vm, err := visualize.BuildViewModel(generate.BuildDependencyGraph(cfg))
	if err != nil {
		return fmt.Errorf("building graph view: %w", err)
	}

	diagram, err := visualize.NewMermaidEmitter().Emit(vm, visualize.DefaultTheme)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", format, err)
	}

	if o.JSON {
		return writeJSON(stdout, format, granularity, theme, diagram)
	}

	// The emitter terminates the diagram with a newline, so Fprint avoids an
	// extra blank line while still leaving a clean trailing newline for piping.
	if _, err := fmt.Fprint(stdout, diagram); err != nil {
		return fmt.Errorf("writing diagram: %w", err)
	}
	return nil
}

// writeJSON emits the diagram wrapped in a structured envelope so a caller can
// consume the format, granularity, theme, and diagram source together.
func writeJSON(stdout io.Writer, format, granularity, theme, diagram string) error {
	payload := struct {
		Format      string `json:"format"`
		Granularity string `json:"granularity"`
		Theme       string `json:"theme"`
		Diagram     string `json:"diagram"`
	}{
		Format:      format,
		Granularity: granularity,
		Theme:       theme,
		Diagram:     diagram,
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("encoding json output: %w", err)
	}
	return nil
}
