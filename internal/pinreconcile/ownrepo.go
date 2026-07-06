package pinreconcile

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
)

// pinManifestFile is the on-disk shape of action_pins.yaml: every action
// cascade pins, keyed by action path. It mirrors internal/generate's unexported
// actionPinsManifest so this package can read and rewrite the full action set
// (both emit:true and emit:false) without internal/generate exposing a
// disk-write seam of its own.
type pinManifestFile struct {
	Actions map[string]generate.ActionPinEntry `yaml:"actions"`
}

// OwnRepoOptions configures a RunOwnRepo. ManifestPath defaults to
// "<Root>/.github/manifest.yaml". ActionPinsPath names the on-disk
// action_pins.yaml cascade's own repo owns; it has no default because its
// location (internal/generate/action_pins.yaml in cascade's own tree) is not a
// convention any other repo shares. ChangedFiles lists the paths (relative to
// Root, or absolute) of the source files that triggered this reconcile: hand-
// written workflows and composite action definitions alike, since both carry
// governed pins.
type OwnRepoOptions struct {
	Root           string
	ManifestPath   string
	ManifestKey    string
	ActionPinsPath string
	ChangedFiles   []string
}

// RunOwnRepo adopts a governed pin bump observed in opts.ChangedFiles into
// cascade's own on-disk action_pins.yaml (a full re-marshal, since cascade owns
// that file outright, unlike the surgical user-manifest edit Run performs) and
// regenerates every workflow cascade's own manifest produces so it agrees
// again. It reports whether anything actually changed.
func RunOwnRepo(opts OwnRepoOptions) (bool, error) {
	manifestPath := resolveManifestPath(opts.Root, opts.ManifestPath)
	manifestKey := opts.ManifestKey
	if manifestKey == "" {
		manifestKey = config.DefaultManifestKey
	}

	sourceRefs, err := scanChangedFiles(opts.Root, opts.ChangedFiles)
	if err != nil {
		return false, err
	}

	diskManifest, err := loadFullPinManifest(opts.ActionPinsPath)
	if err != nil {
		return false, err
	}
	governed := make(map[string]bool, len(diskManifest))
	for action := range diskManifest {
		governed[action] = true
	}

	adopts, err := PlanAdoptions(Input{Governed: governed, SourceRefs: sourceRefs})
	if err != nil {
		return false, err
	}
	if !adopts.Relevant() {
		return false, nil
	}

	changed := false
	for action, ref := range adopts.Pins {
		sha, version := splitRef(ref)
		entry := diskManifest[action]
		if entry.SHA != sha || (version != "" && entry.Version != version) {
			changed = true
			break
		}
	}
	if !changed {
		return false, nil
	}

	for action, ref := range adopts.Pins {
		sha, version := splitRef(ref)
		entry := diskManifest[action]
		entry.SHA = sha
		if version != "" {
			entry.Version = version
		}
		diskManifest[action] = entry
	}

	if err := writeFullPinManifest(opts.ActionPinsPath, diskManifest); err != nil {
		return false, err
	}

	if err := regenerate(manifestPath, manifestKey, opts.ActionPinsPath); err != nil {
		return false, err
	}

	return true, nil
}

// splitRef separates a verbatim adopted ref into its sha (or tag) and its
// trailing "# <version>" comment, if present. A ref with no comment returns an
// empty version so the caller can leave the manifest's existing version field
// untouched rather than blanking it.
func splitRef(ref string) (value, version string) {
	if idx := strings.Index(ref, " # "); idx >= 0 {
		return ref[:idx], ref[idx+len(" # "):]
	}
	return ref, ""
}

// loadFullPinManifest parses an action_pins.yaml from disk into its full
// action set (both emit:true and emit:false), the shape RunOwnRepo needs to
// adopt a bump for any governed action, not just the emit:true subset the
// generator renders into user workflows.
func loadFullPinManifest(path string) (map[string]generate.ActionPinEntry, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller supplies cascade's own repo-relative path.
	if err != nil {
		return nil, fmt.Errorf("reading action pins %s: %w", path, err)
	}
	var file pinManifestFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parsing action pins %s: %w", path, err)
	}
	return file.Actions, nil
}

// pinManifestHeaderEnd returns the byte offset just past the leading comment
// block of an action_pins.yaml (everything before the top-level "actions:"
// key). A full re-marshal preserves that header verbatim by re-attaching it
// rather than round-tripping it through the YAML encoder, which has no
// concept of a file's leading comment block.
func pinManifestHeaderEnd(data []byte) int {
	idx := bytes.Index(data, []byte("\nactions:"))
	if idx < 0 {
		return 0
	}
	return idx + 1
}

// writeFullPinManifest re-marshals the full action set to path, re-attaching
// the file's original header comment block so it survives the rewrite.
func writeFullPinManifest(path string, actions map[string]generate.ActionPinEntry) error {
	original, err := os.ReadFile(path) //nolint:gosec // caller supplies cascade's own repo-relative path.
	if err != nil {
		return fmt.Errorf("reading action pins %s: %w", path, err)
	}
	header := original[:pinManifestHeaderEnd(original)]

	body, err := yaml.Marshal(pinManifestFile{Actions: actions})
	if err != nil {
		return fmt.Errorf("marshaling action pins: %w", err)
	}

	var out bytes.Buffer
	out.Write(header)
	out.Write(body)

	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil { //nolint:gosec // manifest is not a secret.
		return fmt.Errorf("writing action pins %s: %w", path, err)
	}
	return nil
}
