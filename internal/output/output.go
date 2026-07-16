// Package output provides helpers for outputting data to GitHub Actions and JSON.
package output

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/ghaoutput"
	"github.com/stablekernel/cascade/internal/globals"
)

// JSON outputs structured data as JSON to stdout.
// This is used with the --json flag for workflow consumption.
func JSON(data any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

// Result outputs data either as JSON (if --json flag is set) or as human-readable text.
// The textFn is called to produce human-readable output when not in JSON mode.
func Result(data any, textFn func()) error {
	if globals.JSON() {
		return JSON(data)
	}
	textFn()
	return nil
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
