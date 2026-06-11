package status

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
)

// NewCommand creates the status parent command with subcommands.
func NewCommand() *cobra.Command {
	var configPath string
	var manifestKey string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Query deployed state from the manifest",
		Long: `Read-only commands for inspecting deployed state recorded in the manifest.

Subcommands:
  env    <name>          - Version, SHA, committed_at for a single environment
  build  <name>          - Build state (SHA, artifact_id, tags) for a build within an environment
  deploy <name>          - Deploy state (SHA, timestamp) for a deploy within an environment

When no subcommand is given, all environments are summarised together with latest_release.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatusAll(configPath, manifestKey, jsonOutput)
		},
	}

	cmd.PersistentFlags().StringVarP(&configPath, "config", "c", "", "Path to manifest file (default: .github/manifest.yaml)")
	cmd.PersistentFlags().StringVar(&manifestKey, "key", config.DefaultManifestKey, "Top-level manifest key (default: ci)")
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	cmd.AddCommand(newEnvCommand(&configPath, &manifestKey, &jsonOutput))
	cmd.AddCommand(newBuildCommand(&configPath, &manifestKey, &jsonOutput))
	cmd.AddCommand(newDeployCommand(&configPath, &manifestKey, &jsonOutput))
	cmd.AddCommand(newConsistencyCommand(&configPath, &manifestKey, &jsonOutput))

	return cmd
}

// -------- "status" (all-envs summary) --------

func runStatusAll(configPath, key string, jsonOutput bool) error {
	file, err := loadManifest(configPath, key)
	if err != nil {
		return err
	}

	if jsonOutput {
		return printJSON(buildAllOutput(file))
	}
	printAll(file)
	return nil
}

// allOutput is the JSON shape for the top-level status command.
type allOutput struct {
	Environments  map[string]*config.EnvState `json:"environments"`
	LatestRelease *config.LatestReleaseState  `json:"latest_release,omitempty"`
}

func buildAllOutput(file *config.CICDFile) allOutput {
	return allOutput{
		Environments:  file.State,
		LatestRelease: file.LatestRelease,
	}
}

func printAll(file *config.CICDFile) {
	envs := sortedKeys(file.State)

	if len(envs) == 0 {
		fmt.Println("no environments found in state")
	}

	for _, env := range envs {
		state := file.State[env]
		fmt.Printf("environment: %s\n", env)
		printEnvState(state, "  ")
		fmt.Println()
	}

	if file.LatestRelease != nil && file.LatestRelease.Version != "" {
		fmt.Println("latest_release:")
		fmt.Printf("  version:     %s\n", orDash(file.LatestRelease.Version))
		fmt.Printf("  sha:         %s\n", orDash(file.LatestRelease.SHA))
		fmt.Printf("  released_on: %s\n", orDash(file.LatestRelease.ReleasedOn))
		fmt.Printf("  released_by: %s\n", orDash(file.LatestRelease.ReleasedBy))
	}
}

// -------- "status env <name>" --------

func newEnvCommand(configPath, key *string, jsonOutput *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env <name>",
		Short: "Show deployed state for a single environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnv(*configPath, *key, *jsonOutput, args[0])
		},
	}
	return cmd
}

func runEnv(configPath, key string, jsonOutput bool, envName string) error {
	file, err := loadManifest(configPath, key)
	if err != nil {
		return err
	}

	state, ok := file.State[envName]
	if !ok || state == nil {
		state = &config.EnvState{}
	}

	if jsonOutput {
		type envJSON struct {
			Environment string           `json:"environment"`
			State       *config.EnvState `json:"state"`
		}
		return printJSON(envJSON{Environment: envName, State: state})
	}

	fmt.Printf("environment: %s\n", envName)
	printEnvState(state, "")
	return nil
}

func printEnvState(state *config.EnvState, indent string) {
	if state == nil {
		fmt.Printf("%s(no state recorded)\n", indent)
		return
	}
	fmt.Printf("%sversion:      %s\n", indent, orDash(state.Version))
	fmt.Printf("%ssha:          %s\n", indent, orDash(state.SHA))
	fmt.Printf("%scommitted_at: %s\n", indent, orDash(state.CommittedAt))
	fmt.Printf("%scommitted_by: %s\n", indent, orDash(state.CommittedBy))

	if state.Ref != "" {
		fmt.Printf("%sref:          %s\n", indent, state.Ref)
	}
	if state.BaseSHA != "" {
		fmt.Printf("%sbase_sha:     %s\n", indent, state.BaseSHA)
	}
	if len(state.Patches) > 0 {
		fmt.Printf("%spatches:      %s\n", indent, strings.Join(state.Patches, ", "))
	}

	if len(state.Builds) > 0 {
		fmt.Printf("%sbuilds:\n", indent)
		for name, b := range state.Builds {
			fmt.Printf("%s  %s: sha=%s built_at=%s\n", indent, name, orDash(b.SHA), orDash(b.BuiltAt))
		}
	}
	if len(state.Deploys) > 0 {
		fmt.Printf("%sdeploys:\n", indent)
		for name, d := range state.Deploys {
			fmt.Printf("%s  %s: sha=%s deployed_at=%s\n", indent, name, orDash(d.SHA), orDash(d.DeployedAt))
		}
	}
}

// -------- "status build <name> --env <env>" --------

func newBuildCommand(configPath, key *string, jsonOutput *bool) *cobra.Command {
	var envName string

	cmd := &cobra.Command{
		Use:   "build <name>",
		Short: "Show build state within an environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuild(*configPath, *key, *jsonOutput, envName, args[0])
		},
	}

	cmd.Flags().StringVar(&envName, "env", "", "Environment name (required)")
	_ = cmd.MarkFlagRequired("env")

	return cmd
}

func runBuild(configPath, key string, jsonOutput bool, envName, buildName string) error {
	file, err := loadManifest(configPath, key)
	if err != nil {
		return err
	}

	state, ok := file.State[envName]
	if !ok || state == nil {
		if jsonOutput {
			return printJSON(map[string]interface{}{
				"environment": envName,
				"build":       buildName,
				"state":       nil,
			})
		}
		fmt.Printf("environment %q has no recorded state\n", envName)
		return nil
	}

	bs := state.Builds[buildName]
	if bs == nil {
		if jsonOutput {
			return printJSON(map[string]interface{}{
				"environment": envName,
				"build":       buildName,
				"state":       nil,
			})
		}
		fmt.Printf("build %q not found in environment %q\n", buildName, envName)
		return nil
	}

	if jsonOutput {
		type buildJSON struct {
			Environment string             `json:"environment"`
			Build       string             `json:"build"`
			State       *config.BuildState `json:"state"`
		}
		return printJSON(buildJSON{Environment: envName, Build: buildName, State: bs})
	}

	fmt.Printf("environment: %s  build: %s\n", envName, buildName)
	fmt.Printf("  sha:         %s\n", orDash(bs.SHA))
	fmt.Printf("  artifact_id: %s\n", orDash(bs.ArtifactID))
	fmt.Printf("  built_at:    %s\n", orDash(bs.BuiltAt))
	fmt.Printf("  built_by:    %s\n", orDash(bs.BuiltBy))
	if len(bs.Tags) > 0 {
		fmt.Printf("  tags:\n")
		for k, v := range bs.Tags {
			fmt.Printf("    %s: %s\n", k, v)
		}
	}
	return nil
}

// -------- "status deploy <name> --env <env>" --------

func newDeployCommand(configPath, key *string, jsonOutput *bool) *cobra.Command {
	var envName string

	cmd := &cobra.Command{
		Use:   "deploy <name>",
		Short: "Show deploy state within an environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(*configPath, *key, *jsonOutput, envName, args[0])
		},
	}

	cmd.Flags().StringVar(&envName, "env", "", "Environment name (required)")
	_ = cmd.MarkFlagRequired("env")

	return cmd
}

func runDeploy(configPath, key string, jsonOutput bool, envName, deployName string) error {
	file, err := loadManifest(configPath, key)
	if err != nil {
		return err
	}

	state, ok := file.State[envName]
	if !ok || state == nil {
		if jsonOutput {
			return printJSON(map[string]interface{}{
				"environment": envName,
				"deploy":      deployName,
				"state":       nil,
			})
		}
		fmt.Printf("environment %q has no recorded state\n", envName)
		return nil
	}

	ds := state.Deploys[deployName]
	if ds == nil {
		if jsonOutput {
			return printJSON(map[string]interface{}{
				"environment": envName,
				"deploy":      deployName,
				"state":       nil,
			})
		}
		fmt.Printf("deploy %q not found in environment %q\n", deployName, envName)
		return nil
	}

	if jsonOutput {
		type deployJSON struct {
			Environment string              `json:"environment"`
			Deploy      string              `json:"deploy"`
			State       *config.DeployState `json:"state"`
		}
		return printJSON(deployJSON{Environment: envName, Deploy: deployName, State: ds})
	}

	fmt.Printf("environment: %s  deploy: %s\n", envName, deployName)
	fmt.Printf("  sha:         %s\n", orDash(ds.SHA))
	fmt.Printf("  deployed_at: %s\n", orDash(ds.DeployedAt))
	fmt.Printf("  deployed_by: %s\n", orDash(ds.DeployedBy))
	return nil
}

// -------- helpers --------

func loadManifest(configPath, key string) (*config.CICDFile, error) {
	if configPath == "" {
		configPath = config.FindConfigFile("")
	}
	file, err := config.ParseManifestFile(configPath, key)
	if err != nil {
		return nil, fmt.Errorf("loading manifest: %w", err)
	}
	return file, nil
}

func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sortedKeys(m map[string]*config.EnvState) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
