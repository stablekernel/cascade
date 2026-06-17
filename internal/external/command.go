// Package external provides CLI commands for handling external repo notifications.
package external

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/globals"
	"github.com/stablekernel/cascade/internal/log"
)

// Flags
var (
	configPath  string
	manifestKey string
	ghaOutput   bool
)

// NewCommand creates the external command with subcommands.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "external",
		Short: "Handle external repository operations",
		Long: `Handle external repository operations for multi-repo orchestration.

This command provides subcommands for managing external repository state:

  update    - Update state when a satellite repo notifies after deployment

Examples:
  # Update external state from satellite notification
  cascade external update --source-repo org/cdk-infra --deploy-name cdk \
    --environment dev --sha abc123 --version v1.2.0`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Auto-detect config file if not specified
			if configPath == "" {
				configPath = config.FindConfigFile("")
			}
			return nil
		},
	}

	// Add persistent flags
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "Path to CI/CD config file")
	cmd.PersistentFlags().StringVar(&manifestKey, "manifest-key", config.DefaultManifestKey, "Key in manifest file")
	cmd.PersistentFlags().BoolVar(&ghaOutput, "gha-output", false, "Write outputs to $GITHUB_OUTPUT")

	// Add subcommands
	cmd.AddCommand(newUpdateCommand())

	return cmd
}

// newUpdateCommand creates the 'external update' subcommand.
func newUpdateCommand() *cobra.Command {
	var (
		sourceRepo  string
		deployName  string
		environment string
		sha         string
		version     string
		artifacts   string // JSON string
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update state when satellite repo deploys",
		Long: `Update the primary repo's state when a satellite repo notifies after deployment.

This command is called by the external-update workflow when a satellite repo
dispatches a notification after deploying to dev.

The command:
  1. Reads the primary repo's manifest
  2. Updates the external deploy state for the specified environment
  3. Commits and pushes the state change (unless --dry-run)

Example:
  cascade external update --source-repo org/cdk-infra --deploy-name cdk \
    --environment dev --sha abc123 --version v1.2.0 \
    --artifacts '{"image_tag": "cdk-abc123"}'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(sourceRepo, deployName, environment, sha, version, artifacts)
		},
	}

	cmd.Flags().StringVar(&sourceRepo, "source-repo", "", "Source repository (e.g., org/cdk-infra)")
	cmd.Flags().StringVar(&deployName, "deploy-name", "", "Deploy name (e.g., cdk)")
	cmd.Flags().StringVar(&environment, "environment", "", "Target environment (e.g., dev)")
	cmd.Flags().StringVar(&sha, "sha", "", "Commit SHA from source repo")
	cmd.Flags().StringVar(&version, "version", "", "Version from source repo (optional)")
	cmd.Flags().StringVar(&artifacts, "artifacts", "", "Artifacts JSON (optional)")

	_ = cmd.MarkFlagRequired("source-repo")
	_ = cmd.MarkFlagRequired("deploy-name")
	_ = cmd.MarkFlagRequired("environment")
	_ = cmd.MarkFlagRequired("sha")

	return cmd
}

// runUpdate executes the external update.
func runUpdate(sourceRepo, deployName, environment, sha, version, artifactsJSON string) error {
	log.Section("External Update")
	log.Info("Source repo: %s", sourceRepo)
	log.Info("Deploy: %s", deployName)
	log.Info("Environment: %s", environment)
	log.Info("SHA: %s", sha)
	if version != "" {
		log.Info("Version: %s", version)
	}

	if globals.DryRun() {
		log.Info("%sRunning in dry-run mode", log.DryRunPrefix())
	}

	// Parse manifest for validation. Validation runs once, before the retry loop.
	manifest, err := config.ParseManifestFile(configPath, manifestKey)
	if err != nil {
		return fmt.Errorf("parsing manifest: %w", err)
	}

	// Validate this is a primary repo
	if !manifest.Config.IsPrimary() {
		return fmt.Errorf("this repo is not configured as a primary (no external repos defined)")
	}

	// Validate the external deploy exists
	extDeploy, extRepo := manifest.Config.GetExternalDeploy(deployName)
	if extDeploy == nil {
		return fmt.Errorf("external deploy '%s' not found in config", deployName)
	}

	// Validate source repo matches
	if extRepo.Repo != sourceRepo {
		return fmt.Errorf("source repo mismatch: expected '%s', got '%s'", extRepo.Repo, sourceRepo)
	}

	// Validate environment
	if manifest.State[environment] == nil {
		return fmt.Errorf("environment '%s' not found in state", environment)
	}

	// Parse artifacts once, before the retry loop.
	var artifacts map[string]string
	if artifactsJSON != "" {
		if err := json.Unmarshal([]byte(artifactsJSON), &artifacts); err != nil {
			return fmt.Errorf("parsing artifacts JSON: %w", err)
		}
	}

	// Capture a single timestamp and actor so retries do not drift them.
	now := time.Now().UTC().Format(time.RFC3339)
	actor := os.Getenv("GITHUB_ACTOR")
	if actor == "" {
		actor = "unknown"
	}

	// applyUpdate re-reads the manifest from disk and applies the external-state
	// mutation, then writes it back. It re-reads on every call so that after a
	// fetch-and-reset the mutation merges onto the freshly-fetched remote state
	// rather than onto a stale in-memory copy.
	applyUpdate := func() error {
		m, err := config.ParseManifestFile(configPath, manifestKey)
		if err != nil {
			return fmt.Errorf("parsing manifest: %w", err)
		}

		envState := m.State[environment]
		if envState == nil {
			return fmt.Errorf("environment '%s' not found in state", environment)
		}

		if envState.External == nil {
			envState.External = make(map[string]*config.ExternalDeployState)
		}

		envState.External[deployName] = &config.ExternalDeployState{
			Repo:       sourceRepo,
			SHA:        sha,
			Version:    version,
			DeployedAt: now,
			DeployedBy: actor,
			Artifacts:  artifacts,
		}

		if err := writeManifest(configPath, manifestKey, m); err != nil {
			return fmt.Errorf("writing manifest: %w", err)
		}
		return nil
	}

	log.Info("Updated external state for %s in %s", deployName, environment)

	if globals.DryRun() {
		log.Info("%sWould commit and push state changes", log.DryRunPrefix())
		return nil
	}

	// Commit and push with application-level retry. Concurrent external updates
	// (multiple upstream artifacts notifying the same primary repo) each hold a
	// checkout whose parent may be stale by push time. Both writes land in the
	// same YAML region, so a git-level rebase hits a textual conflict it cannot
	// resolve. commitWithApplicationRetry instead fetches the remote tip, resets
	// onto it, and re-applies the mutation, so each artifact's slot is preserved.
	commitMsg := fmt.Sprintf("chore: update %s external state from %s [skip ci]", environment, sourceRepo)
	if err := commitWithApplicationRetry(configPath, commitMsg, 5, applyUpdate); err != nil {
		return fmt.Errorf("committing state: %w", err)
	}

	log.Info("State committed and pushed")
	return nil
}

// commitWithApplicationRetry writes the manifest by calling applyUpdate(), commits
// filePath with message, and pushes. If the push is rejected non-fast-forward,
// it fetches the remote tip, resets the working tree to it, and re-applies the
// mutation before retrying. It retries up to maxAttempts times (starting at 1).
// applyUpdate must re-read the manifest from disk on each call so it merges onto
// the freshly-fetched remote state.
func commitWithApplicationRetry(filePath, commitMsg string, maxAttempts int, applyUpdate func() error) error {
	var lastPushErr error
	var lastPushOut []byte

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := applyUpdate(); err != nil {
			return err
		}

		if out, err := exec.Command("git", "add", filePath).CombinedOutput(); err != nil {
			return fmt.Errorf("git add failed: %s: %w", string(out), err)
		}

		out, err := exec.Command("git", "commit", "-m", commitMsg).CombinedOutput()
		if err != nil {
			if strings.Contains(string(out), "nothing to commit") {
				// The on-disk state already matches: re-applying produced no diff.
				return nil
			}
			return fmt.Errorf("git commit failed: %s: %w", string(out), err)
		}

		pushOut, pushErr := exec.Command("git", "push").CombinedOutput()
		if pushErr == nil {
			return nil
		}
		lastPushErr = pushErr
		lastPushOut = pushOut

		// The push did not succeed (typically a non-fast-forward rejection because
		// a competing update advanced the remote). Recover by resetting onto the
		// freshly-fetched remote tip so the next attempt re-applies the mutation on
		// top of it. A genuinely unrecoverable push error surfaces on the next
		// fetch/reset, which return wrapped.
		branch, err := git.CurrentBranch()
		if err != nil {
			return fmt.Errorf("determining current branch: %w", err)
		}

		if out, err := exec.Command("git", "fetch", "origin", branch).CombinedOutput(); err != nil {
			return fmt.Errorf("git fetch origin %s failed: %s: %w", branch, string(out), err)
		}

		if out, err := exec.Command("git", "reset", "--hard", "origin/"+branch).CombinedOutput(); err != nil {
			return fmt.Errorf("git reset --hard origin/%s failed: %s: %w", branch, string(out), err)
		}

		time.Sleep(time.Duration(attempt) * time.Second)
	}

	return fmt.Errorf("git push failed after %d attempts: %s: %w", maxAttempts, strings.TrimSpace(string(lastPushOut)), lastPushErr)
}

// writeManifest writes the CICD file back to the manifest under the specified key.
func writeManifest(path, key string, file *config.CICDFile) error {
	// Read existing manifest to preserve other keys
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}

	var manifest map[string]any
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parsing manifest: %w", err)
	}

	// Update the CI key
	manifest[key] = file

	// Write back
	out, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}

	if err := os.WriteFile(path, out, 0644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}

	return nil
}
