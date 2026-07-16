package output

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/globals"
)

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
