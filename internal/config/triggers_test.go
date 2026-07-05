package config

import "testing"

func TestMatchTrigger(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		file     string
		want     bool
	}{
		// Positive-only patterns behave exactly as before negation support.
		{"positive match", []string{"src/**"}, "src/main.go", true},
		{"positive no match", []string{"src/**"}, "other/main.go", false},
		{"multiple positives one matches", []string{"src/**", "Dockerfile"}, "Dockerfile", true},

		// Negation excludes a file that also matches a positive pattern.
		{"negation excludes md under wildcard", []string{"**", "!**/*.md"}, "docs/README.md", false},
		{"negation excludes docs dir", []string{"**", "!docs/**"}, "docs/guide.txt", false},
		{"positive survives unrelated negation", []string{"**", "!**/*.md"}, "src/main.go", true},

		// Ordering is irrelevant: a path triggers iff it matches a positive
		// and is not excluded by any negation, regardless of pattern order.
		{"negation listed first still excludes", []string{"!**/*.md", "**"}, "docs/README.md", false},
		{"negation listed first allows non-excluded", []string{"!**/*.md", "**"}, "src/main.go", true},

		// Negation-only list behaves like paths-ignore: any non-excluded file
		// is a match.
		{"negation-only excludes match", []string{"!**/*.md"}, "README.md", false},
		{"negation-only allows non-match", []string{"!**/*.md"}, "src/main.go", true},

		// Scoped negation: only excludes within the negated subtree.
		{"scoped negation excludes md in src", []string{"src/**", "!src/**/*.md"}, "src/docs.md", false},
		{"scoped negation keeps go in src", []string{"src/**", "!src/**/*.md"}, "src/main.go", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchTrigger(tt.patterns, tt.file); got != tt.want {
				t.Errorf("MatchTrigger(%v, %q) = %v, want %v", tt.patterns, tt.file, got, tt.want)
			}
		})
	}
}

func TestMatchAnyTrigger(t *testing.T) {
	tests := []struct {
		name         string
		patterns     []string
		changedFiles []string
		want         bool
	}{
		{"empty patterns always trigger", nil, []string{"anything.go"}, true},
		{"positive matches one file", []string{"src/**"}, []string{"README.md", "src/main.go"}, true},
		{"positive matches no file", []string{"src/**"}, []string{"README.md"}, false},

		// The canonical docs-only-excluded scenario from the issue.
		{
			name:         "docs-only change does not trigger",
			patterns:     []string{"**", "!**/*.md", "!docs/**"},
			changedFiles: []string{"docs/README.md"},
			want:         false,
		},
		{
			name:         "source change triggers",
			patterns:     []string{"**", "!**/*.md", "!docs/**"},
			changedFiles: []string{"src/main.go"},
			want:         true,
		},
		{
			name:         "mixed change triggers via non-excluded file",
			patterns:     []string{"**", "!**/*.md", "!docs/**"},
			changedFiles: []string{"docs/README.md", "src/main.go"},
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchAnyTrigger(tt.patterns, tt.changedFiles); got != tt.want {
				t.Errorf("MatchAnyTrigger(%v, %v) = %v, want %v", tt.patterns, tt.changedFiles, got, tt.want)
			}
		})
	}
}

func TestIsNegationPattern(t *testing.T) {
	if !IsNegationPattern("!**/*.md") {
		t.Error("IsNegationPattern(!**/*.md) = false, want true")
	}
	if IsNegationPattern("src/**") {
		t.Error("IsNegationPattern(src/**) = true, want false")
	}
}

// TestMatchGlobPattern_SlashBoundary locks in the slash-native matching
// semantics that path.Match provides: a single "*" matches within one path
// segment and never crosses a "/", matching the GitHub Actions paths filter
// grammar this evaluator is meant to mirror. These cases pass on Linux/macOS
// under either path.Match or filepath.Match (the separator is "/" on both),
// so they are guard/documentation tests that would catch a future regression
// to a matcher whose separator is platform-dependent; the Windows behavioral
// delta (filepath.Match treating "/" as an ordinary character) cannot be
// exercised on Linux CI.
func TestMatchGlobPattern_SlashBoundary(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		file    string
		want    bool
	}{
		{"single star does not cross slash", "a/*/b", "a/x/y/b", false},
		{"single star matches one segment", "a/*/b", "a/x/b", true},
		{"dir star does not descend", "dir/*", "dir/sub/file", false},
		{"dir star matches direct child", "dir/*", "dir/file", true},
		{"doublestar still matches nested files", "**/*.go", "a/b/c.go", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchGlobPattern(tt.pattern, tt.file); got != tt.want {
				t.Errorf("MatchGlobPattern(%q, %q) = %v, want %v", tt.pattern, tt.file, got, tt.want)
			}
		})
	}
}

func TestMatchGlobPattern_StripsNegation(t *testing.T) {
	// The bare-glob helper matches whether the glob (negation stripped) matches.
	if !MatchGlobPattern("!**/*.md", "docs/README.md") {
		t.Error("MatchGlobPattern(!**/*.md, docs/README.md) = false, want true")
	}
	if MatchGlobPattern("!**/*.md", "src/main.go") {
		t.Error("MatchGlobPattern(!**/*.md, src/main.go) = true, want false")
	}
}
