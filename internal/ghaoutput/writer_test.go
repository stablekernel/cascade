package ghaoutput

import (
	"os"
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
	require.Contains(t, string(content), "changelog<<EOF")
	require.Contains(t, string(content), "## v1.0.0")
	require.Contains(t, string(content), "EOF")
}
