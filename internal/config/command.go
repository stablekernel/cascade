package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/log"
)

// NewCommand creates the lint command, which parses and validates a manifest.
// By default it prints a human-readable report and exits non-zero when the
// manifest is invalid. With --json it emits the machine-readable ParseResult
// (the valid/errors/warnings surface the generated workflow lanes gate on).
func NewCommand() *cobra.Command {
	var configPath string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Lint and validate a cascade manifest",
		Long: `Parse a cascade manifest, validate it, and report any errors or warnings.
Unknown or misspelled keys are rejected with a "did you mean" suggestion.

By default the report is human-readable and the command exits non-zero when the
manifest is invalid. Use --json to emit the parsed config and validation result
as JSON (valid/errors/warnings), which the generated validation lanes consume.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLint(configPath, jsonOutput)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "cicd-config.yaml", "Path to the manifest file")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the parsed config and validation result as JSON")

	return cmd
}

func runLint(configPath string, jsonOutput bool) error {
	cfg, err := Parse(configPath)
	if err != nil {
		if jsonOutput {
			// Preserve the machine surface the lanes gate on: a parse failure is
			// reported as {valid:false, errors:[...]} with a zero exit so the
			// lane's jq gate, not the exit code, decides pass/fail.
			return outputJSON(ParseResult{Valid: false, Errors: []string{err.Error()}})
		}
		return err
	}

	errors := Validate(cfg)
	warnings, _ := cfg.ValidateSchemaVersion()

	if jsonOutput {
		for _, w := range warnings {
			log.Warn("%s", w)
		}
		return outputJSON(ParseResult{
			Config:      *cfg,
			BuildNames:  GetBuildNames(cfg),
			DeployNames: GetDeployNames(cfg),
			Valid:       len(errors) == 0,
			Errors:      errors,
			Warnings:    warnings,
		})
	}

	for _, w := range warnings {
		log.Warn("%s", w)
	}
	for _, e := range errors {
		log.Error("%s", e)
	}
	if len(errors) > 0 {
		return fmt.Errorf("manifest is invalid: %d error(s)", len(errors))
	}
	log.Info("manifest is valid")
	return nil
}

func outputJSON(v interface{}) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}
	return nil
}
