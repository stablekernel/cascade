package log

import (
	"bytes"
	"strings"
	"testing"
)

func TestLevelFiltering(t *testing.T) {
	tests := []struct {
		name      string
		level     Level
		logFunc   func(string, ...interface{})
		message   string
		shouldLog bool
	}{
		{"info at info level", LevelInfo, Info, "test", true},
		{"debug at info level", LevelInfo, Debug, "test", false},
		{"trace at info level", LevelInfo, Trace, "test", false},
		{"info at debug level", LevelDebug, Info, "test", true},
		{"debug at debug level", LevelDebug, Debug, "test", true},
		{"trace at debug level", LevelDebug, Trace, "test", false},
		{"info at trace level", LevelTrace, Info, "test", true},
		{"debug at trace level", LevelTrace, Debug, "test", true},
		{"trace at trace level", LevelTrace, Trace, "test", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			SetOutput(&buf)
			SetColors(false)
			SetLevel(tt.level)

			tt.logFunc(tt.message)

			output := buf.String()
			hasOutput := strings.Contains(output, tt.message)

			if hasOutput != tt.shouldLog {
				t.Errorf("expected shouldLog=%v, got output=%q", tt.shouldLog, output)
			}
		})
	}
}

func TestLogFormatting(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	SetColors(false)
	SetLevel(LevelDebug)

	Info("hello %s", "world")

	output := buf.String()
	if !strings.Contains(output, "[INFO]") {
		t.Errorf("expected [INFO] prefix, got %q", output)
	}
	if !strings.Contains(output, "hello world") {
		t.Errorf("expected formatted message, got %q", output)
	}
}

func TestWarnAndError(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	SetColors(false)
	SetLevel(LevelInfo) // Even at INFO, warn and error should show

	Warn("warning message")
	Error("error message")

	output := buf.String()
	if !strings.Contains(output, "[WARN]") {
		t.Errorf("expected [WARN], got %q", output)
	}
	if !strings.Contains(output, "[ERROR]") {
		t.Errorf("expected [ERROR], got %q", output)
	}
}

func TestDryRunPrefix(t *testing.T) {
	SetColors(false)
	prefix := DryRunPrefix()
	if prefix != "[DRY-RUN] " {
		t.Errorf("expected '[DRY-RUN] ', got %q", prefix)
	}
}

func TestDryRunPrefixWithColors(t *testing.T) {
	SetColors(true)
	prefix := DryRunPrefix()
	// Should contain ANSI codes when colors enabled
	if !strings.Contains(prefix, "\033[") {
		t.Errorf("expected ANSI color codes in prefix, got %q", prefix)
	}
	if !strings.Contains(prefix, "DRY-RUN") {
		t.Errorf("expected 'DRY-RUN' in prefix, got %q", prefix)
	}
}

func TestEnableTrace(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	SetColors(false)
	SetLevel(LevelDebug) // Start at DEBUG

	// Trace should not show at DEBUG level
	Trace("should not appear")
	if strings.Contains(buf.String(), "should not appear") {
		t.Error("trace message should not appear at DEBUG level")
	}

	buf.Reset()

	// Enable trace
	EnableTrace()

	// Now trace should show
	Trace("should appear")
	if !strings.Contains(buf.String(), "should appear") {
		t.Error("trace message should appear after EnableTrace()")
	}
}

func TestSection(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	SetColors(false)
	SetLevel(LevelDebug)

	Section("Test Section")

	output := buf.String()
	if !strings.Contains(output, "Test Section") {
		t.Errorf("expected section title in output, got %q", output)
	}
	if !strings.Contains(output, "─") {
		t.Errorf("expected section line characters, got %q", output)
	}
}

func TestSectionNotShownAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	SetColors(false)
	SetLevel(LevelInfo)

	Section("Hidden Section")

	if strings.Contains(buf.String(), "Hidden Section") {
		t.Error("section should not appear at INFO level")
	}
}

func TestSectionWithColors(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	SetColors(true)
	SetLevel(LevelDebug)

	Section("Colored Section")

	output := buf.String()
	if !strings.Contains(output, "\033[") {
		t.Errorf("expected ANSI color codes, got %q", output)
	}
}

func TestLogWithColors(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	SetColors(true)
	SetLevel(LevelDebug)

	Info("colored message")

	output := buf.String()
	if !strings.Contains(output, "\033[") {
		t.Errorf("expected ANSI color codes, got %q", output)
	}
	if !strings.Contains(output, "colored message") {
		t.Errorf("expected message content, got %q", output)
	}
}
