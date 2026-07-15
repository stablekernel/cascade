package pinreconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
)

// Options configures a user-repo Run. ManifestPath defaults to
// "<Root>/.github/manifest.yaml" when empty. ManifestKey defaults to
// config.DefaultManifestKey. ChangedFiles lists the paths (relative to Root, or
// absolute) of the source workflow files that triggered this reconcile; each is
// scanned line by line for governed "uses:" refs. These are the only files ever
// read as a ref SOURCE: every file the manifest itself produces is exclusively
// a regenerate TARGET and is never read back, so generation stays a pure
// offline function of the manifest.
type Options struct {
	Root         string
	ManifestPath string
	ManifestKey  string
	ChangedFiles []string
}

// Run adopts a governed pin bump observed in opts.ChangedFiles into the user
// manifest's action_pins (verbatim, via SetActionPinSurgical) and regenerates
// every workflow the manifest produces so it agrees again. It reports whether
// anything actually changed; a converged tree is a safe no-op, which is the
// loop-termination guarantee a reconcile companion relies on.
func Run(opts Options) (bool, error) {
	manifestPath := resolveManifestPath(opts.Root, opts.ManifestPath)
	manifestKey := opts.ManifestKey
	if manifestKey == "" {
		manifestKey = config.DefaultManifestKey
	}

	sourceRefs, err := scanChangedFiles(opts.Root, opts.ChangedFiles)
	if err != nil {
		return false, err
	}

	governed, err := governedActions()
	if err != nil {
		return false, err
	}

	adopts, err := PlanAdoptions(Input{Governed: governed, SourceRefs: sourceRefs})
	if err != nil {
		return false, err
	}
	if !adopts.Relevant() {
		return false, nil
	}

	cfg, err := config.ParseWithKey(manifestPath, manifestKey)
	if err != nil {
		return false, fmt.Errorf("parsing manifest: %w", err)
	}

	changed := false
	for action, ref := range adopts.Pins {
		if cfg.ActionPins[action] != ref {
			changed = true
			break
		}
	}
	if !changed {
		return false, nil
	}

	doc, err := os.ReadFile(manifestPath) //nolint:gosec // caller-provided repo manifest path.
	if err != nil {
		return false, fmt.Errorf("reading manifest: %w", err)
	}
	for action, ref := range adopts.Pins {
		doc, err = SetActionPinSurgical(doc, action, ref)
		if err != nil {
			return false, fmt.Errorf("adopting %s: %w", action, err)
		}
	}

	if err := os.WriteFile(manifestPath, doc, 0o644); err != nil { //nolint:gosec // manifest is not a secret.
		return false, fmt.Errorf("writing manifest: %w", err)
	}

	if err := regenerate(manifestPath, manifestKey, ""); err != nil {
		return false, err
	}

	return true, nil
}

// resolveManifestPath returns the manifest path Run and RunOwnRepo read and
// write, and the value threaded through to generate.Plan as its ConfigPath. A
// caller-supplied manifestPath (an explicit --config) is used verbatim. When
// it is unset, the default resolves to an absolute path anchored at the
// process working directory (matching root when root is relative), rather
// than a root-relative join, so the file reads and writes above stay correct
// regardless of the process working directory. The spelling never reaches the
// generated files: every generator rebases the manifest path to repo-relative
// (generate.relativeManifestPath) before embedding it, so emitted output is
// byte-identical for relative, absolute, and auto-detected paths.
func resolveManifestPath(root, manifestPath string) string {
	if manifestPath != "" {
		return manifestPath
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	return filepath.Join(absRoot, ".github", "manifest.yaml")
}

// scanChangedFiles reads every changed file (resolved against root when not
// already absolute) and extracts every governed "uses:" line it carries into a
// source-ref set keyed by action path, one entry per ref observed.
func scanChangedFiles(root string, changedFiles []string) (map[string][]string, error) {
	refs := make(map[string][]string)
	for _, rel := range changedFiles {
		path := rel
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, rel)
		}
		content, err := os.ReadFile(path) //nolint:gosec // caller-provided repo-relative path.
		if err != nil {
			return nil, fmt.Errorf("reading changed file %s: %w", rel, err)
		}
		for _, line := range strings.Split(string(content), "\n") {
			action, ref, ok := generate.ParseUsesLine(line)
			if !ok {
				continue
			}
			refs[action] = append(refs[action], ref)
		}
	}
	return refs, nil
}

// governedActions returns the full set of actions cascade's built-in pin table
// governs (both emit:true and emit:false), the set PlanAdoptions may adopt a
// bump for.
func governedActions() (map[string]bool, error) {
	manifest, err := generate.LoadEmbeddedPinManifest()
	if err != nil {
		return nil, fmt.Errorf("loading embedded pin manifest: %w", err)
	}
	governed := make(map[string]bool, len(manifest))
	for action := range manifest {
		governed[action] = true
	}
	return governed, nil
}

// regenerate writes every file the manifest at manifestPath would produce,
// mirroring the generate command's write step. When pinOverridesPath is
// non-empty, the on-disk pin table there overlays cfg.ActionPins first (the
// own-repo mode seam, see RunOwnRepo), so the regenerate reflects a pin change
// a stale compiled-in binary would otherwise miss.
func regenerate(manifestPath, manifestKey, pinOverridesPath string) error {
	planned, err := generate.Plan(generate.PlanOptions{
		ConfigPath:       manifestPath,
		ManifestKey:      manifestKey,
		PinOverridesPath: pinOverridesPath,
	})
	if err != nil {
		return fmt.Errorf("planning regenerate: %w", err)
	}

	baseDir := generate.ResolveBaseDir(manifestPath)
	for _, p := range planned {
		path := p.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(p.Content), 0o644); err != nil { //nolint:gosec // generated workflow, not a secret.
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}
