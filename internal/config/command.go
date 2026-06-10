package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewCommand creates the parse-config command
func NewCommand() *cobra.Command {
	var configPath string
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "parse-config",
		Short: "Parse and validate cicd-config.yaml",
		Long: `Parse a cicd-config.yaml file and output its contents as JSON.
Validates the configuration and reports any errors.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runParseConfig(configPath, outputFormat)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "cicd-config.yaml", "Path to cicd-config.yaml")
	cmd.Flags().StringVarP(&outputFormat, "output", "o", "json", "Output format (json)")

	return cmd
}

func runParseConfig(configPath, outputFormat string) error {
	cfg, err := Parse(configPath)
	if err != nil {
		// Output error as JSON for workflow consumption
		result := ParseResult{
			Valid:  false,
			Errors: []string{err.Error()},
		}
		return outputJSON(result)
	}

	errors := Validate(cfg)

	result := ParseResult{
		Config:      *cfg,
		BuildNames:  GetBuildNames(cfg),
		DeployNames: GetDeployNames(cfg),
		Valid:       len(errors) == 0,
		Errors:      errors,
	}

	return outputJSON(result)
}

func outputJSON(v interface{}) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}
	return nil
}
