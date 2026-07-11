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
	t := reflect.TypeOf(TrunkConfig{})
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
		if suggestion := suggestField(key, known); suggestion != "" {
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
