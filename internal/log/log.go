// Package log provides structured logging with INFO, DEBUG, and TRACE levels.
// DEBUG is visible by default; TRACE requires explicit opt-in.
package log

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Level represents a logging level.
type Level int

const (
	// LevelInfo shows key actions only.
	LevelInfo Level = iota
	// LevelDebug shows state values, decision reasoning, paths taken.
	LevelDebug
	// LevelTrace shows detailed internals, API calls, full payloads.
	LevelTrace
)

// ANSI color codes for terminal output.
const (
	colorReset  = "\033[0m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorGray   = "\033[90m"
)

// Logger provides structured logging functionality.
type Logger struct {
	level     Level
	output    io.Writer
	useColors bool
}

// defaultLogger is the package-level logger instance.
var defaultLogger = &Logger{
	level:     LevelDebug, // DEBUG visible by default
	output:    os.Stderr,
	useColors: true,
}

// SetLevel sets the logging level for the default logger.
func SetLevel(l Level) {
	defaultLogger.level = l
}

// SetOutput sets the output writer for the default logger.
func SetOutput(w io.Writer) {
	defaultLogger.output = w
}

// SetColors enables or disables ANSI color codes.
func SetColors(enabled bool) {
	defaultLogger.useColors = enabled
}

// EnableTrace sets the log level to TRACE.
func EnableTrace() {
	defaultLogger.level = LevelTrace
}

// TraceEnabled reports whether TRACE level logging is active.
func TraceEnabled() bool {
	return defaultLogger.level >= LevelTrace
}

// ParseLevel parses a string into a Level.
func ParseLevel(s string) Level {
	switch strings.ToLower(s) {
	case "trace":
		return LevelTrace
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	default:
		return LevelDebug
	}
}

// Info logs at INFO level - key actions like "promoting X to Y".
func Info(format string, args ...interface{}) {
	defaultLogger.log(LevelInfo, "INFO", colorGreen, format, args...)
}

// Debug logs at DEBUG level - state values, decision reasoning, paths taken.
func Debug(format string, args ...interface{}) {
	defaultLogger.log(LevelDebug, "DEBUG", colorCyan, format, args...)
}

// Trace logs at TRACE level - detailed internals, API calls, full payloads.
func Trace(format string, args ...interface{}) {
	defaultLogger.log(LevelTrace, "TRACE", colorGray, format, args...)
}

// Warn logs a warning message (always visible).
func Warn(format string, args ...interface{}) {
	defaultLogger.log(LevelInfo, "WARN", colorYellow, format, args...)
}

// Error logs an error message (always visible).
func Error(format string, args ...interface{}) {
	defaultLogger.log(LevelInfo, "ERROR", colorRed, format, args...)
}

// Section logs a section header for organizing output.
func Section(title string) {
	if defaultLogger.level < LevelDebug {
		return
	}
	line := strings.Repeat("─", 40)
	if defaultLogger.useColors {
		_, _ = fmt.Fprintf(defaultLogger.output, "%s%s %s %s%s\n", colorBlue, line[:10], title, line[:10], colorReset)
	} else {
		_, _ = fmt.Fprintf(defaultLogger.output, "%s %s %s\n", line[:10], title, line[:10])
	}
}

// log writes a formatted log message at the specified level.
func (l *Logger) log(level Level, label, color, format string, args ...interface{}) {
	if l.level < level {
		return
	}

	msg := fmt.Sprintf(format, args...)

	if l.useColors {
		_, _ = fmt.Fprintf(l.output, "%s[%s]%s %s\n", color, label, colorReset, msg)
	} else {
		_, _ = fmt.Fprintf(l.output, "[%s] %s\n", label, msg)
	}
}

// DryRunPrefix returns a prefix for dry-run mode messages.
func DryRunPrefix() string {
	if defaultLogger.useColors {
		return fmt.Sprintf("%s[DRY-RUN]%s ", colorYellow, colorReset)
	}
	return "[DRY-RUN] "
}
