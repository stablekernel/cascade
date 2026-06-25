package graph

import (
	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/globals"
)

// NewCommand creates the graph command, a read-only renderer that turns the
// manifest's generated pipeline into a Mermaid diagram on stdout. The verb is
// "graph" because cascade renders a graph; it does not collide with "plan" (the
// textual change preview) or "status" (the live environment view). "visualize"
// was the considered alternative; "graph" is shorter and names the artifact
// rather than the act.
func NewCommand() *cobra.Command {
	var o Options

	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Render the generated pipeline as a Mermaid diagram",
		Long: `Render the manifest's generated pipeline as a diagram on stdout.

graph loads the manifest, builds the same job dependency graph the generator
uses, and emits it as Mermaid that GitHub renders natively in Markdown. Pipe the
output into a file or paste it into a README or pull request to show how the
pipeline's jobs depend on one another. Hard dependencies render as solid arrows
and optional ordering-only dependencies as dotted arrows.

graph is read-only: it never writes files, runs git, or modifies the repo. A
missing or invalid manifest is reported as an error.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.JSON = globals.JSON()
			return Run(o, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVarP(&o.ConfigPath, "config", "c", "", "Path to config file (default: auto-detect .github/manifest.yaml)")
	cmd.Flags().StringVar(&o.ManifestKey, "manifest-key", config.DefaultManifestKey, "Key in manifest file containing CI config")
	cmd.Flags().StringVar(&o.Granularity, "granularity", string(GranularityJobs), "Pipeline projection to render; supported value: jobs")
	cmd.Flags().StringVar(&o.Format, "format", formatMermaid, "Diagram output format; supported value: mermaid")
	cmd.Flags().StringVar(&o.Theme, "theme", defaultThemeName, "Diagram theme; supported value: default")

	return cmd
}
