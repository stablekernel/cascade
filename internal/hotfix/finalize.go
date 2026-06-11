package hotfix

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/release"
	"github.com/stablekernel/cascade/internal/version"
)

// releaseManager is the subset of release operations the finalize verb needs.
// The production implementation is *release.Manager; tests inject a stub. It is
// a small interface with a single method so callers without GitHub context are
// not forced to provide one.
type releaseManager interface {
	Manage(opts release.Options) (*release.Result, error)
}

// tagLister returns the repository's tags so the finalize verb can allocate the
// next free hotfix version without colliding with existing tags. The default
// implementation lists local git tags; tests inject a fixed set.
type tagLister interface {
	ListTags() ([]string, error)
}

// statePusher commits the manifest change to trunk and pushes it with the
// rebase-retry behavior promote uses. The default implementation reuses the
// shared git helper; tests inject a recorder.
type statePusher interface {
	CommitAndPush(path, message string) error
}

// gitTipReader resolves the tip SHA of a local branch. The default implementation
// shells out to git; tests reuse the planner's execGitRunner.
type gitTipReader interface {
	LocalBranchSHA(name string) (string, error)
}

// execTagLister lists local git tags.
type execTagLister struct{}

func (execTagLister) ListTags() ([]string, error) {
	tags, err := git.ListTags()
	if err != nil {
		return nil, fmt.Errorf("listing tags: %w", err)
	}
	return tags, nil
}

// gitStatePusher commits and pushes the manifest with the promote rebase-retry.
type gitStatePusher struct{}

func (gitStatePusher) CommitAndPush(path, message string) error {
	return git.CommitAndPushWithRetry(path, message)
}

// Finalizer writes the diverged state, tag, and release object for a completed
// hotfix. It mirrors the inputs of promote's Finalizer but targets one env on
// its integration branch rather than a trunk promotion.
type Finalizer struct {
	cicd        *config.CICDFile
	configPath  string
	manifestKey string
	actor       string
	dryRun      bool

	deployResults map[string]string
	buildResults  map[string]string

	releaseMgr releaseManager
	tagLister  tagLister
	pusher     statePusher
	tipReader  gitTipReader
}

// FinalizerOptions carries the required inputs for NewFinalizer.
type FinalizerOptions struct {
	ConfigPath  string
	ManifestKey string
	Actor       string
}

// FinalizeOption configures optional, additive Finalizer behavior.
type FinalizeOption func(*Finalizer)

// WithFinalizeDryRun computes the finalize plan without writing state, tags, or
// release objects.
func WithFinalizeDryRun(dryRun bool) FinalizeOption {
	return func(f *Finalizer) { f.dryRun = dryRun }
}

// WithReleaseManager injects the release operations. When unset, finalize builds
// a *release.Manager from GITHUB_REPOSITORY and the release token at run time.
func WithReleaseManager(m releaseManager) FinalizeOption {
	return func(f *Finalizer) {
		if m != nil {
			f.releaseMgr = m
		}
	}
}

// WithTagLister injects the existing-tag lookup used for version allocation.
func WithTagLister(l tagLister) FinalizeOption {
	return func(f *Finalizer) {
		if l != nil {
			f.tagLister = l
		}
	}
}

// WithStatePusher injects the manifest commit/push. The default reuses the
// shared rebase-retry helper.
func WithStatePusher(p statePusher) FinalizeOption {
	return func(f *Finalizer) {
		if p != nil {
			f.pusher = p
		}
	}
}

// NewFinalizer constructs a Finalizer over the manifest at opts.ConfigPath.
func NewFinalizer(opts FinalizerOptions, options ...FinalizeOption) (*Finalizer, error) {
	key := opts.ManifestKey
	if key == "" {
		key = config.DefaultManifestKey
	}

	cicd, err := config.ParseManifestFile(opts.ConfigPath, key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	actor := opts.Actor
	if actor == "" {
		if a := os.Getenv("GITHUB_ACTOR"); a != "" {
			actor = a
		} else {
			actor = "github-actions[bot]"
		}
	}

	f := &Finalizer{
		cicd:          cicd,
		configPath:    opts.ConfigPath,
		manifestKey:   key,
		actor:         actor,
		deployResults: make(map[string]string),
		buildResults:  make(map[string]string),
		tagLister:     execTagLister{},
		pusher:        gitStatePusher{},
		tipReader:     execGitRunner{},
	}
	for _, o := range options {
		o(f)
	}
	return f, nil
}

// SetDeployResult records the result of a deploy job, mirroring
// promote.Finalizer.SetDeployResult. Valid results: "success", "failure",
// "skipped", "cancelled". Only successful deploys update per-deploy state.
func (f *Finalizer) SetDeployResult(name, result string) {
	f.deployResults[name] = result
}

// SetBuildResult records the conclusion of a build job. Valid results mirror
// SetDeployResult; only successful builds update per-build state.
func (f *Finalizer) SetBuildResult(name, result string) {
	f.buildResults[name] = result
}

// Finalize writes the diverged state for a completed hotfix on env/<targetEnv>.
//
// targetEnv is the environment being hotfixed; mergeSHA is the tip of
// env/<targetEnv> after the resolution PR merged; fixSHA is the trunk commit the
// hotfix carries; baseSHA is the trunk anchor the integration branch diverged
// from. It cross-checks the merge SHA against the env-branch tip, allocates the
// next free hotfix version, snapshots the prior state into the Previous ring,
// writes the divergence fields and substates, commits the manifest to trunk, and
// creates the hotfix tag and release object.
//
// Finalize is idempotent on identical inputs: a rerun after the state already
// records the merge SHA is a no-op that neither double-applies patches nor
// re-snapshots Previous.
func (f *Finalizer) Finalize(targetEnv, mergeSHA, fixSHA, baseSHA string) error {
	cfg := f.cicd.Config
	if cfg == nil {
		return fmt.Errorf("manifest has no config block")
	}
	if cfg.GetEnvironmentIndex(targetEnv) == -1 {
		return fmt.Errorf("%q is not a configured environment", targetEnv)
	}

	prior := f.cicd.State[targetEnv]
	if prior == nil || prior.SHA == "" {
		return fmt.Errorf("environment %q has no recorded state SHA", targetEnv)
	}

	branch := envBranch(targetEnv)

	// Idempotency gate: if state already records the merge SHA, finalize already
	// ran for these inputs. Re-running must not double-apply.
	if prior.SHA == mergeSHA {
		return nil
	}

	// Cross-check the merge SHA equals the env-branch tip.
	tip, err := f.tipReader.LocalBranchSHA(branch)
	if err != nil {
		return fmt.Errorf("reading tip of %s: %w", branch, err)
	}
	if tip != mergeSHA {
		return fmt.Errorf(
			"merge SHA %s does not match %s tip %s; the resolution branch advanced or an earlier run was interrupted: re-run finalize for the actual tip",
			short(mergeSHA), branch, short(tip))
	}

	// Allocate the next free hotfix version over the prior version.
	hotfixVersion, err := f.allocateVersion(prior.Version)
	if err != nil {
		return err
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)

	if f.dryRun {
		return nil
	}

	// Snapshot the prior state into the Previous ring (newest first).
	snapshot := config.EnvStateSnapshot{
		SHA:         prior.SHA,
		Version:     prior.Version,
		CommittedAt: prior.CommittedAt,
		CommittedBy: prior.CommittedBy,
	}
	prior.Previous = append([]config.EnvStateSnapshot{snapshot}, prior.Previous...)

	// Carry BaseSHA forward when already diverged; otherwise anchor it now.
	if prior.BaseSHA == "" {
		prior.BaseSHA = baseSHA
	}
	prior.Patches = append(prior.Patches, fixSHA)
	prior.Ref = branch
	prior.SHA = mergeSHA
	prior.Version = hotfixVersion
	prior.CommittedAt = timestamp
	prior.CommittedBy = f.actor

	// Record per-deploy and per-build substates for successful jobs.
	f.recordSubstates(prior, mergeSHA, hotfixVersion, timestamp)

	if err := f.writeConfig(); err != nil {
		return err
	}

	message := fmt.Sprintf("chore: record hotfix %s on %s [skip ci]", hotfixVersion, targetEnv)
	if err := f.pusher.CommitAndPush(f.configPath, message); err != nil {
		return fmt.Errorf("committing hotfix state: %w", err)
	}

	// Create the hotfix tag and release object.
	if err := f.createRelease(cfg, targetEnv, mergeSHA, hotfixVersion, fixSHA, prior.Version); err != nil {
		return err
	}

	return nil
}

// allocateVersion returns the next free hotfix version over priorVersion.
//
// For an rc-based version (e.g. v1.4.0-rc.2) it allocates the next free dotted
// vX.Y.Z-rc.N.hotfix.M, skipping any hotfix tag that already exists. For a
// published version (e.g. v1.3.0, no rc segment) it allocates the next free
// patch bump (v1.3.1, v1.3.2, ...), reconciling against existing tags so it does
// not collide with a patch the normal release flow may also mint.
func (f *Finalizer) allocateVersion(priorVersion string) (string, error) {
	if priorVersion == "" {
		return "", fmt.Errorf("target environment has no recorded version; cannot allocate a hotfix version")
	}
	v, err := version.Parse(priorVersion)
	if err != nil {
		return "", fmt.Errorf("parsing target version %q: %w", priorVersion, err)
	}

	tags, err := f.tagLister.ListTags()
	if err != nil {
		return "", err
	}
	existing := make(map[string]bool, len(tags))
	for _, t := range tags {
		existing[t] = true
	}

	if v.PreRelease >= 0 {
		// RC-based: nested .hotfix.M segment.
		for m := 1; ; m++ {
			candidate := v.WithHotfix(m).String()
			if !existing[candidate] {
				return candidate, nil
			}
		}
	}

	// Published base: normal patch bump, reconciled against existing tags.
	next := v
	for {
		next = next.Bump(version.BumpPatch)
		candidate := next.String()
		if !existing[candidate] {
			return candidate, nil
		}
	}
}

// recordSubstates writes per-deploy and per-build substates for successful jobs,
// mirroring promote's finalize substate handling.
func (f *Finalizer) recordSubstates(state *config.EnvState, sha, ver, timestamp string) {
	for name, result := range f.deployResults {
		if result != "success" {
			continue
		}
		if state.Deploys == nil {
			state.Deploys = make(map[string]*config.DeployState)
		}
		if state.Deploys[name] == nil {
			state.Deploys[name] = &config.DeployState{}
		}
		ds := state.Deploys[name]
		ds.SHA = sha
		ds.Version = ver
		ds.DeployedAt = timestamp
		ds.DeployedBy = f.actor
	}

	for name, result := range f.buildResults {
		if result != "success" {
			continue
		}
		if state.Builds == nil {
			state.Builds = make(map[string]*config.BuildState)
		}
		if state.Builds[name] == nil {
			state.Builds[name] = &config.BuildState{}
		}
		bs := state.Builds[name]
		bs.SHA = sha
		bs.BuiltAt = timestamp
		bs.BuiltBy = f.actor
	}
}

// createRelease creates the hotfix tag and release object. For a prerelease-env
// target the release is promoted to a GitHub prerelease, superseding the env's
// current prerelease object; for other envs it stays a draft.
func (f *Finalizer) createRelease(cfg *config.TrunkConfig, targetEnv, sha, hotfixVersion, fixSHA, baseVersion string) error {
	mgr, err := f.resolveReleaseManager()
	if err != nil {
		return err
	}

	body := fmt.Sprintf("Hotfix based on %s, carries trunk commit %s.", baseVersion, short(fixSHA))

	if _, err := mgr.Manage(release.Options{
		Action:      release.ActionCreate,
		Environment: targetEnv,
		SHA:         sha,
		Tag:         hotfixVersion,
		Changelog:   body,
		CreateTag:   true,
	}); err != nil {
		return fmt.Errorf("creating hotfix release: %w", err)
	}

	if f.isPrereleaseEnv(cfg, targetEnv) {
		if _, err := mgr.Manage(release.Options{
			Action:      release.ActionPrerelease,
			Environment: targetEnv,
			SHA:         sha,
			Tag:         hotfixVersion,
		}); err != nil {
			return fmt.Errorf("promoting hotfix release to prerelease: %w", err)
		}
	}

	return nil
}

// resolveReleaseManager returns the injected release manager or builds one from
// the environment.
func (f *Finalizer) resolveReleaseManager() (releaseManager, error) {
	if f.releaseMgr != nil {
		return f.releaseMgr, nil
	}
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return nil, fmt.Errorf("GITHUB_REPOSITORY is not set; cannot create the hotfix release")
	}
	return release.NewManager(repo, releaseToken()), nil
}

// releaseToken resolves the GitHub token for release operations from the
// environment, preferring an explicit RELEASE_TOKEN.
func releaseToken() string {
	if t := os.Getenv("RELEASE_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GITHUB_TOKEN")
}

// isPrereleaseEnv reports whether env is the prerelease env (second from top),
// mirroring promote's prerelease-env detection.
func (f *Finalizer) isPrereleaseEnv(cfg *config.TrunkConfig, env string) bool {
	envs := cfg.Environments
	if len(envs) < 2 {
		return false
	}
	return env == envs[len(envs)-2]
}

// writeConfig writes the updated manifest back to disk, wrapped in the manifest
// key, matching the layout promote's finalize produces.
func (f *Finalizer) writeConfig() error {
	wrapper := map[string]any{
		f.manifestKey: f.cicd,
	}
	data, err := yaml.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	if err := os.WriteFile(f.configPath, data, 0o600); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	return nil
}
