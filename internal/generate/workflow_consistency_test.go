package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// usesRefRe extracts the action path, pinned ref, and trailing version comment
// from a single workflow/composite "uses:" line. It accepts an optional leading
// "- " and optional surrounding quotes so it matches both step-list and
// continuation forms. The ref is everything up to whitespace or "#"; the comment
// is the first token after "# ", if present.
var usesRefRe = regexp.MustCompile(`uses:\s*["']?([^"'\s@]+)@([^"'\s#]+)["']?\s*(?:#\s*(\S+))?`)

// pinMismatch is one governed "uses:" line whose pinned ref or version comment
// disagrees with the action-pins manifest. file and line locate it; want* hold
// the manifest's canonical values; got* hold what the file actually carries.
type pinMismatch struct {
	file        string
	line        int
	action      string
	wantSHA     string
	wantVersion string
	gotRef      string
	gotComment  string
}

// String renders a mismatch as a single "file:line want ... got ..." row for the
// aggregated failure table.
func (m pinMismatch) String() string {
	return fmt.Sprintf("%s:%d %s want %s # %s got %s # %s",
		m.file, m.line, m.action, m.wantSHA, m.wantVersion, m.gotRef, m.gotComment)
}

// scanUsesForPinDrift walks every line of one file's content and returns the
// governed "uses:" lines that diverge from the manifest. A line is governed when
// its action path is a manifest key; local ("./...") and cascade self-action
// refs are never manifest keys and so are skipped implicitly. For a governed
// line the pinned ref must equal the manifest SHA and the trailing comment must
// equal the manifest version, so a silent SHA-or-comment drift is caught.
func scanUsesForPinDrift(file, content string, manifest map[string]actionPinEntry) []pinMismatch {
	var mismatches []pinMismatch
	for i, line := range strings.Split(content, "\n") {
		groups := usesRefRe.FindStringSubmatch(line)
		if groups == nil {
			continue
		}
		action, ref, comment := groups[1], groups[2], groups[3]
		entry, governed := manifest[action]
		if !governed {
			continue
		}
		if ref == entry.SHA && comment == entry.Version {
			continue
		}
		mismatches = append(mismatches, pinMismatch{
			file:        file,
			line:        i + 1,
			action:      action,
			wantSHA:     entry.SHA,
			wantVersion: entry.Version,
			gotRef:      ref,
			gotComment:  comment,
		})
	}
	return mismatches
}

// repoGitHubDir walks up from this test file's own source location to the module
// root (the directory holding go.mod) and returns that root's ".github" path.
// It anchors on runtime.Caller rather than os.Getwd so a sibling test that
// chdir's in the shared test binary cannot misdirect the scan, and it keys on
// go.mod rather than the first ".github" found because the package carries
// .github fixtures of its own that must not be mistaken for the repo root.
func repoGitHubDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller could not locate the test source file")
	dir := filepath.Dir(thisFile)
	for {
		if info, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && !info.IsDir() {
			githubDir := filepath.Join(dir, ".github")
			info, statErr := os.Stat(githubDir)
			require.NoErrorf(t, statErr, "module root %q has no .github directory", dir)
			require.Truef(t, info.IsDir(), "%q is not a directory", githubDir)
			return githubDir
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir, "walked to filesystem root without finding go.mod from %q", dir)
		dir = parent
	}
}

// governedFiles returns every hand-written and generated workflow plus every
// composite action definition under .github that the consistency lint locks to
// the manifest.
func governedFiles(t *testing.T, githubDir string) []string {
	t.Helper()
	var files []string
	for _, pattern := range []string{
		filepath.Join(githubDir, "workflows", "*.yml"),
		filepath.Join(githubDir, "workflows", "*.yaml"),
		filepath.Join(githubDir, "actions", "*", "action.yml"),
		filepath.Join(githubDir, "actions", "*", "action.yaml"),
	} {
		matches, err := filepath.Glob(pattern)
		require.NoError(t, err)
		files = append(files, matches...)
	}
	sort.Strings(files)
	require.NotEmpty(t, files, "consistency lint found no workflow or composite-action files to scan")
	return files
}

// loadActionPinsManifest parses the embedded action_pins.yaml into its full
// action set (both emit:true and emit:false) so the lint locks every governed
// action across cascade's own .github tree, not just the generator-emitted ones.
func loadActionPinsManifest(t *testing.T) map[string]actionPinEntry {
	t.Helper()
	var manifest actionPinsManifest
	require.NoError(t, yaml.Unmarshal(actionPinsYAML, &manifest))
	require.NotEmpty(t, manifest.Actions, "action_pins.yaml parsed to an empty action set")
	return manifest.Actions
}

// TestWorkflowsConsistentWithActionPins is the merge gate that keeps cascade's
// own hand-written workflows and composite actions locked to action_pins.yaml.
// dependabot edits workflow files but never the manifest, so without this lint a
// piecemeal bump could leave the manifest and the repo silently disagreeing on a
// pinned SHA. It scans every governed "uses:" line and fails once with the full
// file:line want/got table rather than on the first divergence, so a sweep fixes
// every drift in one pass.
func TestWorkflowsConsistentWithActionPins(t *testing.T) {
	manifest := loadActionPinsManifest(t)
	githubDir := repoGitHubDir(t)

	var mismatches []pinMismatch
	for _, file := range governedFiles(t, githubDir) {
		content, err := os.ReadFile(file) //nolint:gosec // path comes from a fixed glob under the repo's .github tree.
		require.NoError(t, err)
		rel, err := filepath.Rel(filepath.Dir(githubDir), file)
		require.NoError(t, err)
		mismatches = append(mismatches, scanUsesForPinDrift(rel, string(content), manifest)...)
	}

	if len(mismatches) > 0 {
		var b strings.Builder
		b.WriteString("governed uses: refs diverge from internal/generate/action_pins.yaml:\n")
		for _, m := range mismatches {
			fmt.Fprintf(&b, "  %s\n", m)
		}
		b.WriteString("update the workflow/composite file or the manifest so they agree.")
		t.Fatal(b.String())
	}
}

// TestWorkflowConsistencyLint_DetectsDivergence is the negative control that
// keeps the lint honest: it feeds scanUsesForPinDrift a fixture whose checkout
// SHA is deliberately wrong and asserts the scan reports exactly that line with
// the manifest's canonical SHA as the "want". If the detector ever silently
// stopped flagging drift, this test goes red even though the real repo is clean.
func TestWorkflowConsistencyLint_DetectsDivergence(t *testing.T) {
	manifest := loadActionPinsManifest(t)
	want, ok := manifest[actionCheckout]
	require.True(t, ok, "manifest must define %s for the negative control", actionCheckout)

	const stale = "0000000000000000000000000000000000000000"
	fixture := "jobs:\n" +
		"  build:\n" +
		"    steps:\n" +
		"      - uses: actions/checkout@" + stale + " # " + want.Version + "\n"

	mismatches := scanUsesForPinDrift("fixture.yaml", fixture, manifest)
	require.Len(t, mismatches, 1, "a flipped checkout SHA must produce exactly one mismatch")
	require.Equal(t, actionCheckout, mismatches[0].action)
	require.Equal(t, stale, mismatches[0].gotRef)
	require.Equal(t, want.SHA, mismatches[0].wantSHA, "want must carry the manifest's canonical SHA")
}
