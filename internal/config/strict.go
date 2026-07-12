package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// knownTrunkFields returns the set of modeled top-level config keys, derived
// from the yaml struct tags on TrunkConfig. Reflecting the tags keeps the
// known-field set in lockstep with the struct, so a new field never has to be
// hand-registered in a second place. The inline catch-all (Extra) and any
// yaml:"-" field are excluded.
func knownTrunkFields() []string {
	return knownYAMLFields(reflect.TypeOf(TrunkConfig{}))
}

// knownComponentFields returns the set of modeled per-component config keys,
// derived from the yaml struct tags on ComponentConfig. Like knownTrunkFields it
// reflects the tags so the set stays in lockstep with the struct, backing the
// component-level did-you-mean suggestion.
func knownComponentFields() []string {
	return knownYAMLFields(reflect.TypeOf(ComponentConfig{}))
}

// knownEnvironmentEntryFields returns the modeled yaml keys accepted on an
// environments entry: the entry's own name and role plus every inline
// per-environment setting promoted from EnvironmentConfig (the folded former
// environment_config block). It backs the entry-level did-you-mean suggestion.
func knownEnvironmentEntryFields() []string {
	fields := []string{"name", "role"}
	fields = append(fields, knownYAMLFields(reflect.TypeOf(EnvironmentConfig{}))...)
	sort.Strings(fields)
	return fields
}

// knownYAMLFields returns the modeled yaml key names for a struct type, derived
// from its yaml struct tags. The inline catch-all (yaml:",inline") and any
// yaml:"-" field are excluded. Reflecting the tags keeps every known-field set
// in lockstep with its struct, so a renamed or added field never has to be
// hand-registered in a second place.
func knownYAMLFields(t reflect.Type) []string {
	fields := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			// yaml:",inline" and similar carry no field name.
			continue
		}
		fields = append(fields, name)
	}
	sort.Strings(fields)
	return fields
}

// validateUnknownCallbackFields rejects any key on a build or deploy callback
// that is not a modeled field of the callback struct. Unknown keys are captured
// by the inline Extra map; each becomes a hard error with a "did you mean X?"
// suggestion when a close modeled field exists. This mirrors validateUnknownTopLevel
// one level down, so a renamed key (for example the former artifact:, now
// artifact_upload:) is caught rather than silently dropped by the yaml decoder.
func validateUnknownCallbackFields(scope string, extra map[string]any, known []string) []string {
	if len(extra) == 0 {
		return nil
	}
	var errs []string
	for _, key := range sortedKeys(toAnyKeyed(extra)) {
		if suggestion := fieldSuggestion(key, known, legacyCallbackFieldRenames); suggestion != "" {
			errs = append(errs, fmt.Sprintf("%s has unknown field %q; did you mean %q?", scope, key, suggestion))
		} else {
			errs = append(errs, fmt.Sprintf("%s has unknown field %q", scope, key))
		}
	}
	return errs
}

// legacyCallbackFieldRenames maps a former callback key to its current name so
// the did-you-mean points at the intended replacement rather than the nearest
// field by edit distance. The singular artifact: (the upload passthrough) was
// renamed to artifact_upload:; without this hint a plain Levenshtein match would
// steer a user to the unrelated plural artifacts: (release artifacts), which is
// exactly the one-letter-apart confusion the rename removes.
var legacyCallbackFieldRenames = map[string]string{
	"artifact": "artifact_upload",
}

// legacyFieldRenames maps a former top-level or per-component key to its current
// name so the did-you-mean points at the intended replacement rather than the
// nearest field by edit distance. The standalone tag_prefix: was folded into
// tag_grammar.prefix:; without this hint a Levenshtein match would not surface
// the nested destination, since it is not a single top-level field name.
var legacyFieldRenames = map[string]string{
	"tag_prefix": "tag_grammar.prefix",
	// The separate environment_config map was folded into the environments list:
	// per-environment settings now ride on each environments entry as inline keys
	// ({name: dev, wait_timer: 5, ...}). Point a stale environment_config: at the
	// list rather than at the nearest field by edit distance.
	"environment_config": "environments",
}

// fieldSuggestion returns the best did-you-mean replacement for an unknown key:
// an explicit legacy-rename target when one is registered, otherwise the closest
// modeled field by Levenshtein distance, or "" when nothing is close enough.
func fieldSuggestion(key string, known []string, renames map[string]string) string {
	if suggestion, ok := renames[key]; ok {
		return suggestion
	}
	return suggestField(key, known)
}

// validateUnknownTopLevel rejects any top-level config key that is not a modeled
// TrunkConfig field. Unknown keys are captured by the inline Extra map; each one
// becomes a hard error with a "did you mean X?" suggestion when a close modeled
// field exists. This mirrors the per-component strictness in validateComponents
// one level up, closing the previously-lenient top level.
func validateUnknownTopLevel(cfg *TrunkConfig) []string {
	if len(cfg.Extra) == 0 {
		return nil
	}
	known := knownTrunkFields()
	var errs []string
	for _, key := range sortedKeys(toAnyKeyed(cfg.Extra)) {
		if suggestion := fieldSuggestion(key, known, legacyFieldRenames); suggestion != "" {
			errs = append(errs, fmt.Sprintf("config has unknown field %q; did you mean %q?", key, suggestion))
		} else {
			errs = append(errs, fmt.Sprintf("config has unknown field %q", key))
		}
	}
	return errs
}

// suggestField returns the modeled field closest to key by Levenshtein distance,
// or "" when nothing is close enough to be a helpful suggestion. The threshold
// scales with the key length so short keys need a near-exact match while longer
// keys tolerate a typo or two, avoiding misleading suggestions for a genuinely
// out-of-place field.
func suggestField(key string, known []string) string {
	threshold := len(key) / 3
	if threshold < 2 {
		threshold = 2
	}
	best := ""
	bestDist := threshold + 1
	for _, candidate := range known {
		d := levenshtein(key, candidate)
		if d < bestDist {
			bestDist = d
			best = candidate
		}
	}
	if bestDist > threshold {
		return ""
	}
	return best
}

// levenshtein computes the edit distance between two strings using the standard
// two-row dynamic-programming table.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
