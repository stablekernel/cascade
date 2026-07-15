package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/globals"
)

func TestGitHubOutput(t *testing.T) {
	// Create temp file for GITHUB_OUTPUT
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "output.txt")

	// Set env var (t.Setenv handles cleanup automatically)
	t.Setenv("GITHUB_OUTPUT", outputFile)

	// Test simple value
	err := GitHubOutput("version", "v1.2.3")
	if err != nil {
		t.Fatalf("GitHubOutput failed: %v", err)
	}

	// Read file
	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	if !strings.Contains(string(content), "version=v1.2.3") {
		t.Errorf("expected version=v1.2.3, got %s", content)
	}
}

func TestGitHubOutputMultiline(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "output.txt")

	t.Setenv("GITHUB_OUTPUT", outputFile)

	multilineValue := "line1\nline2\nline3"
	err := GitHubOutput("changelog", multilineValue)
	if err != nil {
		t.Fatalf("GitHubOutput failed: %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	// Should use heredoc syntax
	if !strings.Contains(string(content), "changelog<<") {
		t.Errorf("expected heredoc syntax, got %s", content)
	}
	if !strings.Contains(string(content), "line1\nline2\nline3") {
		t.Errorf("expected multiline content preserved, got %s", content)
	}
}

// TestGitHubOutputMultilineDelimiterNotForgeable proves a value carrying the
// old deterministic delimiter as one of its own lines cannot terminate the
// heredoc early and forge additional outputs, and that the delimiter is no
// longer derivable from the key.
func TestGitHubOutputMultilineDelimiterNotForgeable(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "output.txt")
	t.Setenv("GITHUB_OUTPUT", outputFile)

	injected := "line1\nEOF_changelog\nforged=v9.9.9\nline2"
	if err := GitHubOutput("changelog", injected); err != nil {
		t.Fatalf("GitHubOutput failed: %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	lines := strings.Split(string(content), "\n")
	open := strings.SplitN(lines[0], "<<", 2)
	if len(open) != 2 || open[0] != "changelog" {
		t.Fatalf("first line must be changelog<<delimiter, got %q", lines[0])
	}
	delim := open[1]
	if delim == "EOF_changelog" {
		t.Fatalf("delimiter must not be derivable from the key, got %q", delim)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == delim {
			end = i
			break
		}
	}
	if end == -1 {
		t.Fatalf("closing delimiter %q not found in %q", delim, content)
	}
	if got := strings.Join(lines[1:end], "\n"); got != injected {
		t.Errorf("value must round-trip intact, got %q want %q", got, injected)
	}
}

func TestGitHubOutputMultiple(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "output.txt")

	t.Setenv("GITHUB_OUTPUT", outputFile)

	outputs := map[string]string{
		"version": "v1.0.0",
		"sha":     "abc123",
	}

	err := GitHubOutputMultiple(outputs)
	if err != nil {
		t.Fatalf("GitHubOutputMultiple failed: %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "version=v1.0.0") {
		t.Errorf("expected version output, got %s", contentStr)
	}
	if !strings.Contains(contentStr, "sha=abc123") {
		t.Errorf("expected sha output, got %s", contentStr)
	}
}

// TestGitHubOutputMultipleSortedOrder proves the entries render in sorted key
// order rather than Go's randomized map-range order, so repeated runs produce
// identical output files.
func TestGitHubOutputMultipleSortedOrder(t *testing.T) {
	tmpDir := t.TempDir()
	outputFile := filepath.Join(tmpDir, "output.txt")
	t.Setenv("GITHUB_OUTPUT", outputFile)

	outputs := map[string]string{
		"kilo": "1", "alpha": "2", "hotel": "3", "bravo": "4", "juliet": "5",
		"delta": "6", "india": "7", "charlie": "8", "golf": "9", "echo": "10",
	}
	if err := GitHubOutputMultiple(outputs); err != nil {
		t.Fatalf("GitHubOutputMultiple failed: %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	var got []string
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("unexpected output line %q", line)
		}
		got = append(got, key)
	}
	want := make([]string, 0, len(outputs))
	for k := range outputs {
		want = append(want, k)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keys must render sorted: got %v want %v", got, want)
	}
}

func TestJSON(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	data := SetupResult{
		HeadSHA: "abc123",
		Version: "v1.2.3",
	}

	err := JSON(data)
	if err != nil {
		t.Fatalf("JSON failed: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("failed to close pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("failed to read from pipe: %v", err)
	}
	os.Stdout = oldStdout

	output := buf.String()

	// Parse the JSON back
	var result SetupResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result.HeadSHA != "abc123" {
		t.Errorf("expected HeadSHA=abc123, got %s", result.HeadSHA)
	}
	if result.Version != "v1.2.3" {
		t.Errorf("expected Version=v1.2.3, got %s", result.Version)
	}
}

func TestNoticeWarningError(t *testing.T) {
	// Test when not in GitHub Actions - ensure GITHUB_ACTIONS is unset
	t.Setenv("GITHUB_ACTIONS", "")

	// These should not panic and just print to stdout
	Notice("test notice")
	Warning("test warning")
	Error("test error")
}

func TestNoticeInGitHubActions(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	t.Setenv("GITHUB_ACTIONS", "true")

	Notice("test notice")

	if err := w.Close(); err != nil {
		t.Fatalf("failed to close pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("failed to read from pipe: %v", err)
	}
	os.Stdout = oldStdout

	output := buf.String()
	if !strings.Contains(output, "::notice::test notice") {
		t.Errorf("expected ::notice:: annotation, got %s", output)
	}
}

func TestWarningInGitHubActions(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	t.Setenv("GITHUB_ACTIONS", "true")

	Warning("test warning")

	if err := w.Close(); err != nil {
		t.Fatalf("failed to close pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("failed to read from pipe: %v", err)
	}
	os.Stdout = oldStdout

	output := buf.String()
	if !strings.Contains(output, "::warning::test warning") {
		t.Errorf("expected ::warning:: annotation, got %s", output)
	}
}

func TestErrorInGitHubActions(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	t.Setenv("GITHUB_ACTIONS", "true")

	Error("test error")

	if err := w.Close(); err != nil {
		t.Fatalf("failed to close pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("failed to read from pipe: %v", err)
	}
	os.Stdout = oldStdout

	output := buf.String()
	if !strings.Contains(output, "::error::test error") {
		t.Errorf("expected ::error:: annotation, got %s", output)
	}
}

func TestGitHubStepSummary(t *testing.T) {
	tmpDir := t.TempDir()
	summaryFile := filepath.Join(tmpDir, "summary.txt")

	t.Setenv("GITHUB_STEP_SUMMARY", summaryFile)

	err := GitHubStepSummary("## Test Summary\n\nThis is a test")
	if err != nil {
		t.Fatalf("GitHubStepSummary failed: %v", err)
	}

	content, err := os.ReadFile(summaryFile)
	if err != nil {
		t.Fatalf("failed to read summary file: %v", err)
	}

	if !strings.Contains(string(content), "## Test Summary") {
		t.Errorf("expected summary content, got %s", content)
	}
}

func TestGitHubStepSummaryNoEnvVar(t *testing.T) {
	// Ensure env var is not set
	t.Setenv("GITHUB_STEP_SUMMARY", "")

	// Should not error, just print to stderr
	err := GitHubStepSummary("test content")
	if err != nil {
		t.Fatalf("GitHubStepSummary should not error when env not set: %v", err)
	}
}

func TestGitHubOutputNoEnvVar(t *testing.T) {
	// Ensure env var is not set
	t.Setenv("GITHUB_OUTPUT", "")

	// Should not error, just print to stderr
	err := GitHubOutput("key", "value")
	if err != nil {
		t.Fatalf("GitHubOutput should not error when env not set: %v", err)
	}
}

func TestResult(t *testing.T) {
	// Import globals for SetJSON
	t.Run("text mode", func(t *testing.T) {
		// Ensure JSON mode is off
		globals.SetJSON(false)

		called := false
		data := map[string]string{"key": "value"}

		err := Result(data, func() {
			called = true
		})
		if err != nil {
			t.Fatalf("Result failed: %v", err)
		}

		if !called {
			t.Error("expected textFn to be called in non-JSON mode")
		}
	})

	t.Run("JSON mode", func(t *testing.T) {
		// Capture stdout
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		globals.SetJSON(true)
		defer globals.SetJSON(false)

		called := false
		data := map[string]string{"key": "value"}

		err := Result(data, func() {
			called = true
		})
		if err != nil {
			t.Fatalf("Result failed: %v", err)
		}

		if err := w.Close(); err != nil {
			t.Fatalf("failed to close pipe writer: %v", err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(r); err != nil {
			t.Fatalf("failed to read from pipe: %v", err)
		}
		os.Stdout = oldStdout

		if called {
			t.Error("expected textFn NOT to be called in JSON mode")
		}

		output := buf.String()
		if !strings.Contains(output, `"key"`) {
			t.Errorf("expected JSON output, got %s", output)
		}
	})
}
