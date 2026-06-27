package promote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMatchGlob covers the single-segment and doublestar dispatch of matchGlob.
func TestMatchGlob(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"exact match", "go.mod", "go.mod", true},
		{"exact non-match", "go.mod", "go.sum", false},
		{"single star matches within a segment", "src/*.go", "src/main.go", true},
		{"single star does not span directories", "src/*.go", "src/sub/main.go", false},
		{"single star same dir", "*.go", "main.go", true},
		{"single star does not cross slash", "*.go", "src/main.go", false},
		{"doublestar prefix matches nested", "src/**", "src/a/b/c.go", true},
		{"doublestar prefix matches direct child", "src/**", "src/main.go", true},
		{"doublestar non-match wrong root", "src/**", "lib/main.go", false},
		{"doublestar middle segment", "a/**/d.go", "a/b/c/d.go", true},
		{"doublestar middle non-match", "a/**/d.go", "a/b/c/e.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, matchGlob(tc.pattern, tc.path))
		})
	}
}

// TestMatchDoublestar exercises the ** handling directly, including the
// trailing-** "matches everything" branch and exhausted-pattern checks.
func TestMatchDoublestar(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"trailing doublestar matches deep path", "infra/**", "infra/modules/vpc/main.tf", true},
		{"trailing doublestar matches single", "infra/**", "infra/main.tf", true},
		{"leading doublestar matches suffix", "**/main.go", "a/b/main.go", true},
		{"leading doublestar matches root file", "**/main.go", "main.go", true},
		{"leading doublestar suffix mismatch", "**/main.go", "a/b/other.go", false},
		{"doublestar with wildcard segment", "src/**/*.go", "src/a/b.go", true},
		{"prefix mismatch before doublestar", "x/**", "y/z", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, matchDoublestar(tc.pattern, tc.path))
		})
	}
}

// TestMatchParts covers the recursive segment matcher edge cases that the
// higher-level helpers route into: exhausted pattern, exhausted path, and the
// zero-segment ** match.
func TestMatchParts(t *testing.T) {
	cases := []struct {
		name    string
		pattern []string
		path    []string
		want    bool
	}{
		{"equal literal segments", []string{"a", "b"}, []string{"a", "b"}, true},
		{"pattern longer than path", []string{"a", "b", "c"}, []string{"a", "b"}, false},
		{"path longer than pattern without doublestar", []string{"a"}, []string{"a", "b"}, false},
		{"doublestar absorbs zero segments", []string{"a", "**"}, []string{"a"}, true},
		{"doublestar absorbs many segments", []string{"a", "**"}, []string{"a", "b", "c"}, true},
		{"trailing doublestar after full match", []string{"a", "b", "**"}, []string{"a", "b"}, true},
		{"both empty", []string{}, []string{}, true},
		{"empty pattern non-empty path", []string{}, []string{"a"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, matchParts(tc.pattern, tc.path))
		})
	}
}
