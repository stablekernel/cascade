package changes

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
)

// NewCommand creates the detect-changes command
func NewCommand() *cobra.Command {
	var configPath string
	var baseSHA string
	var headSHA string

	cmd := &cobra.Command{
		Use:   "detect-changes",
		Short: "Detect changes and determine triggered builds/deploys",
		Long: `Detect file changes between two git SHAs and determine which
builds and deploys should be triggered based on their trigger patterns.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDetectChanges(configPath, baseSHA, headSHA)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "cicd-config.yaml", "Path to cicd-config.yaml")
	cmd.Flags().StringVar(&baseSHA, "base-sha", "", "Base commit SHA")
	cmd.Flags().StringVar(&headSHA, "head-sha", "", "Head commit SHA")

	_ = cmd.MarkFlagRequired("base-sha")
	_ = cmd.MarkFlagRequired("head-sha")

	return cmd
}

func runDetectChanges(configPath, baseSHA, headSHA string) error {
	cfg, err := config.Parse(configPath)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	result, err := Detect(cfg, baseSHA, headSHA)
	if err != nil {
		return fmt.Errorf("detecting changes: %w", err)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
