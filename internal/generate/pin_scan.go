package generate

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// usesRefRe extracts the action path, pinned ref, and trailing version comment
// from a single workflow/composite "uses:" line. It accepts an optional leading
// "- " and optional surrounding quotes so it matches both step-list and
// continuation forms. The ref is everything up to whitespace or "#"; the comment
// is the first token after "# ", if present.
var usesRefRe = regexp.MustCompile(`uses:\s*["']?([^"'\s@]+)@([^"'\s#]+)["']?\s*(?:#\s*(\S+))?`)

// PinMismatch is one governed "uses:" line whose pinned ref or version comment
// disagrees with the action-pins manifest. File and Line locate it; Want* hold
// the manifest's canonical values; Got* hold what the file actually carries.
type PinMismatch struct {
	File        string
	Line        int
	Action      string
	WantSHA     string
	WantVersion string
	GotRef      string
	GotComment  string
}

// String renders a mismatch as a single "file:line want ... got ..." row for the
// aggregated failure table.
func (m PinMismatch) String() string {
	return fmt.Sprintf("%s:%d %s want %s # %s got %s # %s",
		m.File, m.Line, m.Action, m.WantSHA, m.WantVersion, m.GotRef, m.GotComment)
}

// ScanUsesForPinDrift walks every line of one file's content and returns the
// governed "uses:" lines that diverge from the manifest. A line is governed when
// its action path is a manifest key; local ("./...") and cascade self-action
// refs are never manifest keys and so are skipped implicitly. For a governed
// line the pinned ref must equal the manifest SHA and the trailing comment must
// equal the manifest version, so a silent SHA-or-comment drift is caught.
func ScanUsesForPinDrift(file, content string, manifest map[string]actionPinEntry) []PinMismatch {
	var mismatches []PinMismatch
	for i, line := range strings.Split(content, "\n") {
		action, ref, comment, ok := parseUsesLine(line)
		if !ok {
			continue
		}
		entry, governed := manifest[action]
		if !governed {
			continue
		}
		if ref == entry.SHA && comment == entry.Version {
			continue
		}
		mismatches = append(mismatches, PinMismatch{
			File:        file,
			Line:        i + 1,
			Action:      action,
			WantSHA:     entry.SHA,
			WantVersion: entry.Version,
			GotRef:      ref,
			GotComment:  comment,
		})
	}
	return mismatches
}

// parseUsesLine extracts the action path, the bare ref, and the trailing
// version comment (each as a separate capture) from a single "uses:" line. ok
// is false for a line that is not an action uses:.
func parseUsesLine(line string) (action, ref, comment string, ok bool) {
	g := usesRefRe.FindStringSubmatch(line)
	if g == nil {
		return "", "", "", false
	}
	return g[1], g[2], g[3], true
}

// ActionPinEntry is one action's pin record as authored in action_pins.yaml.
type ActionPinEntry = actionPinEntry

// LoadEmbeddedPinManifest returns the full action set (emit:true and false) from
// the committed action_pins.yaml, so callers lock every governed action.
func LoadEmbeddedPinManifest() (map[string]actionPinEntry, error) {
	var m actionPinsManifest
	if err := yaml.Unmarshal(actionPinsYAML, &m); err != nil {
		return nil, fmt.Errorf("parse embedded action_pins.yaml: %w", err)
	}
	return m.Actions, nil
}
