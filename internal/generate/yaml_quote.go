package generate

import "strings"

// yamlSingleQuote renders s as a YAML single-quoted scalar. The only escape
// in that style is doubling embedded single quotes, so any operator-authored
// value (apostrophes included) round-trips intact instead of breaking the
// emitted document. Callers pass single-line values; JSON-serialized input
// blobs and dispatch-input defaults never carry raw newlines.
func yamlSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
