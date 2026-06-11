package ghaoutput

import (
	"encoding/json"
	"fmt"
	"os"
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
func (w *Writer) SetJSON(key string, value interface{}) error {
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

// Flush writes all outputs to $GITHUB_OUTPUT or stdout
func (w *Writer) Flush() error {
	outputFile := os.Getenv("GITHUB_OUTPUT")
	if outputFile == "" {
		// Not in GHA, print to stdout instead
		for k, v := range w.outputs {
			fmt.Printf("%s=%s\n", k, v)
		}
		for k, v := range w.multiline {
			fmt.Printf("%s<<EOF\n%s\nEOF\n", k, v)
		}
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

	var sb strings.Builder
	for k, v := range w.outputs {
		fmt.Fprintf(&sb, "%s=%s\n", k, v)
	}
	for k, v := range w.multiline {
		fmt.Fprintf(&sb, "%s<<EOF\n%s\nEOF\n", k, v)
	}

	_, err = f.WriteString(sb.String())
	return err
}
