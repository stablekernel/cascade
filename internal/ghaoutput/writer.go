// Package ghaoutput writes step outputs to the file named by
// $GITHUB_OUTPUT, using plain key=value lines for single-line values and
// randomized heredoc delimiters for multiline values so untrusted
// content cannot forge additional outputs.
package ghaoutput

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Writer handles writing outputs to GitHub Actions $GITHUB_OUTPUT file
type Writer struct {
	outputs   map[string]string
	multiline map[string]string
}

// New creates a new Writer instance
func New() *Writer {
	return &Writer{
		outputs:   make(map[string]string),
		multiline: make(map[string]string),
	}
}

// Set sets a simple key-value output
func (w *Writer) Set(key, value string) {
	w.outputs[key] = value
}

// SetBool sets a boolean output as a string
func (w *Writer) SetBool(key string, value bool) {
	w.outputs[key] = fmt.Sprintf("%v", value)
}

// SetJSON marshals a value to JSON and sets it as output
func (w *Writer) SetJSON(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal %s to JSON: %w", key, err)
	}
	w.outputs[key] = string(data)
	return nil
}

// SetMultiline sets a multiline output value
func (w *Writer) SetMultiline(key, value string) {
	w.multiline[key] = value
}

// FormatLine renders one $GITHUB_OUTPUT entry. Single-line values use the
// plain key=value form. Values containing a newline use GitHub's documented
// heredoc form with a random per-value delimiter: output values can carry
// arbitrary text (commit messages reach changelog outputs), and with a fixed
// or key-derived delimiter a value containing that delimiter as a bare line
// terminates the block early, so the remaining lines parse as forged outputs.
func FormatLine(key, value string) string {
	if !strings.Contains(value, "\n") {
		return fmt.Sprintf("%s=%s\n", key, value)
	}
	delim := heredocDelimiter(value)
	return fmt.Sprintf("%s<<%s\n%s\n%s\n", key, delim, value, delim)
}

// heredocDelimiter returns a random delimiter guaranteed not to occur in
// value. 16 random bytes make collision astronomically unlikely; the
// containment check makes it impossible.
func heredocDelimiter(value string) string {
	for {
		var buf [16]byte
		// crypto/rand.Read never returns an error (guaranteed since Go 1.24).
		_, _ = rand.Read(buf[:])
		delim := "cascade_output_" + hex.EncodeToString(buf[:])
		if !strings.Contains(value, delim) {
			return delim
		}
	}
}

// render produces the full $GITHUB_OUTPUT content for this writer. Multiline
// values registered via Set (not just SetMultiline) also take the heredoc
// path: a plain key=value write of a value with an embedded newline would
// inject the trailing lines as extra output entries. Each map renders in
// sorted key order so repeated runs of the same writer produce identical
// output files.
func (w *Writer) render() string {
	var sb strings.Builder
	for _, k := range sortedKeys(w.outputs) {
		sb.WriteString(FormatLine(k, w.outputs[k]))
	}
	for _, k := range sortedKeys(w.multiline) {
		sb.WriteString(FormatLine(k, w.multiline[k]))
	}
	return sb.String()
}

// sortedKeys returns the map's keys in sorted order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Flush writes all outputs to $GITHUB_OUTPUT or stdout. The error return is
// named so the deferred Close can surface a failure to persist the buffered
// content; with an unnamed return the deferred assignment would mutate a
// local after the return value was already copied, silently dropping the
// flush-to-disk error.
func (w *Writer) Flush() (err error) {
	content := w.render()

	outputFile := os.Getenv("GITHUB_OUTPUT")
	if outputFile == "" {
		// Not in GHA, print to stdout instead
		fmt.Print(content)
		return nil
	}

	f, err := os.OpenFile(outputFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("failed to open $GITHUB_OUTPUT: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	_, err = f.WriteString(content)
	return err
}
