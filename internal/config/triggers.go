package config

import (
	"path/filepath"
	"strings"
)

// IsNegationPattern reports whether a trigger pattern is an exclusion pattern
// (a leading "!"). Exclusion patterns mirror GitHub Actions' paths-ignore
// semantics: a file that matches an exclusion does not count as a trigger even
// if it also matches a positive pattern.
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
// Semantics (frozen v1 contract, GitHub Actions paths/paths-ignore combined
// behaviour): a file matches when it matches at least one positive pattern AND
// matches no negation pattern. This is intentionally order-independent: a path
// is a trigger if and only if it satisfies some positive include and is not
// excluded by any "!" pattern. When the pattern list contains only negation
// patterns (no positives), any file not excluded counts as a match, matching
// GitHub Actions, which treats a negation-only list like paths-ignore.
func MatchTrigger(patterns []string, file string) bool {
	hasPositive := false
	matchedPositive := false

	for _, pattern := range patterns {
		if IsNegationPattern(pattern) {
			if matchGlobPattern(stripNegation(pattern), file) {
				// Excluded by a "!" pattern: never a trigger.
				return false
			}
			continue
		}

		hasPositive = true
		if matchGlobPattern(pattern, file) {
			matchedPositive = true
		}
	}

	if !hasPositive {
		// Negation-only list: any file not excluded above is a match.
		return true
	}

	return matchedPositive
}

// MatchAnyTrigger reports whether any of the changed files is a trigger under
// the supplied pattern list (negation-aware via MatchTrigger). An empty pattern
// list means "always triggered". This is the shared evaluator that the CLI-side
// change detection uses so it agrees with the emitted GitHub Actions paths
// filter, which is generated verbatim from the same pattern list.
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

// MatchGlobPattern reports whether a single glob pattern matches a path. A
// leading "!" is stripped before matching, so the helper reports whether the
// bare glob matches; negation as an exclusion is applied at the pattern-list
// level by MatchTrigger. Supports "*" (single segment), "**" (any number of
// segments) and "?" (single character), matching the grammar used by the
// emitted GitHub Actions paths filter.
func MatchGlobPattern(pattern, path string) bool {
	return matchGlobPattern(stripNegation(pattern), path)
}

// matchGlobPattern matches a file path against a single (already
// negation-stripped) glob pattern.
func matchGlobPattern(pattern, path string) bool {
	if strings.Contains(pattern, "**") {
		return matchDoublestarPattern(pattern, path)
	}

	matched, _ := filepath.Match(pattern, path)
	return matched
}

// matchDoublestarPattern handles "**" glob patterns.
func matchDoublestarPattern(pattern, path string) bool {
	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")
	return matchGlobParts(patternParts, pathParts)
}

func matchGlobParts(pattern, path []string) bool {
	pi, ppi := 0, 0

	for pi < len(pattern) && ppi < len(path) {
		if pattern[pi] == "**" {
			if pi == len(pattern)-1 {
				// "**" at the end matches everything remaining.
				return true
			}

			// Try matching the remaining pattern at each subsequent position.
			for i := ppi; i <= len(path); i++ {
				if matchGlobParts(pattern[pi+1:], path[i:]) {
					return true
				}
			}
			return false
		}

		matched, _ := filepath.Match(pattern[pi], path[ppi])
		if !matched {
			return false
		}

		pi++
		ppi++
	}

	// Any remaining pattern parts must all be "**" to match.
	for pi < len(pattern) {
		if pattern[pi] != "**" {
			return false
		}
		pi++
	}

	return ppi == len(path)
}
