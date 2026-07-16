package orchestrate

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	// component, when non-empty, names the declared component this orchestration
	// is scoped to. It is set only via WithComponent by a per-component generated
	// workflow; an empty value selects the single-component path, byte-identical
	// to today. It scopes version derivation to the component's path and strict
	// tag namespace (calculateComponentVersion).
	component string
	// pushBackoff is the delay between state-write push retries. A zero value
	// selects the shared git package default; tests override it to keep the retry
	// loop fast. It is threaded into git.PushWithRebaseRetry via git.WithBackoff.
	pushBackoff time.Duration
}

// Option configures an Orchestrator at construction. Options are the additive
// extension point so new per-invocation scoping (such as a component selector) is
// never a breaking change to NewOrchestrator's positional signature.
type Option func(*Orchestrator)

// WithComponent scopes the orchestration to the named declared component, so its
// version is derived from that component's path-scoped commits and strict tag
// namespace rather than the whole repo. An empty name is a no-op, preserving the
// single-component path.
func WithComponent(name string) Option {
	return func(o *Orchestrator) { o.component = name }
}

// DefaultStateKey is used for state tracking when no environments are configured.
const DefaultStateKey = "prerelease"

// NewOrchestrator creates a new Orchestrator.
func NewOrchestrator(configPath, manifestKey, environment string, opts ...Option) (*Orchestrator, error) {
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

	log.Debug("Environments: %v", cicdFile.Config.EnvironmentNames())
	log.Debug("Base directory: %s", baseDir)

	o := &Orchestrator{
		configPath:  configPath,
		environment: environment,
		cicdFile:    cicdFile,
		baseDir:     baseDir,
	}
	for _, opt := range opts {
		opt(o)
	}

	// A component-scoped run persists its state under
	// state.components.<component>.<env>, so the flat state map parsed above
	// holds nothing for it. Lift the component's recorded rows into the working
	// map (the same overlay the promote and hotfix paths perform) so the Setup
	// base-SHA ladder anchors on the recorded state instead of the HEAD~1
	// first-run fallback, and Finalize mutates the recorded leaf in place
	// instead of rebuilding it from empty and dropping prior rows. The write
	// path stays component-scoped, so overlaid rows never leak back into the
	// manifest as flat state.
	if o.component != "" {
		raw, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config for component state: %w", err)
		}
		if err := config.OverlayComponentState(cicdFile, raw, manifestKey, o.component); err != nil {
			return nil, fmt.Errorf("failed to overlay component state: %w", err)
		}
	}
	return o, nil
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

	// Effective extra paths for the scoped component (its extra_paths unioned with
	// top-level shared_paths), so a change touching only a shared dependency is not
	// skipped as unchanged by a callback whose own triggers do not list it. Nil on
	// the single-component path, keeping change detection byte-identical there.
	extraPaths := o.componentExtraPaths()

	// Detect which builds need to run
	runBuilds := make(map[string]bool)
	for _, build := range o.cicdFile.Config.Builds {
		baseSHA := baseSHAs["build_"+build.Name]
		needsRun := o.detectChanges(baseSHA, headSHA, withExtraPaths(build.Triggers, extraPaths))
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
			needsRun := o.detectChanges(baseSHA, headSHA, withExtraPaths(deploy.Triggers, extraPaths))
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

	// Update per-build state for successful builds. Recording the build SHA here
	// is what lets the next dispatch's base-SHA ladder anchor change detection on
	// the last successful build, so an unchanged HEAD skips the build instead of
	// re-running it.
	if envState.Builds == nil {
		envState.Builds = make(map[string]*config.BuildState)
	}

	for name, result := range buildResults {
		if result == "success" {
			if envState.Builds[name] == nil {
				envState.Builds[name] = &config.BuildState{}
			}
			envState.Builds[name].SHA = headSHA
			envState.Builds[name].BuiltAt = timestamp
			envState.Builds[name].BuiltBy = actor
			log.Info("Updated %s.builds.%s state", o.environment, name)
		} else {
			log.Debug("Skipping %s build state update (result=%s)", name, result)
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

	// Set base SHAs from per-deployable state. The build base-SHA ladder is, in
	// priority order:
	//
	//  1. The build's own recorded state SHA (envState.Builds[name].SHA). This is
	//     the precise "last time this build ran" anchor, recorded by Finalize.
	//  2. The dependent deploy's state SHA. Preserves the original behavior for
	//     builds whose artifact is carried forward by a deploy that depends on it.
	//  3. The env-level last-orchestrated SHA (envState.SHA). This is the fallback
	//     that fixes no-environment / no-dependent-deploy builds: after the first
	//     orchestrate at HEAD, envState.SHA == HEAD, so a no-change re-dispatch
	//     diffs HEAD..HEAD (empty) and the build is skipped instead of re-running.
	//  4. defaultBase (HEAD~1, or the initial commit) as the first-run fallback.
	for _, build := range o.cicdFile.Config.Builds {
		key := "build_" + build.Name
		if envState != nil {
			// 1. The build's own recorded state SHA.
			if bs := envState.Builds[build.Name]; bs != nil && bs.SHA != "" {
				baseSHAs[key] = bs.SHA
			}
			// 2. The dependent deploy's state SHA.
			if baseSHAs[key] == "" && envState.Deploys != nil {
				for _, deploy := range o.cicdFile.Config.Deploys {
					if contains(deploy.DependsOn, build.Name) {
						if ds := envState.Deploys[deploy.Name]; ds != nil && ds.SHA != "" {
							baseSHAs[key] = ds.SHA
							break
						}
					}
				}
			}
			// 3. The env-level last-orchestrated SHA.
			if baseSHAs[key] == "" && envState.SHA != "" {
				baseSHAs[key] = envState.SHA
			}
		}
		// 4. First-run fallback.
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

	var files []string
	for _, file := range strings.Split(changedFiles, "\n") {
		if file = strings.TrimSpace(file); file != "" {
			files = append(files, file)
		}
	}
	log.Trace("Changed files: %v", files)

	// Delegate to the shared negation-aware evaluator so change detection
	// agrees with the emitted GitHub Actions paths filter, including "!"
	// exclusion patterns.
	matched := config.MatchAnyTrigger(triggers, files)
	log.Trace("Trigger match for %v: %v", triggers, matched)
	return matched
}

// componentExtraPaths returns the effective extra path set for the scoped
// component: its extra_paths unioned with the manifest's top-level shared_paths.
// It returns nil on the single-component path (o.component == ""), so change
// detection there is byte-identical. A resolution error is logged and treated as
// no extra paths rather than failing setup.
func (o *Orchestrator) componentExtraPaths() []string {
	if o.component == "" {
		return nil
	}
	resolved, err := o.cicdFile.Config.ResolveComponent(o.component)
	if err != nil {
		log.Warn("Failed to resolve component %s for change detection: %v", o.component, err)
		return nil
	}
	return resolved.ExtraPaths
}

// withExtraPaths unions a callback's own triggers with the component's effective
// extra paths so a shared-dependency change is detected by that callback. It only
// augments a non-empty trigger list: an empty triggers list already means "run on
// any change", so the shared change is covered and adding paths would wrongly
// narrow it to those paths. It returns the original slice when there is nothing to
// add, keeping the single-component and no-extra-path shapes byte-identical.
func withExtraPaths(triggers, extraPaths []string) []string {
	if len(triggers) == 0 || len(extraPaths) == 0 {
		return triggers
	}
	out := make([]string, 0, len(triggers))
	out = append(out, triggers...)
	out = append(out, extraPaths...)
	return out
}

// calculateVersion calculates the next version for the environment.
func (o *Orchestrator) calculateVersion() (string, error) {
	// A component-scoped orchestration derives its version from only its own
	// path-scoped commits and strict tag namespace, so one component advancing
	// never moves a sibling. The single-component path (component == "") is
	// untouched below.
	if o.component != "" {
		return o.calculateComponentVersion()
	}

	envs := o.cicdFile.Config.EnvironmentNames()

	// Get current environment's version and next env's version
	var currentDevVersion, nextEnvVersion, nextEnvSHA string

	// Resolve the manifest's tag grammar once so tag discovery and version
	// calculation share one shape. With no tag_grammar block this is the
	// historical default, keeping behavior byte-identical.
	spec := o.cicdFile.Config.ResolveTagGrammar()

	// For no-environment setup (library/CLI projects), use the provided environment key
	// for state tracking but don't require it to be in the environments list
	if len(envs) == 0 {
		// No environments - this is a library/CLI project
		// All builds go to pre-release, version bumps based on conventional commits
		// The resolved Spec.Prefix IS the prefix; the tag glob below widens on it
		// and the version predicate narrows, so a custom grammar stays consistent.
		tagPrefix := spec.Prefix

		// Get current dev version from state (for RC number tracking)
		if state := o.cicdFile.State[o.environment]; state != nil {
			currentDevVersion = state.Version
		}

		// If no state, check for latest RC tag. A failing lookup must surface: an
		// established project read as tagless restarts at v0.1.0-rc.0, a
		// regressive mint that can collide with already-published tags.
		if currentDevVersion == "" {
			latestTag, _, err := git.GetLatestTag(o.baseDir, tagPrefix)
			if err != nil {
				return "", fmt.Errorf("reading latest tag: %w", err)
			}
			if latestTag != "" {
				currentDevVersion = latestTag
				log.Debug("No state found, using latest git tag: %s", latestTag)
			}
		}

		// Get latest published release (non-RC) as base version for version calculation
		// This ensures we continue from v1.0.0 → v1.0.1-rc.0, not restart at v0.1.0-rc.0
		latestRelease, releaseSHA, err := git.GetLatestReleaseTagSpec(o.baseDir, spec)
		if err != nil {
			return "", fmt.Errorf("reading latest release tag: %w", err)
		}
		if latestRelease != "" {
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

	// Get commits between base and head. A failing commit enumeration (a shallow
	// clone missing the base, a bad SHA) must surface rather than read as "no
	// commits": an empty range silently under-bumps the calculated version.
	baseSHA := nextEnvSHA
	if baseSHA == "" {
		initial, err := git.GetInitialCommit()
		if err != nil {
			return "", fmt.Errorf("resolving initial commit for version range: %w", err)
		}
		baseSHA = initial
	}

	var commits []changelog.ConventionalCommit
	if baseSHA != "" {
		gitCommits, err := git.GetCommits(baseSHA, "HEAD", nil)
		if err != nil {
			return "", fmt.Errorf("reading commits for version calculation: %w", err)
		}
		for _, gc := range gitCommits {
			if cc := changelog.ParseCommit(gc); cc != nil {
				commits = append(commits, *cc)
			}
		}
	}

	log.Debug("Found %d conventional commits for version calculation", len(commits))

	// Calculate next version under the resolved tag grammar shared with discovery.
	calc := version.NewCalculatorWithGrammar(spec)
	nextVersion, err := calc.CalculateNext(currentDevVersion, nextEnvVersion, commits)
	if err != nil {
		return "", fmt.Errorf("calculating version: %w", err)
	}

	return nextVersion.String(), nil
}

// calculateComponentVersion derives the next version for the orchestration's
// component from that component's own path-scoped commits and strict tag
// namespace. The base version and its SHA come from the component's latest
// published release tag (read under the component's strict prefix), the RC counter
// base from the component's latest tag, and the commit range from
// GetCommitsForPaths scoped to the component's path plus its effective extra
// paths (its extra_paths unioned with top-level shared_paths). Both tag lookups
// scope to the component namespace, so component A's release tags and commits
// never influence component B's computed version (HLD Section 4 row 3, Section
// 5). The scope derivation lives in version.ResolveComponentVersionInputs,
// shared with `cascade next-version --component`, so the CLI preview and this
// production path agree by construction. This mirrors the no-environment
// (tag-derived) single-component path; per-component recorded-state promotion
// coupling is a later stage.
func (o *Orchestrator) calculateComponentVersion() (string, error) {
	inputs, err := version.ResolveComponentVersionInputs(o.cicdFile.Config, o.component, o.baseDir, "", "HEAD")
	if err != nil {
		return "", err
	}

	log.Debug("Component %s: current=%s base=%s (%d commits under %s)",
		o.component, inputs.CurrentDevVersion, inputs.NextEnvVersion, len(inputs.Commits), strings.Join(inputs.Paths, ", "))

	nextVersion, err := inputs.Calc.CalculateNext(inputs.CurrentDevVersion, inputs.NextEnvVersion, inputs.Commits)
	if err != nil {
		return "", fmt.Errorf("calculating version for component %q: %w", o.component, err)
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
	// A component-scoped orchestration answers each tier from the component's
	// own dimension: its (possibly overridden) env ladder, its release marker
	// under latest_release.components.<component>, and its strict tag
	// namespace. The single-component path (component == "") is untouched
	// below.
	if o.component != "" {
		return o.calculateComponentChangelogRefs()
	}

	envs := o.cicdFile.Config.EnvironmentNames()
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

// calculateComponentChangelogRefs is calculateChangelogRefs for a
// component-scoped orchestration. The same three tiers apply, each read from
// the component's own dimension:
//
//  1. The next env on the COMPONENT's effective ladder (its environments
//     override when declared, else the shared list), probed against the
//     working state map the component overlay populated. Walking the global
//     ladder here would index an env outside an overridden component's ladder
//     and collapse the base to the initial commit.
//  2. The component's recorded release marker, read from the parsed
//     latest_release.components.<component> subtree. The flat top-level
//     latest_release fields are structurally empty for a component-scoped
//     manifest (componentStateWrites only ever writes the component leaf, and
//     OverlayComponentState deliberately lifts state rows only, so a lifted
//     marker can never leak back through a flat write), which is why the
//     flat probes in calculateChangelogRefs can never answer for a component
//     and, unfixed, every post-publish component orchestrate re-manifested
//     the full-history changelog of issue #80.
//  3. The component's latest published release tag under its strict grammar,
//     the same base calculateComponentVersion resolves, so a component whose
//     marker was never recorded still anchors on its last release. Only when
//     the component has never released anything does the base fall to the
//     repo's initial commit.
func (o *Orchestrator) calculateComponentChangelogRefs() (string, string) {
	// 1. Intermediate env on the component's own ladder: compare against the
	// component's next-env row (overlaid into the working state map).
	envs := o.componentEnvironmentNames()
	envIndex := indexOf(envs, o.environment)
	if envIndex >= 0 && envIndex < len(envs)-1 {
		nextEnv := envs[envIndex+1]
		if nextState := o.cicdFile.State[nextEnv]; nextState != nil && nextState.SHA != "" {
			return nextState.SHA, nextState.Version
		}
	}

	// 2. The component's recorded release marker.
	if o.cicdFile.LatestRelease != nil {
		if rel := o.cicdFile.LatestRelease.Components[o.component]; rel != nil && rel.SHA != "" {
			return rel.SHA, rel.Version
		}
	}

	// 2b. No recorded marker: the component's latest published release tag.
	// Failures fall through to the initial-commit tier rather than erroring,
	// matching this function's best-effort contract; the version calculation
	// path surfaces the same lookup's errors loudly.
	if resolved, err := o.cicdFile.Config.ResolveComponent(o.component); err == nil {
		if tag, sha, tagErr := git.GetLatestReleaseTagSpec(o.baseDir, resolved.TagGrammarSpec()); tagErr == nil && sha != "" {
			return sha, tag
		}
	}

	// 3. Nothing released yet: fall back to the repo's initial commit.
	initialCommit, _ := o.gitOutput("rev-list", "--max-parents=0", "HEAD")
	return initialCommit, ""
}

// componentEnvironmentNames returns the scoped component's effective env
// ladder: its environments override when the config declares the component,
// else the shared global list. A component recorded only under
// state.components (state-seeded, undeclared) carries no override, so the
// global ladder applies and resolving the undeclared name is not attempted.
func (o *Orchestrator) componentEnvironmentNames() []string {
	if _, declared := o.cicdFile.Config.Components[o.component]; !declared {
		return o.cicdFile.Config.EnvironmentNames()
	}
	resolved, err := o.cicdFile.Config.ResolveComponent(o.component)
	if err != nil {
		log.Warn("Failed to resolve component %s for changelog refs: %v", o.component, err)
		return o.cicdFile.Config.EnvironmentNames()
	}
	return resolved.Config.EnvironmentNames()
}

// writeConfig writes the updated manifest back to disk. It rewrites only the
// mutable state subtree of the on-disk manifest, so the top-level manifest key
// wrapper and every config key, including ones this binary does not model (for
// example cli_version_sha on a SHA-pinned repo running an older cascade), are
// preserved verbatim rather than dropped on the round-trip. Finalize mutates
// only state, so a full re-marshal of the typed config is never needed here.
func (o *Orchestrator) writeConfig() error {
	key := config.DefaultManifestKey
	if o.cicdFile.Config != nil && o.cicdFile.Config.ManifestKey != "" {
		key = o.cicdFile.Config.ManifestKey
	}

	current, err := os.ReadFile(o.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	data, err := o.serializeState(current, key)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(o.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	log.Debug("Wrote updated config to %s", o.configPath)
	return nil
}

// serializeState rewrites current with the orchestrator's owned state. In the
// single-component form (component == "") it reconciles the whole flat state node
// via WriteManifestState, byte-identical to the historical behavior. In the
// component-scoped form it node-patches only state.components.<component>.<env>
// through WriteScopedState, so a sibling component present in current survives
// verbatim, including keys this binary does not model.
func (o *Orchestrator) serializeState(current []byte, key string) ([]byte, error) {
	if o.component != "" {
		return config.WriteScopedState(current, key, o.componentStateWrites()...)
	}
	return config.WriteManifestState(current, key, o.cicdFile.State, o.cicdFile.LatestRelease)
}

// componentStateWrites builds the component-scoped write the orchestrator owns
// from its in-memory state: one state directive addressing
// state.components.<component>.<env> for the orchestrated environment. It is
// re-appliable: on a rejected push the retry loop re-fetches trunk, hard-resets
// to the upstream tip, and reapplyStateLeaf re-runs writeConfig against those
// fresh trunk bytes, so each attempt deterministically re-derives the same owned
// leaf onto whatever a concurrent sibling component already landed, and that
// sibling's subtree is never rebuilt or dropped. A missing env state yields no
// write (a nil State on a StateWrite means delete), so an unexpected miss never
// becomes an accidental node delete.
func (o *Orchestrator) componentStateWrites() []config.StateWrite {
	st := o.cicdFile.State[o.environment]
	if st == nil {
		return nil
	}
	return []config.StateWrite{{
		Component: o.component,
		Env:       o.environment,
		State:     st,
	}}
}

// commitAndPush commits and pushes state changes.
func (o *Orchestrator) commitAndPush(version string) error {
	// For a component-scoped run, re-derive the owned leaf onto the CURRENT trunk
	// manifest before the first commit: re-fetch trunk, hard-reset the working tree
	// to the upstream tip, and node-patch only state.components.<component>.<env>
	// back in through WriteScopedState. This makes the very first attempt rest on
	// fresh trunk bytes rather than the checkout-time base, so a concurrent
	// sibling's leaf is preserved even when the push then fast-forwards with no
	// rejection to trigger the retry-loop re-apply. It mirrors the Contents-API
	// write path, which re-reads fresh trunk on every attempt including the first.
	// The single-component path (component == "") keeps its historical behavior
	// byte-identical: it commits the checkout-base bytes and rebases only on a
	// rejected push.
	if o.component != "" {
		if err := git.RefetchAndReset(o.baseDir); err != nil {
			return err
		}
		if err := o.writeConfig(); err != nil {
			return err
		}
	}

	// Check if there are changes to commit. A failing status must surface: its
	// empty output is otherwise indistinguishable from "no changes", and finalize
	// would report success without ever committing the state write.
	status, err := o.gitOutput("status", "--porcelain", o.configPath)
	if err != nil {
		return fmt.Errorf("checking manifest status before state commit: %w", err)
	}
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

	return o.pushStateWithRetry()
}

// pushStateWithRetry pushes the committed state change, retrying when the push is
// rejected (for example a non-fast-forward caused by a concurrent state writer or
// a "[skip ci]" commit landing on trunk between checkout and push). It delegates
// to git.PushWithRebaseRetry against the orchestrator's base directory.
//
// For a component-scoped run it passes WithReapply(reapplyStateLeaf): on a
// rejected push the shared loop re-fetches trunk and re-derives only this
// component's owned leaf onto the fresh bytes, so a concurrent sibling component's
// adjacent leaf survives where a textual rebase would conflict. The
// single-component path passes no re-apply hook, keeping the historical
// textual-rebase behaviour byte-for-byte and semantically identical (it writes
// the whole state node via WriteManifestState, so there is no adjacent-leaf
// sibling to converge against).
func (o *Orchestrator) pushStateWithRetry() error {
	opts := []git.Option{git.WithDir(o.baseDir), git.WithBackoff(o.pushBackoff)}
	if o.component != "" {
		opts = append(opts, git.WithReapply(o.reapplyStateLeaf))
	}
	if err := git.PushWithRebaseRetry(opts...); err != nil {
		return err
	}
	log.Info("Committed and pushed state changes")
	return nil
}

// reapplyStateLeaf re-derives the orchestrator's owned component state leaf onto
// the re-fetched trunk bytes and re-commits it. The push-retry loop calls it
// after hard-resetting the local branch to the upstream tip, so writeConfig reads
// the fresh trunk manifest (carrying any concurrent sibling leaf), node-patches
// only state.components.<component>.<env> back in via WriteScopedState, and the
// re-staged commit is pushed. It mirrors, in the git-checkout world, the
// re-appliable WriteScopedState closures the promote, hotfix, and rollback
// finalizers run against the Contents API.
func (o *Orchestrator) reapplyStateLeaf() error {
	if err := o.writeConfig(); err != nil {
		return err
	}
	if err := o.gitRun("add", o.configPath); err != nil {
		return err
	}
	message := fmt.Sprintf("chore: update state for %s [skip ci]", o.environment)
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = o.baseDir
	cmd.Env = git.BoundaryEnv(o.baseDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		// The owned leaf is already present on the re-fetched trunk (for example
		// a same-component racer landed it), so there is nothing to re-commit;
		// the following push is a clean no-op. Any other commit failure is real.
		if strings.Contains(string(out), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit failed: %s: %w", string(out), err)
	}
	return nil
}

// gitOutput runs a git command and returns stdout. A failure is wrapped with
// the command line and git's stderr, so a caller propagating it reports what
// actually went wrong instead of a bare exit status.
func (o *Orchestrator) gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = o.baseDir
	cmd.Env = git.BoundaryEnv(o.baseDir)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
				return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), stderr, err)
			}
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRun runs a git command.
func (o *Orchestrator) gitRun(args ...string) error {
	log.Trace("git %s", strings.Join(args, " "))
	cmd := exec.Command("git", args...)
	cmd.Dir = o.baseDir
	cmd.Env = git.BoundaryEnv(o.baseDir)
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

