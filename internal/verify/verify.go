// Package verify implements the read-only "cascade verify" command. It compares
// the workflow and action files committed to a repository against what the
// manifest would currently generate, reporting any drift. verify never writes to
// the filesystem, invokes git, or creates scratch state: the comparison is done
// entirely in memory.
package verify

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
)

// Exit codes returned to the process by the verify command. The contract is
// frozen: 0 means no drift, 1 means drift, 2 means the check could not run. It
// mirrors diff(1) so verify drops into a CI drift gate without surprise.
const (
	exitDrift       = 1
	exitOperational = 2
)

// exitError pairs an error with the process exit code the verify command should
// return for it. cmd/cascade/main.go reads the code through the unexported
// ExitCode() int interface, so verify owns its exit contract without main.go
// importing this package or special-casing the command.
type exitError struct {
	code int
	err  error
}

// Error implements error.
func (e *exitError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped error so errors.Is and errors.As traverse into it.
func (e *exitError) Unwrap() error { return e.err }

// ExitCode reports the process exit code for this error.
func (e *exitError) ExitCode() int { return e.code }

// ErrDrift is returned (wrapped) by Run when the committed files diverge from
// what the manifest would generate (a file is missing or its bytes differ).
// Callers distinguish drift from operational failures (a missing or invalid
// manifest) with errors.Is(err, ErrDrift). It carries exit code 1.
var ErrDrift = &exitError{code: exitDrift, err: errors.New("workflow drift detected")}

// operational wraps err as a code-2 operational failure: the verify check could
// not run (the manifest is missing or invalid, or a planned file could not be
// read). The wrapped chain never contains ErrDrift, so errors.Is(err, ErrDrift)
// stays false for operational failures.
func operational(err error) error {
	return &exitError{code: exitOperational, err: err}
}

// Options configures a verify run. The fields mirror the generate-workflow flags
// that determine which files the manifest emits and where they live.
type Options struct {
	ConfigPath        string
	ManifestKey       string
	ActionFolder      string
	OutputPath        string
	PromoteOutputPath string
	Quiet             bool
	// AllowOrphans disables orphan detection. When false (the default), verify
	// reports cascade-owned workflow files that the manifest no longer plans as
	// drift; when true, those files are ignored and never affect the exit code.
	AllowOrphans bool
	// OwnRepo plans cascade's own-repo release-plumbing variant of the
	// orchestrate workflow and the manage-release composite action, matching
	// generate-workflow --own-repo. Cascade's own manifest is generated in that
	// mode, so its committed orchestrate.yaml and action.yaml are drift-locked to
	// the own-repo output; without this, verify would compute the plain variant
	// and report the deliberate own-repo differences as spurious drift.
	OwnRepo bool
}

// Run compares every file the manifest would generate against the bytes
// committed on disk. It returns:
//
//   - nil when every planned file is present and byte-identical, mapping to
//     exit code 0 (no drift),
//   - ErrDrift when any planned file is missing or differs, mapping to exit
//     code 1 (drift detected), or
//   - an operational error when the manifest cannot be read or validated,
//     mapping to exit code 2 (the check could not run).
//
// The returned error carries its exit code through an ExitCode() int method
// that cmd/cascade/main.go reads to set the process status.
//
// Run is read-only: it reads the manifest, the reusable-workflow stubs the
// generators inspect, and the committed files, and writes nothing.
func Run(o Options, stdout, stderr io.Writer) error {
	planned, err := generate.Plan(generate.PlanOptions{
		ConfigPath:        o.ConfigPath,
		ManifestKey:       o.ManifestKey,
		ActionFolder:      o.ActionFolder,
		OutputPath:        o.OutputPath,
		PromoteOutputPath: o.PromoteOutputPath,
		OwnRepo:           o.OwnRepo,
	})
	if err != nil {
		return operational(fmt.Errorf("planning workflows: %w", err))
	}

	// Anchor relative planned paths to the manifest's repo root so verify reads
	// the committed files where the manifest lives, independent of the process
	// working directory. Absolute planned paths (the composite action) are read
	// as-is. The config path is resolved the same way Plan resolves it, so an
	// auto-detected manifest yields the same base directory.
	configPath := o.ConfigPath
	if configPath == "" {
		configPath = config.FindConfigFile("")
	}
	baseDir := generate.ResolveBaseDir(configPath)

	type drift struct {
		path    string
		missing bool
	}
	var drifts []drift

	// planned holds the absolute on-disk read path of every planned file, used
	// both for the missing/changed comparison and to exclude planned files from
	// orphan detection below.
	plannedPaths := make(map[string]struct{}, len(planned))

	for _, p := range planned {
		readPath := p.Path
		if !filepath.IsAbs(readPath) {
			readPath = filepath.Join(baseDir, readPath)
		}
		plannedPaths[filepath.Clean(readPath)] = struct{}{}
		committed, rerr := os.ReadFile(readPath)
		if rerr != nil {
			if errors.Is(rerr, os.ErrNotExist) {
				drifts = append(drifts, drift{path: p.Path, missing: true})
				continue
			}
			return operational(fmt.Errorf("reading %s: %w", p.Path, rerr))
		}
		if !bytes.Equal(committed, []byte(p.Content)) {
			drifts = append(drifts, drift{path: p.Path})
		}
	}

	var orphans []string
	if !o.AllowOrphans {
		orphans, err = findOrphans(planned, plannedPaths, baseDir)
		if err != nil {
			return operational(err)
		}
	}

	if len(drifts) == 0 && len(orphans) == 0 {
		if !o.Quiet {
			_, _ = fmt.Fprintf(stdout, "verify: %d files, no drift\n", len(planned))
		}
		return nil
	}

	if !o.Quiet {
		for _, d := range drifts {
			if d.missing {
				_, _ = fmt.Fprintf(stderr, "! %s (missing)\n", displayPath(d.path))
			} else {
				_, _ = fmt.Fprintf(stderr, "~ %s\n", displayPath(d.path))
			}
		}
		for _, o := range orphans {
			_, _ = fmt.Fprintf(stderr, "? %s (orphaned)\n", displayPath(o))
		}
		_, _ = fmt.Fprintf(stderr, "\n%d file(s) drifted. Run `cascade generate-workflow` and commit the result.\n", len(drifts)+len(orphans))
	}

	return ErrDrift
}

// findOrphans scans the directories that hold the planned workflow files for
// cascade-owned files the manifest no longer plans. A file is an orphan only
// when it carries generate.GeneratedFileMarker (so hand-written workflows are
// never flagged) and is not itself a planned file. A file carrying
// generate.OwnRepoGeneratedFileMarker is cascade's own self-heal companion,
// generated and drift-locked but intentionally outside the manifest plan, so it
// is skipped rather than flagged. The scan is confined to the
// distinct directories of the planned workflow files (normally
// .github/workflows); the composite action directory and the wider repo are
// never scanned. It returns the orphans sorted by absolute path for stable
// output. A missing scan directory yields no orphans, not an error.
func findOrphans(planned []generate.PlannedFile, plannedPaths map[string]struct{}, baseDir string) ([]string, error) {
	scanDirs := workflowScanDirs(planned, baseDir)

	var orphans []string
	for dir := range scanDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("scanning %s for orphans: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			full := filepath.Clean(filepath.Join(dir, entry.Name()))
			if _, ok := plannedPaths[full]; ok {
				continue
			}
			body, rerr := os.ReadFile(full)
			if rerr != nil {
				if errors.Is(rerr, os.ErrNotExist) {
					continue
				}
				return nil, fmt.Errorf("reading %s for orphan check: %w", full, rerr)
			}
			// The own-repo self-heal companion is cascade-generated and
			// drift-locked, but it lives outside the manifest workflow plan on
			// purpose, so it is never a manifest orphan. Skip it before the plain
			// marker check so real orphans still surface.
			if bytes.Contains(body, []byte(generate.OwnRepoGeneratedFileMarker)) {
				continue
			}
			if bytes.Contains(body, []byte(generate.GeneratedFileMarker)) {
				orphans = append(orphans, full)
			}
		}
	}
	sort.Strings(orphans)
	return orphans, nil
}

// workflowScanDirs returns the distinct directories that hold planned workflow
// files. A planned file counts as a workflow when its (anchored) path lives
// under a ".github/workflows" path segment; the composite action's path does
// not, so its directory is excluded from the orphan scan.
func workflowScanDirs(planned []generate.PlannedFile, baseDir string) map[string]struct{} {
	dirs := make(map[string]struct{})
	for _, p := range planned {
		readPath := p.Path
		if !filepath.IsAbs(readPath) {
			readPath = filepath.Join(baseDir, readPath)
		}
		readPath = filepath.Clean(readPath)
		dir := filepath.Dir(readPath)
		if filepath.Base(dir) != "workflows" || filepath.Base(filepath.Dir(dir)) != ".github" {
			continue
		}
		dirs[dir] = struct{}{}
	}
	return dirs
}

// displayPath renders an absolute planned path relative to the current working
// directory when possible, so reports read as repo-relative paths. It falls back
// to the original path on any error.
func displayPath(path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		return path
	}
	return rel
}
