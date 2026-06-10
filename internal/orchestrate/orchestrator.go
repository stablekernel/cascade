package orchestrate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/changelog"
	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/globals"
	"github.com/stablekernel/cascade/internal/log"
	"github.com/stablekernel/cascade/internal/output"
	"github.com/stablekernel/cascade/internal/version"
)

// Orchestrator handles CI/CD orchestration logic.
type Orchestrator struct {
	configPath  string
	environment string
	cicdFile    *config.CICDFile
	baseDir     string
}

// DefaultStateKey is used for state tracking when no environments are configured.
const DefaultStateKey = "prerelease"

// NewOrchestrator creates a new Orchestrator.
func NewOrchestrator(configPath, manifestKey, environment string) (*Orchestrator, error) {
	log.Debug("Loading config from %s (key: %s)", configPath, manifestKey)

	cicdFile, err := config.ParseManifestFile(configPath, manifestKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if cicdFile.Config == nil {
		return nil, fmt.Errorf("config section not found in manifest (key: %s)", manifestKey)
	}

	baseDir := filepath.Dir(filepath.Dir(configPath)) // Go up from .github/manifest.yaml

	// For no-environment setups, use default state key
	if environment == "" && len(cicdFile.Config.Environments) == 0 {
		environment = DefaultStateKey
		log.Debug("No environment specified and no environments configured - using state key: %s", environment)
	}

	log.Debug("Environments: %v", cicdFile.Config.Environments)
	log.Debug("Base directory: %s", baseDir)

	return &Orchestrator{
		configPath:  configPath,
		environment: environment,
		cicdFile:    cicdFile,
		baseDir:     baseDir,
	}, nil
}

// Setup runs the setup phase and returns the result.
func (o *Orchestrator) Setup(headSHA string) (*output.SetupResult, error) {
	log.Section("Setup Phase")

	// Determine head SHA
	if headSHA == "" {
		sha, err := o.gitOutput("rev-parse", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("failed to get HEAD SHA: %w", err)
		}
		headSHA = sha
	}
	log.Info("Head SHA: %s", headSHA)

	// Get environment state
	envState := o.cicdFile.State[o.environment]
	log.Debug("Current %s state: %+v", o.environment, envState)

	// Calculate base SHAs for change detection
	baseSHAs := o.calculateBaseSHAs(envState)
	log.Debug("Base SHAs: %v", baseSHAs)

	// Detect which builds need to run
	runBuilds := make(map[string]bool)
	for _, build := range o.cicdFile.Config.Builds {
		baseSHA := baseSHAs["build_"+build.Name]
		needsRun := o.detectChanges(baseSHA, headSHA, build.Triggers)
		runBuilds[build.Name] = needsRun
		log.Debug("Build %s: needs_run=%v (base=%s)", build.Name, needsRun, truncateSHA(baseSHA))
	}

	// Detect which deploys need to run
	runDeploys := make(map[string]string)
	for _, deploy := range o.cicdFile.Config.Deploys {
		if len(deploy.DependsOn) > 0 {
			// Deploy depends on builds - mark as pending
			runDeploys[deploy.Name] = "pending"
			log.Debug("Deploy %s: pending (depends on builds)", deploy.Name)
		} else {
			baseSHA := baseSHAs["deploy_"+deploy.Name]
			needsRun := o.detectChanges(baseSHA, headSHA, deploy.Triggers)
			if needsRun {
				runDeploys[deploy.Name] = "true"
			} else {
				runDeploys[deploy.Name] = "false"
			}
			log.Debug("Deploy %s: %s (base=%s)", deploy.Name, runDeploys[deploy.Name], truncateSHA(baseSHA))
		}
	}

	// Calculate version
	version, err := o.calculateVersion()
	if err != nil {
		return nil, fmt.Errorf("failed to calculate version: %w", err)
	}
	log.Info("Calculated version: %s", version)

	// Determine changelog base SHA and previous tag
	changelogBaseSHA, previousTag := o.calculateChangelogRefs()
	log.Debug("Changelog base SHA: %s", truncateSHA(changelogBaseSHA))
	log.Debug("Previous tag: %s", previousTag)

	return &output.SetupResult{
		HeadSHA:          headSHA,
		Version:          version,
		PreviousTag:      previousTag,
		ChangelogBaseSHA: changelogBaseSHA,
		RunBuilds:        runBuilds,
		RunDeploys:       runDeploys,
		BaseSHAs:         baseSHAs,
	}, nil
}

// Finalize runs the finalize phase.
func (o *Orchestrator) Finalize(headSHA, version string, deployResults, buildResults map[string]string) error {
	log.Section("Finalize Phase")

	if globals.DryRun() {
		log.Info("%sWould update state for successful deploys", log.DryRunPrefix())
		for name, result := range deployResults {
			log.Debug("  %s: %s", name, result)
		}
		return nil
	}

	// Update manifest state for successful deploys
	timestamp := time.Now().UTC().Format(time.RFC3339)
	actor := os.Getenv("GITHUB_ACTOR")
	if actor == "" {
		actor = "github-actions[bot]"
	}

	// Ensure state exists for environment
	if o.cicdFile.State == nil {
		o.cicdFile.State = make(map[string]*config.EnvState)
	}
	if o.cicdFile.State[o.environment] == nil {
		o.cicdFile.State[o.environment] = &config.EnvState{}
	}
	envState := o.cicdFile.State[o.environment]

	// Update environment-level state (committed)
	envState.SHA = headSHA
	envState.Version = version
	envState.CommittedAt = timestamp
	envState.CommittedBy = actor
	log.Info("Updated %s state: SHA=%s, Version=%s", o.environment, truncateSHA(headSHA), version)

	// Update per-deploy state for successful deploys
	if envState.Deploys == nil {
		envState.Deploys = make(map[string]*config.DeployState)
	}

	for name, result := range deployResults {
		if result == "success" {
			if envState.Deploys[name] == nil {
				envState.Deploys[name] = &config.DeployState{}
			}
			envState.Deploys[name].SHA = headSHA
			envState.Deploys[name].DeployedAt = timestamp
			envState.Deploys[name].DeployedBy = actor
			log.Info("Updated %s.deploys.%s state", o.environment, name)
		} else {
			log.Debug("Skipping %s deploy state update (result=%s)", name, result)
		}
	}

	// Write updated config
	if err := o.writeConfig(); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Commit and push if changes were made
	if err := o.commitAndPush(version); err != nil {
		return fmt.Errorf("failed to commit/push: %w", err)
	}

	return nil
}

// calculateBaseSHAs returns base SHAs for each build/deploy from current state.
func (o *Orchestrator) calculateBaseSHAs(envState *config.EnvState) map[string]string {
	baseSHAs := make(map[string]string)

	// Default base SHA
	defaultBase, _ := o.gitOutput("rev-parse", "HEAD~1")
	if defaultBase == "" {
		// Fallback to initial commit
		defaultBase, _ = o.gitOutput("rev-list", "--max-parents=0", "HEAD")
	}

	// Set base SHAs from per-deployable state
	for _, build := range o.cicdFile.Config.Builds {
		key := "build_" + build.Name
		if envState != nil && envState.Deploys != nil {
			// For builds, use the deploy state of the dependent deploy
			for _, deploy := range o.cicdFile.Config.Deploys {
				if contains(deploy.DependsOn, build.Name) {
					if ds := envState.Deploys[deploy.Name]; ds != nil && ds.SHA != "" {
						baseSHAs[key] = ds.SHA
						break
					}
				}
			}
		}
		if baseSHAs[key] == "" {
			baseSHAs[key] = defaultBase
		}
	}

	for _, deploy := range o.cicdFile.Config.Deploys {
		key := "deploy_" + deploy.Name
		if envState != nil && envState.Deploys != nil {
			if ds := envState.Deploys[deploy.Name]; ds != nil && ds.SHA != "" {
				baseSHAs[key] = ds.SHA
			}
		}
		if baseSHAs[key] == "" {
			baseSHAs[key] = defaultBase
		}
	}

	return baseSHAs
}

// detectChanges checks if any files matching triggers changed between base and head.
func (o *Orchestrator) detectChanges(baseSHA, headSHA string, triggers []string) bool {
	if baseSHA == "" || len(triggers) == 0 {
		log.Trace("No base SHA or no triggers - assuming changes needed")
		return true
	}

	// Get list of changed files
	changedFiles, err := o.gitOutput("diff", "--name-only", baseSHA, headSHA)
	if err != nil {
		log.Warn("Failed to detect changes: %v - assuming changes needed", err)
		return true
	}

	files := strings.Split(changedFiles, "\n")
	log.Trace("Changed files: %v", files)

	// Check if any changed file matches triggers
	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		for _, trigger := range triggers {
			if matchGlob(file, trigger) {
				log.Trace("File %s matches trigger %s", file, trigger)
				return true
			}
		}
	}

	return false
}

// calculateVersion calculates the next version for the environment.
func (o *Orchestrator) calculateVersion() (string, error) {
	envs := o.cicdFile.Config.Environments

	// Get current environment's version and next env's version
	var currentDevVersion, nextEnvVersion, nextEnvSHA string

	// For no-environment setup (library/CLI projects), use the provided environment key
	// for state tracking but don't require it to be in the environments list
	if len(envs) == 0 {
		// No environments - this is a library/CLI project
		// All builds go to pre-release, version bumps based on conventional commits
		tagPrefix := o.cicdFile.Config.GetTagPrefix()

		// Get current dev version from state (for RC number tracking)
		if state := o.cicdFile.State[o.environment]; state != nil {
			currentDevVersion = state.Version
		}

		// If no state, check for latest RC tag
		if currentDevVersion == "" {
			latestTag, _, err := git.GetLatestTag(tagPrefix)
			if err != nil {
				log.Warn("Failed to get latest tag: %v", err)
			} else if latestTag != "" {
				currentDevVersion = latestTag
				log.Debug("No state found, using latest git tag: %s", latestTag)
			}
		}

		// Get latest published release (non-RC) as base version for version calculation
		// This ensures we continue from v1.0.0 → v1.0.1-rc.0, not restart at v0.1.0-rc.0
		latestRelease, releaseSHA, err := git.GetLatestReleaseTag(tagPrefix)
		if err != nil {
			log.Warn("Failed to get latest release tag: %v", err)
		} else if latestRelease != "" {
			nextEnvVersion = latestRelease
			nextEnvSHA = releaseSHA
			log.Debug("Using latest published release as base: %s (SHA: %s)", latestRelease, truncateSHA(releaseSHA))
		}

		log.Debug("No-environment setup: current version = %s, base version = %s", currentDevVersion, nextEnvVersion)
	} else {
		// Standard setup with environments
		envIndex := indexOf(envs, o.environment)
		if envIndex < 0 {
			return "", fmt.Errorf("environment %q not found", o.environment)
		}

		if state := o.cicdFile.State[o.environment]; state != nil {
			currentDevVersion = state.Version
		}

		// Next environment's version (for comparison)
		if envIndex+1 < len(envs) {
			nextEnv := envs[envIndex+1]
			if state := o.cicdFile.State[nextEnv]; state != nil {
				nextEnvVersion = state.Version
				nextEnvSHA = state.SHA
			}
		}
	}

	log.Debug("Current %s version: %s", o.environment, currentDevVersion)
	log.Debug("Next env version: %s (SHA: %s)", nextEnvVersion, truncateSHA(nextEnvSHA))

	// Get commits between base and head
	baseSHA := nextEnvSHA
	if baseSHA == "" {
		baseSHA, _ = git.GetInitialCommit()
	}

	var commits []changelog.ConventionalCommit
	if baseSHA != "" {
		gitCommits, err := git.GetCommits(baseSHA, "HEAD", nil)
		if err != nil {
			log.Warn("Failed to get commits: %v", err)
		} else {
			for _, gc := range gitCommits {
				if cc := changelog.ParseCommit(gc); cc != nil {
					commits = append(commits, *cc)
				}
			}
		}
	}

	log.Debug("Found %d conventional commits for version calculation", len(commits))

	// Calculate next version
	calc := version.NewCalculator(o.cicdFile.Config.GetTagPrefix())
	nextVersion, err := calc.CalculateNext(currentDevVersion, nextEnvVersion, commits)
	if err != nil {
		return "", fmt.Errorf("calculating version: %w", err)
	}

	return nextVersion.String(), nil
}

// calculateChangelogRefs returns the changelog base SHA and previous tag,
// in priority order:
//
//  1. Multi-env intermediate: next env's state SHA (e.g., dev's changelog
//     is "what's new vs test"). Preserves the existing per-env progression
//     model. The changelog shows what's about to be promoted forward.
//  2. Last published release: state["release"].SHA, falling back to
//     latest_release.SHA. This is the right base whenever there's no
//     "next env" to compare against (i.e., no-env library/CLI projects
//     after their first publish, OR the terminal env in a multi-env list).
//     Without this, a freshly-published-then-orchestrated repo would
//     compute a changelog covering its entire git history (cf. #80).
//  3. Initial commit: only when nothing has been released yet (truly the
//     first release). The changelog is then the full repo introduction.
func (o *Orchestrator) calculateChangelogRefs() (string, string) {
	envs := o.cicdFile.Config.Environments
	envIndex := indexOf(envs, o.environment)

	// 1. Intermediate env: compare against the next env's state.
	if envIndex >= 0 && envIndex < len(envs)-1 {
		nextEnv := envs[envIndex+1]
		if nextState := o.cicdFile.State[nextEnv]; nextState != nil && nextState.SHA != "" {
			return nextState.SHA, nextState.Version
		}
	}

	// 2. Terminal env or no-env: compare against the last published release.
	if releaseState := o.cicdFile.State["release"]; releaseState != nil && releaseState.SHA != "" {
		return releaseState.SHA, releaseState.Version
	}
	if o.cicdFile.LatestRelease != nil && o.cicdFile.LatestRelease.SHA != "" {
		return o.cicdFile.LatestRelease.SHA, o.cicdFile.LatestRelease.Version
	}

	// 3. Nothing released yet: fall back to the repo's initial commit.
	initialCommit, _ := o.gitOutput("rev-list", "--max-parents=0", "HEAD")
	return initialCommit, ""
}

// writeConfig writes the updated cicd.yaml file.
func (o *Orchestrator) writeConfig() error {
	data, err := yaml.Marshal(o.cicdFile)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(o.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	log.Debug("Wrote updated config to %s", o.configPath)
	return nil
}

// commitAndPush commits and pushes state changes.
func (o *Orchestrator) commitAndPush(version string) error {
	// Check if there are changes to commit
	status, _ := o.gitOutput("status", "--porcelain", o.configPath)
	if strings.TrimSpace(status) == "" {
		log.Debug("No changes to commit")
		return nil
	}

	// Configure git
	if err := o.gitRun("config", "user.name", "github-actions[bot]"); err != nil {
		return err
	}
	if err := o.gitRun("config", "user.email", "github-actions[bot]@users.noreply.github.com"); err != nil {
		return err
	}

	// Add and commit
	if err := o.gitRun("add", o.configPath); err != nil {
		return err
	}

	message := fmt.Sprintf("chore: update state for %s [skip ci]", o.environment)
	if err := o.gitRun("commit", "-m", message); err != nil {
		return err
	}

	// Push
	if err := o.gitRun("push"); err != nil {
		return err
	}

	log.Info("Committed and pushed state changes")
	return nil
}

// gitOutput runs a git command and returns stdout.
func (o *Orchestrator) gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = o.baseDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRun runs a git command.
func (o *Orchestrator) gitRun(args ...string) error {
	log.Trace("git %s", strings.Join(args, " "))
	cmd := exec.Command("git", args...)
	cmd.Dir = o.baseDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Helper functions

func truncateSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func indexOf(slice []string, item string) int {
	for i, s := range slice {
		if s == item {
			return i
		}
	}
	return -1
}

// matchGlob performs simple glob matching (supports * and **).
func matchGlob(path, pattern string) bool {
	// Simple implementation - handle common cases
	if pattern == "" {
		return false
	}

	// Handle ** (recursive match)
	if strings.Contains(pattern, "**") {
		parts := strings.Split(pattern, "**")
		if len(parts) == 2 {
			prefix := strings.TrimSuffix(parts[0], "/")
			suffix := strings.TrimPrefix(parts[1], "/")
			if prefix != "" && !strings.HasPrefix(path, prefix) {
				return false
			}
			if suffix != "" && !strings.HasSuffix(path, suffix) {
				return false
			}
			return true
		}
	}

	// Handle single * (match any characters except /)
	if strings.Contains(pattern, "*") {
		parts := strings.Split(pattern, "*")
		pos := 0
		for _, part := range parts {
			if part == "" {
				continue
			}
			idx := strings.Index(path[pos:], part)
			if idx < 0 {
				return false
			}
			pos += idx + len(part)
		}
		return true
	}

	// Exact match
	return path == pattern
}
