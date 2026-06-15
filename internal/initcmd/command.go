// Package initcmd implements the "cascade init" command, which scaffolds a new
// project: it renders a starter manifest and matching reusable-workflow stubs,
// self-checks them through the real generator, and writes them to disk so a
// repository can adopt cascade with a working configuration on the first try.
package initcmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/scaffold"
)

// defaultTopology is the preset used when neither --topology nor --envs is
// given. A two-environment pipeline is the most common starting point.
const defaultTopology = "two-env"

// options holds the resolved inputs for a single init invocation.
type options struct {
	topology   string
	envs       string
	name       string
	dir        string
	cliVersion string
	force      bool
	dryRun     bool
}

// NewCommand creates the "init" command.
func NewCommand() *cobra.Command {
	opts := options{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a starter manifest and callback workflow stubs",
		Long: `Scaffold a new cascade project.

init renders a starter .github/manifest.yaml plus matching reusable-workflow
stubs for your build and deploy callbacks, verifies them through the real
generator, and writes them into your repository. The generated manifest carries
a $schema directive so editors give you autocomplete and validation while you
fill in the stubs.

Choose a shape with --topology (a named preset) or --envs (your own ordered
environment names). Environment names are positional: the last one is the
release stage. When the environment list is empty the project is release-only,
with no deploy stub.

Examples:
  # Two-environment pipeline (dev, prod) in the current directory
  cascade init --topology two-env

  # Custom ordered environments; "production" is the release stage
  cascade init --envs staging,production --name my-service

  # Preview what would be written without touching the filesystem
  cascade init --topology three-env --dry-run

  # Overwrite existing files
  cascade init --topology two-env --force`,
		Args: cobra.NoArgs,
		// Runtime failures (refuse-on-existing, scaffold self-check) are not
		// usage errors, so suppress the usage dump on RunE error.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.OutOrStdout(), opts)
		},
	}

	presets := strings.Join(topologyNames(), ", ")
	cmd.Flags().StringVar(&opts.topology, "topology", "", "Preset environment shape; one of: "+presets+" (default \""+defaultTopology+"\")")
	cmd.Flags().StringVar(&opts.envs, "envs", "", "Comma-separated ordered environment names; the last is the release stage (overrides --topology)")
	cmd.Flags().StringVar(&opts.name, "name", "", "Project name woven into the stubs (default: target directory base name)")
	cmd.Flags().StringVar(&opts.dir, "dir", ".", "Target directory to scaffold into")
	cmd.Flags().StringVar(&opts.cliVersion, "cli-version", "", "cascade CLI version pinned in the manifest (default: the built-in pinned release)")
	cmd.Flags().BoolVarP(&opts.force, "force", "f", false, "Overwrite existing files")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Print what would be written without writing anything")

	return cmd
}

// run resolves inputs, scaffolds (which self-checks before returning), then
// either previews or writes the result. Nothing is written when scaffolding
// fails, when an existing file would be overwritten without --force, or in
// dry-run mode.
func run(out io.Writer, opts options) error {
	envs, err := resolveEnvs(opts.topology, opts.envs)
	if err != nil {
		return err
	}

	name, err := resolveName(opts.name, opts.dir)
	if err != nil {
		return err
	}

	// Scaffold self-checks (parse + validate + generate) before returning, so a
	// broken scaffold never reaches the filesystem.
	files, err := scaffold.Scaffold(name, "main", envs, scaffold.WithCLIVersion(opts.cliVersion))
	if err != nil {
		return fmt.Errorf("scaffolding %q: %w", name, err)
	}

	paths := sortedPaths(files)

	if opts.dryRun {
		return writeReport(out, dryRunReport(opts.dir, name, envs, paths))
	}

	if !opts.force {
		if conflicts := existingFiles(opts.dir, paths); len(conflicts) > 0 {
			return fmt.Errorf(
				"refusing to overwrite existing files (use --force to overwrite):\n  %s",
				strings.Join(conflicts, "\n  "),
			)
		}
	}

	if err := writeFiles(opts.dir, files); err != nil {
		return err
	}

	return writeReport(out, summaryReport(opts.dir, name, envs, paths))
}

// writeReport writes a fully-rendered report to out, surfacing any write error.
func writeReport(out io.Writer, report string) error {
	if _, err := io.WriteString(out, report); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	return nil
}

// resolveEnvs turns the --topology / --envs flags into an ordered environment
// list. --envs wins when set; otherwise the named (or default) preset is used.
func resolveEnvs(topology, envs string) ([]string, error) {
	if strings.TrimSpace(envs) != "" {
		parts := strings.Split(envs, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("--envs was set but contained no environment names")
		}
		return out, nil
	}

	name := topology
	if name == "" {
		name = defaultTopology
	}
	presets := scaffold.Topologies()
	list, ok := presets[name]
	if !ok {
		return nil, fmt.Errorf("unknown topology %q; valid presets: %s", name, strings.Join(topologyNames(), ", "))
	}
	// Return a copy so callers cannot mutate the preset slice.
	return append([]string(nil), list...), nil
}

// resolveName returns the project name, defaulting to the base name of the
// resolved target directory.
func resolveName(name, dir string) (string, error) {
	if strings.TrimSpace(name) != "" {
		return name, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolving target directory %q: %w", dir, err)
	}
	base := filepath.Base(abs)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "", fmt.Errorf("could not derive a project name from %q; pass --name", dir)
	}
	return base, nil
}

// topologyNames returns the preset names in a stable order for help text and
// error messages.
func topologyNames() []string {
	names := make([]string, 0, len(scaffold.Topologies()))
	for n := range scaffold.Topologies() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// sortedPaths returns the file paths of a scaffold output in stable order.
func sortedPaths(files map[string]string) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// existingFiles returns the subset of paths (joined under dir) that already
// exist on disk.
func existingFiles(dir string, paths []string) []string {
	var conflicts []string
	for _, p := range paths {
		abs := filepath.Join(dir, p)
		if _, err := os.Stat(abs); err == nil {
			conflicts = append(conflicts, abs)
		}
	}
	return conflicts
}

// writeFiles materializes the scaffold output under dir, creating parent
// directories as needed.
func writeFiles(dir string, files map[string]string) error {
	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return fmt.Errorf("creating directory for %q: %w", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing %q: %w", abs, err)
		}
	}
	return nil
}

// shape describes the chosen environment list for human-readable output.
func shape(envs []string) string {
	if len(envs) == 0 {
		return "release-only (no environments)"
	}
	return strings.Join(envs, " -> ")
}

// dryRunReport renders what init would write, without touching the filesystem.
func dryRunReport(dir, name string, envs, paths []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dry run: would scaffold project %q in %q\n", name, dir)
	fmt.Fprintf(&b, "Environments: %s\n", shape(envs))
	b.WriteString("Files that would be written:\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "  %s\n", filepath.Join(dir, p))
	}
	b.WriteString("No files were written.\n")
	return b.String()
}

// summaryReport renders what init created and the recommended next steps.
func summaryReport(dir, name string, envs, paths []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Scaffolded project %q in %q\n", name, dir)
	fmt.Fprintf(&b, "Environments: %s\n", shape(envs))
	b.WriteString("Created:\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "  %s\n", filepath.Join(dir, p))
	}
	b.WriteString("\n")
	b.WriteString("The manifest already carries a $schema directive, so editors give you\n")
	b.WriteString("autocomplete and validation as you edit it.\n")
	b.WriteString("\n")
	b.WriteString("Next steps:\n")
	b.WriteString("  1. Review and fill in the stub callback workflows under .github/workflows.\n")
	b.WriteString("  2. Commit the manifest and workflows.\n")
	b.WriteString("  3. Run 'cascade generate-workflow' to produce the orchestration workflows.\n")
	return b.String()
}
