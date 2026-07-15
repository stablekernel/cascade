package ghaoutput

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriter_Set(t *testing.T) {
	w := New()
	w.Set("key1", "value1")
	w.Set("key2", "value2")

	require.Equal(t, "value1", w.outputs["key1"])
	require.Equal(t, "value2", w.outputs["key2"])
}

func TestWriter_SetBool(t *testing.T) {
	w := New()
	w.SetBool("is_true", true)
	w.SetBool("is_false", false)

	require.Equal(t, "true", w.outputs["is_true"])
	require.Equal(t, "false", w.outputs["is_false"])
}

func TestWriter_SetJSON(t *testing.T) {
	w := New()
	err := w.SetJSON("list", []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Equal(t, `["a","b","c"]`, w.outputs["list"])

	err = w.SetJSON("obj", map[string]int{"count": 42})
	require.NoError(t, err)
	require.Equal(t, `{"count":42}`, w.outputs["obj"])
}

func TestWriter_SetMultiline(t *testing.T) {
	w := New()
	w.SetMultiline("changelog", "## v1.0.0\n- First release\n- Breaking change")

	require.Equal(t, "## v1.0.0\n- First release\n- Breaking change", w.multiline["changelog"])
}

func TestWriter_FlushToFile(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := tmpDir + "/github_output"

	t.Setenv("GITHUB_OUTPUT", outputFile)

	w := New()
	w.Set("source_env", "dev")
	w.Set("target_env", "test")
	err := w.Flush()

	require.NoError(t, err)

	content, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	require.Contains(t, string(content), "source_env=dev")
	require.Contains(t, string(content), "target_env=test")
}

func TestWriter_FlushMultilineToFile(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := tmpDir + "/github_output"

	t.Setenv("GITHUB_OUTPUT", outputFile)

	w := New()
	w.SetMultiline("changelog", "## v1.0.0\n- First release")
	err := w.Flush()

	require.NoError(t, err)

	content, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	key, value, _ := parseHeredoc(t, string(content))
	require.Equal(t, "changelog", key)
	require.Equal(t, "## v1.0.0\n- First release", value)
}

// parseHeredoc parses a single $GITHUB_OUTPUT heredoc entry the way the
// Actions runner does: `key<<delim`, value lines, then a line equal to delim.
func parseHeredoc(t *testing.T, content string) (key, value, delim string) {
	t.Helper()
	lines := strings.Split(content, "\n")
	require.GreaterOrEqual(t, len(lines), 3, "heredoc needs open, value, close lines: %q", content)
	open := strings.SplitN(lines[0], "<<", 2)
	require.Len(t, open, 2, "first line must be key<<delimiter: %q", lines[0])
	key, delim = open[0], open[1]
	for i := 1; i < len(lines); i++ {
		if lines[i] == delim {
			return key, strings.Join(lines[1:i], "\n"), delim
		}
	}
	t.Fatalf("closing delimiter %q not found in %q", delim, content)
	return "", "", ""
}

// TestWriter_MultilineDelimiterNotForgeable proves a value that carries a bare
// "EOF" line (arbitrary commit-message text reaches changelog outputs) cannot
// terminate the heredoc early and forge additional step outputs.
func TestWriter_MultilineDelimiterNotForgeable(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := tmpDir + "/github_output"
	t.Setenv("GITHUB_OUTPUT", outputFile)

	injected := "line one\nEOF\nforged_version=v9.9.9\nline two"
	w := New()
	w.SetMultiline("changelog", injected)
	require.NoError(t, w.Flush())

	content, err := os.ReadFile(outputFile)
	require.NoError(t, err)

	key, value, delim := parseHeredoc(t, string(content))
	require.Equal(t, "changelog", key)
	require.Equal(t, injected, value, "the full value must round-trip; an early "+
		"terminator means the remainder parses as forged outputs")
	require.NotEqual(t, "EOF", delim, "a fixed EOF delimiter is guessable and forgeable")
	require.NotContains(t, injected, delim, "delimiter must not occur in the value")
}

// TestWriter_MultilineDelimiterIsRandom proves the delimiter is per-value
// random rather than deterministic, so a value author cannot predict it.
func TestWriter_MultilineDelimiterIsRandom(t *testing.T) {
	tmpDir := t.TempDir()

	delims := make(map[string]bool)
	for i := 0; i < 2; i++ {
		outputFile := fmt.Sprintf("%s/github_output_%d", tmpDir, i)
		t.Setenv("GITHUB_OUTPUT", outputFile)
		w := New()
		w.SetMultiline("body", "a\nb")
		require.NoError(t, w.Flush())
		content, err := os.ReadFile(outputFile)
		require.NoError(t, err)
		_, _, delim := parseHeredoc(t, string(content))
		delims[delim] = true
	}
	require.Len(t, delims, 2, "two flushes of the same value must use different delimiters")
}

// TestWriter_FlushOrderIsSorted proves the rendered output is deterministic:
// entries appear in sorted key order (Set entries first, then SetMultiline
// entries), never in Go's randomized map-range order.
func TestWriter_FlushOrderIsSorted(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := tmpDir + "/github_output"
	t.Setenv("GITHUB_OUTPUT", outputFile)

	keys := []string{"kilo", "alpha", "hotel", "bravo", "juliet", "delta", "india", "charlie", "golf", "echo", "foxtrot"}
	w := New()
	for _, k := range keys {
		w.Set(k, "v")
	}
	w.SetMultiline("zulu", "a\nb")
	w.SetMultiline("mike", "c\nd")
	require.NoError(t, w.Flush())

	content, err := os.ReadFile(outputFile)
	require.NoError(t, err)

	var got []string
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasSuffix(line, "=v") {
			got = append(got, strings.TrimSuffix(line, "=v"))
		}
	}
	want := append([]string(nil), keys...)
	sort.Strings(want)
	require.Equal(t, want, got, "plain outputs must render in sorted key order")

	mikeIdx := strings.Index(string(content), "mike<<")
	zuluIdx := strings.Index(string(content), "zulu<<")
	require.GreaterOrEqual(t, mikeIdx, 0)
	require.GreaterOrEqual(t, zuluIdx, 0)
	require.Less(t, mikeIdx, zuluIdx, "multiline outputs must render in sorted key order")
}

// TestWriter_FlushSurfacesFinalWriteError proves a failure to persist the
// buffered content is returned, not silently dropped: writing to a full
// device fails and Flush must report it.
func TestWriter_FlushSurfacesFinalWriteError(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full not available on this platform")
	}
	t.Setenv("GITHUB_OUTPUT", "/dev/full")

	w := New()
	w.Set("key", "value")
	require.Error(t, w.Flush(), "a failed write to $GITHUB_OUTPUT must surface")
}

// TestWriter_SetMultilineValueRoutedToHeredoc proves a newline smuggled into a
// plain Set value cannot inject a second key=value output line.
func TestWriter_SetMultilineValueRoutedToHeredoc(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := tmpDir + "/github_output"
	t.Setenv("GITHUB_OUTPUT", outputFile)

	w := New()
	w.Set("version", "v1.0.0\nforged=true")
	require.NoError(t, w.Flush())

	content, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	require.NotContains(t, string(content), "version=v1.0.0\nforged=true\n",
		"a plain key=value write of a multiline value injects a forged output line")
	key, value, _ := parseHeredoc(t, string(content))
	require.Equal(t, "version", key)
	require.Equal(t, "v1.0.0\nforged=true", value)
}
