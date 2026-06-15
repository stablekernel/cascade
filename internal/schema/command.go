package schema

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewCommand creates the schema command, which prints the embedded manifest
// JSON Schema. Editors can use the schema for autocomplete, type checking, and
// hover documentation while authoring a manifest.
func NewCommand() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Print the manifest JSON Schema",
		Long: `Print the JSON Schema for the cascade manifest.

The schema describes the structure, types, and allowed values of a manifest
file. Point your editor at it for autocomplete, type checking, and hover docs
while authoring .github/manifest.yaml. cascade parse-config remains the
authority for semantic and cross-field rules.

Examples:
  # Print the schema to stdout
  cascade schema

  # Write the schema to a file
  cascade schema --output manifest.schema.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data := Bytes()
			if output != "" {
				if err := os.WriteFile(output, data, 0o644); err != nil {
					return fmt.Errorf("writing schema to %q: %w", output, err)
				}
				return nil
			}
			if _, err := cmd.OutOrStdout().Write(data); err != nil {
				return fmt.Errorf("writing schema: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Write the schema to a file instead of stdout")

	return cmd
}
