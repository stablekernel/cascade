package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docsVersionPinPattern matches the concrete version pins that docs and the
// README present as current: `cli_version:` manifest examples, `go install
// .../cmd/cascade@vX.Y.Z`, tag-pinned `setup-cli@vX.Y.Z` uses lines, and the
// sample `cascade vX.Y.Z` version output. Deliberately outside the guard:
// sha-pinned `setup-cli@<sha> # vX.Y.Z` examples (the sha/tag pairing is
// internally consistent whatever release it names) and generic prose such as
// "for example `vX.Y.Z`".
var docsVersionPinPattern = regexp.MustCompile(
	`(?:cli_version:\s*|cascade/cmd/cascade@|setup-cli@|cascade )(v\d+\.\d+\.\d+)`)

// TestDefaultCLIVersion_MatchesDocsExamples keeps the README and docs-site
// examples in lockstep with DefaultCLIVersion, so a release bump cannot leave
// the quickstart installing or pinning the previous CLI. The examples went
// stale on the v0.16.0 -> v0.16.1 bump; this turns that staleness into a
// failing gate instead of a per-release manual refresh.
func TestDefaultCLIVersion_MatchesDocsExamples(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))

	files := []string{filepath.Join(root, "README.md")}
	docsRoot := filepath.Join(root, "docs", "src", "content", "docs")
	err = filepath.WalkDir(docsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext == ".md" || ext == ".mdx" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs content: %v", err)
	}

	pins := 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, m := range docsVersionPinPattern.FindAllStringSubmatch(line, -1) {
				pins++
				if m[1] != DefaultCLIVersion {
					t.Errorf("%s:%d pins %q but DefaultCLIVersion is %q; refresh the example (line: %s)",
						rel, i+1, m[1], DefaultCLIVersion, strings.TrimSpace(line))
				}
			}
		}
	}

	// The guard only works while the docs actually contain pinned examples.
	// If a docs restructure drops below this floor, re-point the pattern at
	// the new example surface rather than deleting the check.
	if pins < 5 {
		t.Fatalf("found only %d version-pinned examples across README.md and docs/; expected at least 5, so the pattern no longer matches the docs surface", pins)
	}
}
