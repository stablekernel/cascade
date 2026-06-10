// Package reset provides functionality to reset test repositories by wiping
// releases, tags, and optionally resetting state in the manifest.
// Supports auto-detection of manifest.yaml/manifest.yml with configurable key.
package reset

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/log"
)

// Options configures the reset operation.
type Options struct {
	RepoPath    string // Path to the repository (defaults to current directory)
	ResetState  bool   // Whether to reset state in manifest
	DryRun      bool   // Preview changes without executing
	Push        bool   // Push changes after reset (only applies if ResetState is true)
	ConfigPath  string // Path to manifest file (auto-detected if empty)
	ManifestKey string // Key in manifest file (default: "ci")
}

// Result contains the results of the reset operation.
type Result struct {
	ReleasesDeleted int
	TagsDeleted     int
	StateReset      bool
	Pushed          bool
}

// Resetter handles repository reset operations.
type Resetter struct {
	opts        Options
	repoPath    string
	repoOwner   string
	repoName    string
	configPath  string
	manifestKey string
	cicdFile    *config.CICDFile
}

// New creates a new Resetter.
func New(opts Options) (*Resetter, error) {
	repoPath := opts.RepoPath
	if repoPath == "" {
		var err error
		repoPath, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	// Verify it's a git repository
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); os.IsNotExist(err) {
		return nil, fmt.Errorf("%s is not a git repository", repoPath)
	}

	// Get repo owner and name from remote
	owner, name, err := getRepoInfo(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get repo info: %w", err)
	}

	// Determine config path
	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = config.FindConfigFile(repoPath)
	}

	// Determine manifest key
	manifestKey := opts.ManifestKey
	if manifestKey == "" {
		manifestKey = config.DefaultManifestKey
	}

	// Load config if we need to reset state
	var cicdFile *config.CICDFile
	if opts.ResetState {
		cicdFile, err = config.ParseManifestFile(configPath, manifestKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse manifest: %w", err)
		}
	}

	return &Resetter{
		opts:        opts,
		repoPath:    repoPath,
		repoOwner:   owner,
		repoName:    name,
		configPath:  configPath,
		manifestKey: manifestKey,
		cicdFile:    cicdFile,
	}, nil
}

// Reset performs the reset operation.
func (r *Resetter) Reset() (*Result, error) {
	result := &Result{}

	log.Section("Reset Repository")
	log.Info("Repository: %s/%s", r.repoOwner, r.repoName)
	log.Info("Path: %s", r.repoPath)

	// Delete all releases
	deletedReleases, err := r.deleteAllReleases()
	if err != nil {
		return result, fmt.Errorf("failed to delete releases: %w", err)
	}
	result.ReleasesDeleted = deletedReleases

	// Delete all tags
	deletedTags, err := r.deleteAllTags()
	if err != nil {
		return result, fmt.Errorf("failed to delete tags: %w", err)
	}
	result.TagsDeleted = deletedTags

	// Reset state if requested
	if r.opts.ResetState {
		if err := r.resetState(); err != nil {
			return result, fmt.Errorf("failed to reset state: %w", err)
		}
		result.StateReset = true

		if r.opts.Push && !r.opts.DryRun {
			if err := r.commitAndPush(); err != nil {
				return result, fmt.Errorf("failed to push changes: %w", err)
			}
			result.Pushed = true
		}
	}

	return result, nil
}

// deleteAllReleases deletes all GitHub releases.
func (r *Resetter) deleteAllReleases() (int, error) {
	log.Debug("Fetching releases...")

	// Get list of releases
	releases, err := r.ghOutput("release", "list", "--json", "tagName", "-q", ".[].tagName")
	if err != nil {
		return 0, err
	}

	if releases == "" {
		log.Info("No releases to delete")
		return 0, nil
	}

	tags := strings.Split(strings.TrimSpace(releases), "\n")
	log.Info("Found %d releases to delete", len(tags))

	if r.opts.DryRun {
		for _, tag := range tags {
			log.Info("[DRY-RUN] Would delete release: %s", tag)
		}
		return len(tags), nil
	}

	deleted := 0
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		log.Debug("Deleting release: %s", tag)
		if err := r.ghRun("release", "delete", tag, "--yes"); err != nil {
			log.Warn("Failed to delete release %s: %v", tag, err)
			continue
		}
		deleted++
	}

	log.Info("Deleted %d releases", deleted)
	return deleted, nil
}

// deleteAllTags deletes all git tags (local and remote).
func (r *Resetter) deleteAllTags() (int, error) {
	log.Debug("Fetching tags...")

	// Fetch all tags from remote first
	if err := r.gitRun("fetch", "--tags"); err != nil {
		log.Warn("Failed to fetch tags: %v", err)
	}

	// Get list of tags
	tagsOutput, err := r.gitOutput("tag", "-l")
	if err != nil {
		return 0, err
	}

	if tagsOutput == "" {
		log.Info("No tags to delete")
		return 0, nil
	}

	tags := strings.Split(strings.TrimSpace(tagsOutput), "\n")
	log.Info("Found %d tags to delete", len(tags))

	if r.opts.DryRun {
		for _, tag := range tags {
			log.Info("[DRY-RUN] Would delete tag: %s", tag)
		}
		return len(tags), nil
	}

	// Delete remote tags
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		log.Debug("Deleting remote tag: %s", tag)
		if err := r.gitRun("push", "origin", "--delete", tag); err != nil {
			log.Warn("Failed to delete remote tag %s: %v", tag, err)
		}
	}

	// Delete local tags
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		log.Debug("Deleting local tag: %s", tag)
		if err := r.gitRun("tag", "-d", tag); err != nil {
			log.Warn("Failed to delete local tag %s: %v", tag, err)
		}
	}

	log.Info("Deleted %d tags", len(tags))
	return len(tags), nil
}

// resetState clears the state section in the manifest.
func (r *Resetter) resetState() error {
	if r.cicdFile == nil {
		return fmt.Errorf("config not loaded")
	}

	if len(r.cicdFile.State) == 0 && r.cicdFile.LatestRelease == nil {
		log.Info("State is already empty")
		return nil
	}

	envCount := len(r.cicdFile.State)
	log.Info("Resetting state for %d environments", envCount)

	if r.opts.DryRun {
		for env := range r.cicdFile.State {
			log.Info("[DRY-RUN] Would reset state for: %s", env)
		}
		return nil
	}

	// Clear state
	r.cicdFile.State = nil
	r.cicdFile.LatestRelease = nil

	// Write back with manifest key wrapper
	if err := r.writeConfig(); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	log.Info("Reset state in manifest")
	return nil
}

// writeConfig writes the updated manifest back to disk.
func (r *Resetter) writeConfig() error {
	// Wrap the CICDFile in the manifest key
	wrapper := map[string]interface{}{
		r.manifestKey: r.cicdFile,
	}

	data, err := yaml.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(r.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	log.Debug("Wrote updated config to %s", r.configPath)
	return nil
}

// commitAndPush commits and pushes the state reset.
func (r *Resetter) commitAndPush() error {
	// Get relative path for git add
	configPath := r.configPath
	if filepath.IsAbs(configPath) {
		rel, err := filepath.Rel(r.repoPath, configPath)
		if err == nil {
			configPath = rel
		}
	}

	// Check if there are changes
	status, _ := r.gitOutput("status", "--porcelain", configPath)
	if strings.TrimSpace(status) == "" {
		log.Debug("No changes to commit")
		return nil
	}

	// Configure git
	if err := r.gitRun("config", "user.name", "github-actions[bot]"); err != nil {
		return err
	}
	if err := r.gitRun("config", "user.email", "github-actions[bot]@users.noreply.github.com"); err != nil {
		return err
	}

	// Add, commit, push
	if err := r.gitRun("add", configPath); err != nil {
		return err
	}
	if err := r.gitRun("commit", "-m", "chore: reset state for testing [skip ci]"); err != nil {
		return err
	}
	if err := r.gitRun("push"); err != nil {
		return err
	}

	log.Info("Committed and pushed state reset")
	return nil
}

// Helper functions

func getRepoInfo(repoPath string) (owner, name string, err error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", "", err
	}

	url := strings.TrimSpace(string(out))
	return parseGitURL(url)
}

func parseGitURL(url string) (owner, name string, err error) {
	// Handle SSH format: git@github.com:owner/repo.git
	if strings.HasPrefix(url, "git@") {
		url = strings.TrimPrefix(url, "git@github.com:")
		url = strings.TrimSuffix(url, ".git")
		parts := strings.Split(url, "/")
		if len(parts) != 2 {
			return "", "", fmt.Errorf("invalid git URL format: %s", url)
		}
		return parts[0], parts[1], nil
	}

	// Handle HTTPS format: https://github.com/owner/repo.git
	if strings.HasPrefix(url, "https://") {
		url = strings.TrimPrefix(url, "https://github.com/")
		url = strings.TrimSuffix(url, ".git")
		parts := strings.Split(url, "/")
		if len(parts) != 2 {
			return "", "", fmt.Errorf("invalid git URL format: %s", url)
		}
		return parts[0], parts[1], nil
	}

	return "", "", fmt.Errorf("unsupported git URL format: %s", url)
}

func (r *Resetter) gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *Resetter) gitRun(args ...string) error {
	log.Trace("git %s", strings.Join(args, " "))
	cmd := exec.Command("git", args...)
	cmd.Dir = r.repoPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (r *Resetter) ghOutput(args ...string) (string, error) {
	allArgs := append([]string{"-R", r.repoOwner + "/" + r.repoName}, args...)
	cmd := exec.Command("gh", allArgs...)
	cmd.Dir = r.repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *Resetter) ghRun(args ...string) error {
	allArgs := append([]string{"-R", r.repoOwner + "/" + r.repoName}, args...)
	log.Trace("gh %s", strings.Join(allArgs, " "))
	cmd := exec.Command("gh", allArgs...)
	cmd.Dir = r.repoPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
