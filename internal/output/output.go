// Package output provides helpers for outputting data to GitHub Actions and JSON.
package output

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/ghaoutput"
	"github.com/stablekernel/cascade/internal/globals"
)

// GitHubOutput writes a key-value pair to GITHUB_OUTPUT for workflow consumption.
// In GitHub Actions, this file is used to pass outputs between steps.
func GitHubOutput(key, value string) (err error) {
	outputFile := os.Getenv("GITHUB_OUTPUT")
	if outputFile == "" {
		// Not running in GitHub Actions, just log it
		_, _ = fmt.Fprintf(os.Stderr, "[OUTPUT] %s=%s\n", key, value)
		return nil
	}

	f, err := os.OpenFile(outputFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("failed to open GITHUB_OUTPUT: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	// Handle multiline values using heredoc syntax
	if strings.Contains(value, "\n") {
		delimiter := "EOF_" + key
		_, err = fmt.Fprintf(f, "%s<<%s\n%s\n%s\n", key, delimiter, value, delimiter)
	} else {
		_, err = fmt.Fprintf(f, "%s=%s\n", key, value)
	}

	return err
}

// GitHubOutputMultiple writes multiple key-value pairs to GITHUB_OUTPUT.
func GitHubOutputMultiple(outputs map[string]string) error {
	for key, value := range outputs {
		if err := GitHubOutput(key, value); err != nil {
			return err
		}
	}
	return nil
}

// GitHubStepSummary appends markdown content to the GitHub Actions step summary.
func GitHubStepSummary(content string) (err error) {
	summaryFile := os.Getenv("GITHUB_STEP_SUMMARY")
	if summaryFile == "" {
		// Not running in GitHub Actions, just log it
		_, _ = fmt.Fprintln(os.Stderr, content)
		return nil
	}

	f, err := os.OpenFile(summaryFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("failed to open GITHUB_STEP_SUMMARY: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	_, err = fmt.Fprintln(f, content)
	return err
}

// JSON outputs structured data as JSON to stdout.
// This is used with the --json flag for workflow consumption.
func JSON(data interface{}) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

// Result outputs data either as JSON (if --json flag is set) or as human-readable text.
// The textFn is called to produce human-readable output when not in JSON mode.
func Result(data interface{}, textFn func()) error {
	if globals.JSON() {
		return JSON(data)
	}
	textFn()
	return nil
}

// Notice outputs a GitHub Actions notice annotation.
func Notice(message string) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Printf("::notice::%s\n", message)
	} else {
		fmt.Printf("[NOTICE] %s\n", message)
	}
}

// Warning outputs a GitHub Actions warning annotation.
func Warning(message string) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Printf("::warning::%s\n", message)
	} else {
		fmt.Printf("[WARNING] %s\n", message)
	}
}

// Error outputs a GitHub Actions error annotation.
func Error(message string) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Printf("::error::%s\n", message)
	} else {
		fmt.Printf("[ERROR] %s\n", message)
	}
}

// SetupResult represents the output of the orchestrate setup command.
type SetupResult struct {
	HeadSHA          string            `json:"head_sha"`
	Version          string            `json:"version"`
	PreviousTag      string            `json:"previous_tag,omitempty"`
	ChangelogBaseSHA string            `json:"changelog_base_sha,omitempty"`
	RunBuilds        map[string]bool   `json:"run_builds,omitempty"`
	RunDeploys       map[string]string `json:"run_deploys,omitempty"` // "true", "false", or "pending"
	BaseSHAs         map[string]string `json:"base_shas,omitempty"`
}

// WriteGHAOutput writes the setup result to $GITHUB_OUTPUT for workflow consumption.
func (r *SetupResult) WriteGHAOutput() error {
	w := ghaoutput.New()

	// Core info
	w.Set("head_sha", r.HeadSHA)
	w.Set("version", r.Version)
	w.Set("previous_tag", r.PreviousTag)
	w.Set("changelog_base_sha", r.ChangelogBaseSHA)

	// Build decisions. Output keys are normalized to underscores via OutputKey
	// so they match the run_build_* identifiers the generated workflow consumes;
	// GitHub Actions parses a hyphen in an expression as subtraction.
	for name, run := range r.RunBuilds {
		w.SetBool(fmt.Sprintf("run_build_%s", config.OutputKey(name)), run)
	}

	// Deploy decisions
	for name, run := range r.RunDeploys {
		w.Set(fmt.Sprintf("run_deploy_%s", config.OutputKey(name)), run)
	}

	// Base SHAs for per-deployable change detection
	for name, sha := range r.BaseSHAs {
		w.Set(fmt.Sprintf("base_%s", config.OutputKey(name)), sha)
	}

	return w.Flush()
}

// PromotionPlanResult represents the output of the promote plan command.
type PromotionPlanResult struct {
	Promotions []PlannedPromotion `json:"promotions"`
	DryRun     bool               `json:"dry_run"`
}

// PlannedPromotion represents a single planned promotion.
type PlannedPromotion struct {
	Source  string `json:"source"`
	Target  string `json:"target"`
	SHA     string `json:"sha"`
	Version string `json:"version"`
}
