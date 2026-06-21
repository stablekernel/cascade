// Package plan implements the read-only "cascade plan" command. It renders, as a
// per-file unified diff, what "cascade generate-workflow" would change in the
// committed workflow and action files, writing nothing. plan is the human-facing
// preview counterpart to "cascade verify": where verify is a pass/fail CI gate,
// plan shows the actual diff and is purely informational, always exiting 0 on
// success. It reuses the same side-effect-free producer, generate.Plan.
package plan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
)

// Options configures a plan run. The fields mirror the generate-workflow flags
// that determine which files the manifest emits and where they live.
type Options struct {
	ConfigPath        string
	ManifestKey       string
	ActionFolder      string
	OutputPath        string
	PromoteOutputPath string
}

// Run renders a per-file unified diff of every file the manifest would generate
// against the bytes committed on disk and writes nothing. A planned file that is
// absent on disk is rendered as a whole-file add (every line an addition); a
// planned file whose bytes differ is rendered as a unified hunk; a byte-identical
// planned file is skipped.
//
// Run always returns nil on success regardless of whether any diff exists: the
// preview is informational, not a gate. It returns a plain error only for an
// operational failure (the manifest is missing or invalid, or a planned file
// cannot be read for a reason other than not existing), which cmd/cascade/main.go
// maps to exit code 1.
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
		return fmt.Errorf("planning workflows: %w", err)
	}

	// Anchor relative planned paths to the manifest's repo root so plan reads the
	// committed files where the manifest lives, independent of the process working
	// directory. Absolute planned paths (the composite action) are read as-is. The
	// config path is resolved the same way Plan resolves it, so an auto-detected
	// manifest yields the same base directory.
	configPath := o.ConfigPath
	if configPath == "" {
		configPath = config.FindConfigFile("")
	}
	baseDir := generate.ResolveBaseDir(configPath)

	changed := 0
	for _, p := range planned {
		readPath := p.Path
		if !filepath.IsAbs(readPath) {
			readPath = filepath.Join(baseDir, readPath)
		}

		committed, rerr := os.ReadFile(readPath)
		switch {
		case rerr != nil && errors.Is(rerr, os.ErrNotExist):
			// New file: render a whole-file add with an empty committed side.
			committed = nil
		case rerr != nil:
			return fmt.Errorf("reading %s: %w", p.Path, rerr)
		case bytes.Equal(committed, []byte(p.Content)):
			// No pending change for this file.
			continue
		}

		diff := difflib.UnifiedDiff{
			A:        difflib.SplitLines(string(committed)),
			B:        difflib.SplitLines(p.Content),
			FromFile: "a/" + displayPath(p.Path),
			ToFile:   "b/" + displayPath(p.Path),
			Context:  3,
		}
		text, derr := difflib.GetUnifiedDiffString(diff)
		if derr != nil {
			return fmt.Errorf("rendering diff for %s: %w", p.Path, derr)
		}
		_, _ = io.WriteString(stdout, text)
		changed++
	}

	if changed == 0 {
		_, _ = fmt.Fprintf(stdout, "plan: %d files, no pending changes\n", len(planned))
		return nil
	}

	_, _ = fmt.Fprintf(stdout, "\n%d file(s) would change. Run `cascade generate-workflow` to apply.\n", changed)
	return nil
}

// displayPath renders an absolute planned path relative to the current working
// directory when possible, so the diff headers read as repo-relative paths. It
// falls back to the original path on any error.
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
