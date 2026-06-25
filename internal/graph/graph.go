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

// Granularity selects which projection of the pipeline the graph renders. Each
// value maps to a distinct view-model build feeding the same emitter: jobs is
// the full job DAG, stages is the coarse lifecycle flow, and env is the
// promotion state machine with its hotfix divergence and rejoin.
type Granularity string

// Granularity values.
const (
	GranularityJobs   Granularity = "jobs"
	GranularityStages Granularity = "stages"
	GranularityEnv    Granularity = "env"
)

// formatMermaid is the only diagram format cascade graph emits today. The flag
// validates against it so a future format is additive rather than a silent
// behavior change.
const formatMermaid = "mermaid"

// defaultThemeName is the theme applied when no theme flag is set. It mirrors
// the visualize package default (the branded cascade palette) so a manifest
// renders styled without a theme flag.
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
	case GranularityJobs, GranularityStages, GranularityEnv:
		// Each granularity maps to a supported projection.
	default:
		return fmt.Errorf("unknown granularity %q: supported values are %q, %q, and %q",
			granularity, GranularityJobs, GranularityStages, GranularityEnv)
	}

	theme, err := resolveTheme(o.Theme)
	if err != nil {
		return err
	}

	configPath := o.ConfigPath
	if configPath == "" {
		configPath = config.FindConfigFile("")
	}
	key := o.ManifestKey
	if key == "" {
		key = config.DefaultManifestKey
	}

	vm, err := buildView(Granularity(granularity), configPath, key)
	if err != nil {
		return err
	}

	diagram, err := visualize.NewMermaidEmitter().Emit(vm, theme)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", format, err)
	}

	if o.JSON {
		return writeJSON(stdout, format, granularity, theme.Name, diagram)
	}

	// The emitter terminates the diagram with a newline, so Fprint avoids an
	// extra blank line while still leaving a clean trailing newline for piping.
	if _, err := fmt.Fprint(stdout, diagram); err != nil {
		return fmt.Errorf("writing diagram: %w", err)
	}
	return nil
}

// resolveTheme turns the --theme value into a concrete theme. An empty value or
// a built-in name (default, cascade, bland) selects a built-in palette; any
// other value is treated as a path to a user-supplied JSON theme file, loaded
// and validated so a malformed file fails fast with a clear error.
func resolveTheme(name string) (visualize.Theme, error) {
	if theme, ok := visualize.LookupTheme(name); ok {
		return theme, nil
	}
	theme, err := visualize.LoadTheme(name)
	if err != nil {
		return visualize.Theme{}, fmt.Errorf("loading theme: %w", err)
	}
	return theme, nil
}

// buildView loads the manifest and projects it into the view model the chosen
// granularity calls for. The jobs and stages projections need only the pipeline
// config; the env projection also needs the per-environment state so it can draw
// hotfix divergence, so it loads the full manifest file rather than just the
// config section.
func buildView(granularity Granularity, configPath, key string) (visualize.ViewModel, error) {
	switch granularity {
	case GranularityEnv:
		file, err := config.ParseManifestFile(configPath, key)
		if err != nil {
			return visualize.ViewModel{}, fmt.Errorf("loading manifest: %w", err)
		}
		vm, err := visualize.BuildEnvViewModel(file.Config, file.State)
		if err != nil {
			return visualize.ViewModel{}, fmt.Errorf("building env view: %w", err)
		}
		return vm, nil
	case GranularityStages:
		cfg, err := config.ParseWithKey(configPath, key)
		if err != nil {
			return visualize.ViewModel{}, fmt.Errorf("loading manifest: %w", err)
		}
		vm, err := visualize.BuildStagesViewModel(cfg)
		if err != nil {
			return visualize.ViewModel{}, fmt.Errorf("building stages view: %w", err)
		}
		return vm, nil
	default:
		cfg, err := config.ParseWithKey(configPath, key)
		if err != nil {
			return visualize.ViewModel{}, fmt.Errorf("loading manifest: %w", err)
		}
		vm, err := visualize.BuildViewModel(generate.BuildDependencyGraph(cfg))
		if err != nil {
			return visualize.ViewModel{}, fmt.Errorf("building graph view: %w", err)
		}
		return vm, nil
	}
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
