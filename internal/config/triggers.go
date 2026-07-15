package config

import (
	"regexp"
	"strings"
	"sync"
)

// IsNegationPattern reports whether a trigger pattern is an exclusion pattern
// (a leading "!"). Exclusion patterns carry the "!" semantics of the GitHub
// Actions paths filter: they subtract matching files from whatever the
// preceding patterns included.
func IsNegationPattern(pattern string) bool {
	return strings.HasPrefix(pattern, "!")
}

// stripNegation removes a leading "!" from a trigger pattern, returning the bare
// glob to match against.
func stripNegation(pattern string) string {
	return strings.TrimPrefix(pattern, "!")
}

// MatchTrigger reports whether a single changed file path matches the supplied
// trigger pattern list, honouring negation ("!"-prefixed) patterns.
//
// Semantics (frozen v1 contract, the GitHub Actions on.push.paths behaviour
// cascade emits): evaluation is ORDER-DEPENDENT, last match wins, and the file
// starts excluded. A matching negative pattern after a positive match excludes
// the file; a matching positive pattern after a negative match includes it
// again. This mirrors the workflow-syntax reference exactly, so CLI-side
// change detection predicts what the emitted filter does.
//
// A list with no positive pattern cannot be expressed as a paths filter
// (GitHub requires at least one non-"!" entry), so cascade emits it as
// paths-ignore. The evaluator mirrors that translation: any file not matching
// an exclusion is a match.
func MatchTrigger(patterns []string, file string) bool {
	hasPositive := false
	included := false
	excluded := false

	for _, pattern := range patterns {
		if IsNegationPattern(pattern) {
			if matchGlobPattern(stripNegation(pattern), file) {
				included = false
				excluded = true
			}
			continue
		}

		hasPositive = true
		if matchGlobPattern(pattern, file) {
			included = true
		}
	}

	if !hasPositive {
		// Negation-only list, emitted as paths-ignore: any file not excluded
		// is a match.
		return !excluded
	}

	return included
}

// MatchAnyTrigger reports whether any of the changed files is a trigger under
// the supplied pattern list (negation-aware via MatchTrigger). An empty pattern
// list means "always triggered". This is the shared evaluator that the CLI-side
// change detection uses so it agrees with the GitHub Actions paths filter the
// generator emits from the same pattern list.
func MatchAnyTrigger(patterns []string, changedFiles []string) bool {
	if len(patterns) == 0 {
		return true
	}

	for _, file := range changedFiles {
		if MatchTrigger(patterns, file) {
			return true
		}
	}

	return false
}

// MatchAnyTriggerSet reports whether any of the trigger pattern sets matches
// the changed files. Each set is evaluated independently with MatchAnyTrigger
// and the results are OR-ed, so a "!" exclusion is scoped to its own set and
// cannot veto a sibling set's positive match. An empty sets list means
// "always triggered", as does any empty set within it (MatchAnyTrigger's
// contract for an unconstrained source).
func MatchAnyTriggerSet(sets [][]string, changedFiles []string) bool {
	if len(sets) == 0 {
		return true
	}

	for _, set := range sets {
		if MatchAnyTrigger(set, changedFiles) {
			return true
		}
	}

	return false
}

// MatchGlobPattern reports whether a single glob pattern matches a path. A
// leading "!" is stripped before matching, so the helper reports whether the
// bare glob matches; negation as an exclusion is applied at the pattern-list
// level by MatchTrigger. The glob grammar is the GitHub Actions filter-pattern
// grammar (see matchGlobPattern).
func MatchGlobPattern(pattern, filePath string) bool {
	return matchGlobPattern(stripNegation(pattern), filePath)
}

// patternCache memoizes compiled patterns; trigger lists are matched against
// every changed file, so each pattern compiles once per process.
var patternCache sync.Map // pattern string -> *regexp.Regexp

// matchGlobPattern matches a file path against a single (already
// negation-stripped) glob pattern using the GitHub Actions filter-pattern
// grammar, the grammar of the emitted paths filter:
//
//   - "*"  matches zero or more characters, but never "/".
//   - "**" matches zero or more of any character, including "/". When it forms
//     a whole path segment followed by "/" (leading "**/" or a "/**/" run) the
//     segment collapses entirely, so "**/README.md" matches a root README.md
//     and "docs/**/*.md" matches "docs/README.md".
//   - "?"  matches zero or one of the preceding character.
//   - "+"  matches one or more of the preceding character.
//   - "[]" matches one character listed in the brackets or in ranges.
//
// Patterns must match the whole path, starting from the repository root.
func matchGlobPattern(pattern, filePath string) bool {
	re, ok := patternCache.Load(pattern)
	if !ok {
		compiled, err := regexp.Compile(translateFilterPattern(pattern))
		if err != nil {
			// Unreachable by construction (every token emitted below is valid
			// regexp); fall back to an exact literal comparison.
			return pattern == filePath
		}
		re, _ = patternCache.LoadOrStore(pattern, compiled)
	}
	return re.(*regexp.Regexp).MatchString(filePath)
}

// translateFilterPattern converts a GitHub Actions filter pattern into an
// anchored Go regular expression, token by token so that the "?" and "+"
// quantifiers can attach to the token that precedes them. Constructs GitHub
// leaves undefined (a leading "?" or "+", an unclosed or non-alphanumeric
// bracket expression) are treated as literal characters.
func translateFilterPattern(pattern string) string {
	var tokens []string

	quantify := func(q byte) {
		if len(tokens) == 0 {
			// No preceding character: undefined in the GitHub grammar,
			// treated as a literal.
			tokens = append(tokens, regexp.QuoteMeta(string(q)))
			return
		}
		last := tokens[len(tokens)-1]
		tokens[len(tokens)-1] = "(?:" + last + ")" + string(q)
	}

	for i := 0; i < len(pattern); {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			// A "**" that is a whole segment followed by "/" collapses with
			// its slash, so the pattern also matches paths where the segment
			// is absent ("**/README.md" matches a root-level README.md).
			atSegmentStart := i == 0 || pattern[i-1] == '/'
			if atSegmentStart && i+2 < len(pattern) && pattern[i+2] == '/' {
				tokens = append(tokens, "(?:.*/)?")
				i += 3
				continue
			}
			tokens = append(tokens, ".*")
			i += 2
		case pattern[i] == '*':
			tokens = append(tokens, "[^/]*")
			i++
		case pattern[i] == '?':
			quantify('?')
			i++
		case pattern[i] == '+':
			quantify('+')
			i++
		case pattern[i] == '[':
			end := strings.IndexByte(pattern[i:], ']')
			if end > 1 && isBracketContent(pattern[i+1:i+end]) {
				tokens = append(tokens, pattern[i:i+end+1])
				i += end + 1
				continue
			}
			// Unclosed, empty, or out-of-grammar bracket: literal "[".
			tokens = append(tokens, regexp.QuoteMeta("["))
			i++
		default:
			tokens = append(tokens, regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}

	return `\A` + strings.Join(tokens, "") + `\z`
}

// isBracketContent reports whether a bracket expression's body stays within
// the documented GitHub grammar: alphanumeric characters and "-" ranges over
// a-z, A-Z, and 0-9. Anything else falls back to a literal "[".
func isBracketContent(body string) bool {
	for i := 0; i < len(body); i++ {
		c := body[i]
		isAlnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !isAlnum && c != '-' {
			return false
		}
	}
	return true
}
