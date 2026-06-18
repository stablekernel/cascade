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

	for _, p := range planned {
		readPath := p.Path
		if !filepath.IsAbs(readPath) {
			readPath = filepath.Join(baseDir, readPath)
		}
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

	if len(drifts) == 0 {
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
		_, _ = fmt.Fprintf(stderr, "\n%d file(s) drifted. Run `cascade generate-workflow` and commit the result.\n", len(drifts))
	}

	return ErrDrift
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
