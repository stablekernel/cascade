package config

import "testing"

// TestMatchTrigger locks in the order-dependent semantics of the GitHub
// Actions paths filter that cascade emits. Per the workflow-syntax reference,
// the order patterns are defined in matters: a matching negative pattern
// (prefixed with "!") after a positive match excludes the path, and a matching
// positive pattern after a negative match includes the path again. Evaluation
// is last-match-wins with the path starting excluded.
func TestMatchTrigger(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		file     string
		want     bool
	}{
		// Positive-only patterns are order-insensitive by construction.
		{"positive match", []string{"src/**"}, "src/main.go", true},
		{"positive no match", []string{"src/**"}, "other/main.go", false},
		{"multiple positives one matches", []string{"src/**", "Dockerfile"}, "Dockerfile", true},

		// Exclusion after inclusion: the trailing negation wins.
		{"negation excludes md under wildcard", []string{"**", "!**/*.md"}, "docs/README.md", false},
		{"negation excludes docs dir", []string{"**", "!docs/**"}, "docs/guide.txt", false},
		{"positive survives unrelated negation", []string{"**", "!**/*.md"}, "src/main.go", true},

		// Order matters: a matching positive pattern AFTER a negative match
		// includes the path again, exactly as the emitted paths filter does.
		// The old order-independent evaluator returned false for these.
		{"positive after negation re-includes", []string{"!**/*.md", "**"}, "docs/README.md", true},
		{"positive after scoped negation re-includes", []string{"!src/vendor/**", "src/**"}, "src/vendor/lib.go", true},
		{"negation first allows non-excluded", []string{"!**/*.md", "**"}, "src/main.go", true},

		// Exclusion then re-inclusion of a subtree.
		{"re-included subtree matches", []string{"src/**", "!src/vendor/**", "src/vendor/keep/**"}, "src/vendor/keep/patch.go", true},
		{"excluded subtree outside re-include stays excluded", []string{"src/**", "!src/vendor/**", "src/vendor/keep/**"}, "src/vendor/other/lib.go", false},
		{"unexcluded file still matches with re-include present", []string{"src/**", "!src/vendor/**", "src/vendor/keep/**"}, "src/app.go", true},

		// Alternating chain: the LAST matching pattern decides.
		{"alternating chain ends excluded", []string{"**", "!docs/**", "docs/api/**", "!docs/api/internal/**"}, "docs/api/internal/secret.md", false},
		{"alternating chain ends included", []string{"**", "!docs/**", "docs/api/**", "!docs/api/internal/**"}, "docs/api/spec.md", true},
		{"alternating chain middle exclusion holds", []string{"**", "!docs/**", "docs/api/**", "!docs/api/internal/**"}, "docs/guide.md", false},

		// Scoped negation only excludes within the negated subtree.
		{"scoped negation excludes md in src", []string{"src/**", "!src/**/*.md"}, "src/docs.md", false},
		{"scoped negation keeps go in src", []string{"src/**", "!src/**/*.md"}, "src/main.go", true},

		// Negation-only list: GitHub disallows a paths filter with no positive
		// entry, so cascade emits these as paths-ignore. The evaluator mirrors
		// that translation: any file not excluded is a match.
		{"negation-only excludes match", []string{"!**/*.md"}, "README.md", false},
		{"negation-only allows non-match", []string{"!**/*.md"}, "src/main.go", true},
		{"negation-only multiple patterns", []string{"!docs/**", "!**/*.md"}, "src/main.go", true},
		{"negation-only multiple patterns excludes", []string{"!docs/**", "!**/*.md"}, "notes.md", false},
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
		{
			name:         "re-included subtree triggers a changeset",
			patterns:     []string{"src/**", "!src/vendor/**", "src/vendor/keep/**"},
			changedFiles: []string{"src/vendor/keep/patch.go"},
			want:         true,
		},
		{
			name:         "excluded-only changeset does not trigger",
			patterns:     []string{"src/**", "!src/vendor/**", "src/vendor/keep/**"},
			changedFiles: []string{"src/vendor/other/lib.go"},
			want:         false,
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

func TestMatchAnyTriggerSet(t *testing.T) {
	tests := []struct {
		name         string
		sets         [][]string
		changedFiles []string
		want         bool
	}{
		{"no sets always trigger", nil, []string{"anything.go"}, true},
		{"empty set inside always triggers", [][]string{{"src/**"}, {}}, []string{"README.md"}, true},
		{"one set matches", [][]string{{"api/**"}, {"web/**"}}, []string{"web/app.js"}, true},
		{"no set matches", [][]string{{"api/**"}, {"web/**"}}, []string{"docs/guide.md"}, false},

		// Sets are OR-ed independently, never concatenated: MatchTrigger is
		// order-dependent (last match wins), so a trailing "!" from one set
		// would veto a sibling set's positive match if the lists were joined.
		{
			name:         "sibling exclusion cannot veto a match",
			sets:         [][]string{{"**/*.md"}, {"docs/**", "!docs/api/**"}},
			changedFiles: []string{"docs/api/readme.md"},
			want:         true,
		},
		{
			name:         "exclusion still applies within its own set",
			sets:         [][]string{{"src/**"}, {"docs/**", "!docs/api/**"}},
			changedFiles: []string{"docs/api/readme.md"},
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchAnyTriggerSet(tt.sets, tt.changedFiles); got != tt.want {
				t.Errorf("MatchAnyTriggerSet(%v, %v) = %v, want %v", tt.sets, tt.changedFiles, got, tt.want)
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

// TestMatchGlobPattern_GHAGrammar exercises the glob grammar of the GitHub
// Actions filter-pattern cheat sheet, which the emitted paths filter uses:
//
//   - "*"  matches zero or more characters, but never "/".
//   - "**" matches zero or more of any character, including "/". A "**" that
//     forms a whole path segment followed by "/" also collapses to nothing, so
//     "**/README.md" matches a root-level README.md.
//   - "?"  matches zero or one of the preceding character.
//   - "+"  matches one or more of the preceding character.
//   - "[]" matches one character listed in the brackets or included in ranges.
//
// Every case marked "cheat sheet" is a documented example from the reference.
func TestMatchGlobPattern_GHAGrammar(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		file    string
		want    bool
	}{
		// cheat sheet: '*'
		{"star matches root file", "*", "README.md", true},
		{"star does not cross slash", "*", "docs/README.md", false},

		// cheat sheet: '*.jsx?'
		{"question mark drops preceding char", "*.jsx?", "page.js", true},
		{"question mark keeps preceding char", "*.jsx?", "page.jsx", true},
		{"question mark is not a wildcard", "*.jsx?", "page.jsxx", false},

		// cheat sheet: '**'
		{"double star matches everything", "**", "all/the/files.md", true},

		// cheat sheet: '*.js'
		{"star suffix matches root file", "*.js", "app.js", true},
		{"star suffix does not descend", "*.js", "src/app.js", false},

		// cheat sheet: '**.js' (inline ** crosses slashes)
		{"inline double star matches root", "**.js", "index.js", true},
		{"inline double star matches nested", "**.js", "js/index.js", true},
		{"inline double star matches deeply nested", "**.js", "src/js/app.js", true},
		{"inline double star respects suffix", "**.js", "src/js/app.ts", false},

		// cheat sheet: 'docs/*'
		{"dir star matches direct child", "docs/*", "docs/README.md", true},
		{"dir star does not descend", "docs/*", "docs/mona/octocat.txt", false},

		// cheat sheet: 'docs/**'
		{"dir double star matches child", "docs/**", "docs/README.md", true},
		{"dir double star matches nested", "docs/**", "docs/mona/octocat.txt", true},
		{"dir double star needs the directory", "docs/**", "docs2/file.txt", false},

		// cheat sheet: 'docs/**/*.md'
		{"mid double star collapses", "docs/**/*.md", "docs/README.md", true},
		{"mid double star matches one level", "docs/**/*.md", "docs/mona/hello-world.md", true},
		{"mid double star matches many levels", "docs/**/*.md", "docs/a/markdown/file.md", true},
		{"mid double star respects suffix", "docs/**/*.md", "docs/a/file.txt", false},

		// cheat sheet: '**/docs/**'
		{"anywhere dir matches at root", "**/docs/**", "docs/hello.md", true},
		{"anywhere dir matches nested", "**/docs/**", "dir/docs/my-file.txt", true},

		// cheat sheet: '**/README.md'
		{"leading double star collapses at root", "**/README.md", "README.md", true},
		{"leading double star matches nested", "**/README.md", "js/README.md", true},
		{"leading double star anchors the name", "**/README.md", "js/READMES.md", false},

		// cheat sheet: '**/*src/**'
		{"suffix segment anywhere", "**/*src/**", "a/src/app.js", true},
		{"suffix segment at root", "**/*src/**", "my-src/code/js/app.js", true},

		// cheat sheet: '**/*-post.md'
		{"suffix file at root", "**/*-post.md", "my-post.md", true},
		{"suffix file nested", "**/*-post.md", "path/their-post.md", true},

		// cheat sheet: '**/migrate-*.sql'
		{"prefix file at root", "**/migrate-*.sql", "migrate-10909.sql", true},
		{"prefix file nested", "**/migrate-*.sql", "db/migrate-v1.0.sql", true},
		{"prefix file deeply nested", "**/migrate-*.sql", "db/sept/migrate-v1.sql", true},

		// cheat sheet: '[CB]at' and '[1-2]00'
		{"bracket set matches listed char", "[CB]at", "Cat", true},
		{"bracket set matches other listed char", "[CB]at", "Bat", true},
		{"bracket set rejects unlisted char", "[CB]at", "mat", false},
		{"bracket range matches", "[1-2]00", "100", true},
		{"bracket range matches upper bound", "[1-2]00", "200", true},
		{"bracket range rejects out of range", "[1-2]00", "300", false},

		// '+' matches one or more of the preceding character.
		{"plus needs at least one", "[0-9]+.md", "1.md", true},
		{"plus matches repeats", "[0-9]+.md", "12.md", true},
		{"plus rejects zero occurrences", "a+.txt", ".txt", false},
		{"plus matches run of preceding char", "a+.txt", "aaa.txt", true},

		// Literal characters that are regex metacharacters must stay literal.
		{"dot is literal", "*.go", "maingo", false},
		{"exact file match", "Dockerfile", "Dockerfile", true},
		{"exact file mismatch", "Dockerfile", "Dockerfile.dev", false},

		// A leading '?' or '+' has no preceding character; GitHub does not
		// define the behaviour, so cascade treats it as a literal character.
		{"leading question mark is literal", "?.go", "a.go", false},
		{"leading question mark matches itself", "?.go", "?.go", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchGlobPattern(tt.pattern, tt.file); got != tt.want {
				t.Errorf("MatchGlobPattern(%q, %q) = %v, want %v", tt.pattern, tt.file, got, tt.want)
			}
		})
	}
}

// TestMatchGlobPattern_SlashBoundary locks in the slash-native matching
// semantics of the GitHub Actions grammar: a single "*" matches within one
// path segment and never crosses a "/". These are guard/documentation tests
// that would catch a regression to a matcher whose separator is
// platform-dependent.
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
