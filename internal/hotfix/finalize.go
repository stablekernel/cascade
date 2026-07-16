package hotfix

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/globals"
	"github.com/stablekernel/cascade/internal/release"
	"github.com/stablekernel/cascade/internal/statewrite"
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

// statePusher commits the manifest change and lands it on the given trunk
// branch. The default implementation writes via the GitHub Contents API on real
// GitHub and plain git under act; tests inject a recorder.
type statePusher interface {
	CommitAndPush(path, branch, message string) error
}

// gitTipReader resolves the tip SHA of an env branch. The default implementation
// shells out to git; tests reuse the planner's execGitRunner.
type gitTipReader interface {
	LocalBranchSHA(name string) (string, error)
}

// trunkStateReader returns the raw manifest bytes as they exist on the trunk
// branch, so finalize can read prior env state from trunk rather than from the
// checked-out env branch. Promote finalize writes env state only to trunk, so
// the env branch the hotfix merged into lags trunk and can record a stale or
// absent state SHA for the target env; reading at trunk is the source of truth.
// The default implementation reads via the GitHub Contents API on real GitHub
// and plain git under act; tests inject a stub.
type trunkStateReader interface {
	ReadManifest(path, trunk string) ([]byte, error)
}

// gitOrAPITrunkReader reads the manifest at the trunk ref. On real GitHub it
// fetches the file through the Contents REST API at the trunk ref; under
// act/gitea it fetches the trunk branch and shows the blob at that ref.
type gitOrAPITrunkReader struct{}

func (gitOrAPITrunkReader) ReadManifest(path, trunk string) ([]byte, error) {
	if isRealGitHub() {
		return readManifestViaAPI(path, trunk)
	}
	return readManifestViaGit(path, trunk)
}

// readManifestViaAPI fetches the manifest at the trunk ref through the GitHub
// Contents REST API using the gh CLI, returning the raw file bytes.
func readManifestViaAPI(path, trunk string) ([]byte, error) {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return nil, fmt.Errorf("GITHUB_REPOSITORY is not set; cannot read trunk state via API")
	}
	apiPath := fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, path, trunk)
	out, err := exec.Command("gh", "api", apiPath, "-H", "Accept: application/vnd.github.raw").Output()
	if err != nil {
		return nil, fmt.Errorf("reading trunk manifest via API at %s: %w", trunk, err)
	}
	return out, nil
}

// readManifestViaGit fetches the trunk branch and returns the manifest blob at
// that ref. Used in the act/gitea e2e environment, where there is no GitHub API.
func readManifestViaGit(path, trunk string) ([]byte, error) {
	if out, err := exec.Command("git", "fetch", "origin", trunk).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git fetch origin %s failed: %s: %w", trunk, strings.TrimSpace(string(out)), err)
	}
	out, err := exec.Command("git", "show", "origin/"+trunk+":"+path).Output()
	if err != nil {
		return nil, fmt.Errorf("reading trunk manifest via git at origin/%s: %w", trunk, err)
	}
	return out, nil
}

// envTipReader resolves an env branch tip in a CI checkout. The finalize job
// checks out trunk and fetches env/* into refs/remotes/origin/*, so the branch
// is usually a remote-tracking ref rather than a local one. It resolves the
// local ref first (preserving local-clone behavior) and falls back to the
// remote-tracking ref so the env-branch cross-check works on a fresh runner.
type envTipReader struct{}

func (envTipReader) LocalBranchSHA(name string) (string, error) {
	for _, ref := range []string{"refs/heads/" + name, "refs/remotes/origin/" + name} {
		out, err := exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}").Output()
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", fmt.Errorf("git rev-parse env branch %q: not found as a local or origin-tracking ref", name)
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

// gitStatePusher commits the manifest change and lands it on the trunk branch.
//
// On real GitHub the write goes through the Contents REST API (signed commit,
// branch-protection bypass with a capable token), mirroring promote finalize.
// In the act/gitea e2e environment there is no GitHub API, so the change is
// committed and pushed with plain git. The finalize job runs on a pull_request
// (closed) event, which checks out in detached HEAD, so the push targets the
// trunk branch explicitly (git push origin HEAD:<trunk>) rather than relying on
// an upstream tracking branch.
type gitStatePusher struct{}

func (gitStatePusher) CommitAndPush(path, branch, message string) error {
	return commitAndPushGit(path, branch, message)
}

// apiStatePusher commits the manifest to trunk through the GitHub Contents REST
// API using the shared optimistic-lock retry loop, so concurrent env finalizers
// that each touch only their own env state merge rather than clobbering each
// other on the file blob SHA. mutate re-applies this hotfix's state change onto
// whatever trunk bytes the loop fetches.
type apiStatePusher struct {
	mutate statewrite.Mutate
	author statewrite.Identity
}

func (p apiStatePusher) CommitAndPush(path, branch, message string) error {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return fmt.Errorf("GITHUB_REPOSITORY is not set; cannot write state via API")
	}
	return statewrite.CommitWithRetry(statewrite.Options{
		Client:  statewrite.NewGHClient(),
		Repo:    repo,
		Path:    path,
		Ref:     branch,
		Message: message,
		Mutate:  p.mutate,
		Author:  p.author,
	})
}

// gitIdentity resolves the author/committer for a Contents API state commit from
// the manifest git config, defaulting to the github-actions[bot] identity when
// the config is absent. This attributes the automated state commit to the bot
// rather than the token owner GitHub would otherwise stamp.
func gitIdentity(cfg *config.TrunkConfig) statewrite.Identity {
	if cfg == nil {
		return statewrite.Identity{}
	}
	return statewrite.Identity{Name: cfg.GetGitUserName(), Email: cfg.GetGitUserEmail()}
}

// isRealGitHub reports whether the workflow runs on github.com rather than an
// act/gitea e2e environment, detected by GITHUB_SERVER_URL as the generated
// dispatch steps do.
func isRealGitHub() bool {
	server := os.Getenv("GITHUB_SERVER_URL")
	return server == "" || server == "https://github.com"
}

// commitAndPushGit commits the manifest and pushes it to the trunk branch with
// plain git. Used in the act/gitea e2e environment, which enforces neither
// branch protection nor commit signatures. The push refspec is explicit so it
// works from the detached-HEAD checkout of a pull_request event.
func commitAndPushGit(path, branch, message string) error {
	// Dry-run backstop: finalize's explicit gate returns before this point, so
	// hitting it under --dry-run means a caller missed its gate.
	if err := globals.GuardMutation(fmt.Sprintf("commit and push %s to %s", path, branch)); err != nil {
		return err
	}

	if out, err := exec.Command("git", "config", "user.name", "github-actions[bot]").CombinedOutput(); err != nil {
		return fmt.Errorf("git config user.name failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if out, err := exec.Command("git", "config", "user.email", "github-actions[bot]@users.noreply.github.com").CombinedOutput(); err != nil {
		return fmt.Errorf("git config user.email failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if out, err := exec.Command("git", "add", path).CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if out, err := exec.Command("git", "commit", "-m", message).CombinedOutput(); err != nil {
		if strings.Contains(string(out), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	refspec := "HEAD:refs/heads/" + branch
	if out, err := exec.Command("git", "push", "origin", refspec).CombinedOutput(); err != nil {
		return fmt.Errorf("git push origin %s failed: %s: %w", refspec, strings.TrimSpace(string(out)), err)
	}
	return nil
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

	// component, when non-empty, names the declared component this hotfix is
	// scoped to. It is set only via WithComponent by a per-component generated
	// hotfix workflow. An empty value selects the single-component path, whose
	// state write and env-branch name are byte-identical to the historical
	// behavior. A non-empty value records state under
	// state.components.<component>.<env> via WriteScopedState (preserving every
	// sibling component subtree under the concurrent-finalize retry loop),
	// resolves the hotfix version and tag in the component's own tag namespace,
	// and names the integration branch env/<component>/<env>.
	component string

	// trunkRaw holds the raw manifest bytes read from the trunk branch. It is the
	// write basis for the component-scoped local write so a node-patch preserves
	// every sibling component's trunk-recorded subtree rather than overwriting it
	// with the lagging env-branch checkout.
	trunkRaw []byte

	deployResults map[string]string
	buildResults  map[string]string

	releaseMgr  releaseManager
	tagLister   tagLister
	pusher      statePusher
	tipReader   gitTipReader
	trunkReader trunkStateReader

	// pusherInjected records whether a caller supplied an explicit statePusher.
	// When true, Finalize uses that pusher verbatim (tests inject a recorder);
	// when false on real GitHub, Finalize swaps in the API retry pusher.
	pusherInjected bool
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

// WithComponent scopes the finalize to a declared component. It records state
// under state.components.<name>.<env>, resolves the hotfix version and tag in the
// component's tag namespace, and names the integration branch env/<name>/<env>.
// An empty name (the default) keeps the single-component behavior byte-identical.
func WithComponent(name string) FinalizeOption {
	return func(f *Finalizer) { f.component = name }
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
			f.pusherInjected = true
		}
	}
}

// WithTrunkStateReader injects the reader that returns the manifest as it exists
// on the trunk branch. Finalize uses it to read prior env state from trunk, the
// source of truth, rather than from the lagging env branch the hotfix merged
// into. The default reads via the GitHub Contents API on real GitHub and plain
// git under act.
func WithTrunkStateReader(r trunkStateReader) FinalizeOption {
	return func(f *Finalizer) {
		if r != nil {
			f.trunkReader = r
		}
	}
}

// WithTipReader injects the reader Finalize uses to cross-check the merge SHA
// against the resolution branch tip. The default reads the local env-branch tip
// via git. The what-if simulator injects a record-only reader so finalize can
// run without a git checkout.
func WithTipReader(r gitTipReader) FinalizeOption {
	return func(f *Finalizer) {
		if r != nil {
			f.tipReader = r
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
		tipReader:     envTipReader{},
		trunkReader:   gitOrAPITrunkReader{},
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
// env/<targetEnv> after the resolution PR merged; fixSHAs are the trunk commits
// the hotfix carries (every cherry-picked commit, in apply order); baseSHA is the
// trunk anchor the integration branch diverged from. It cross-checks the merge
// SHA against the env-branch tip, allocates the next free hotfix version,
// snapshots the prior state into the Previous ring, writes the divergence fields
// and substates, commits the manifest to trunk, and creates the hotfix tag and
// release object. Every commit in fixSHAs is appended to the env's recorded
// patch set so a multi-commit hotfix records all of its commits, not just the
// first.
//
// Finalize is idempotent on identical inputs: a rerun after the state already
// records the merge SHA neither double-applies patches nor re-snapshots
// Previous. Because the state commit lands before the tag/release step, a rerun
// additionally converges the release: it re-invokes the release step with the
// recorded version (find-or-create, tolerant of an existing tag), so a run that
// failed between the state commit and the release creation is completed by its
// rerun rather than silently reported as done with the release missing.
func (f *Finalizer) Finalize(targetEnv, mergeSHA string, fixSHAs []string, baseSHA string) error {
	if len(fixSHAs) == 0 {
		return fmt.Errorf("no fix commits supplied; finalize needs at least one trunk commit")
	}
	cfg := f.cicd.Config
	if cfg == nil {
		return fmt.Errorf("manifest has no config block")
	}
	if cfg.GetEnvironmentIndex(targetEnv) == -1 {
		return fmt.Errorf("%q is not a configured environment", targetEnv)
	}

	// Resolve trunk the same way the state write does: the configured trunk
	// branch, defaulting to "main".
	trunk := cfg.TrunkBranch
	if trunk == "" {
		trunk = "main"
	}

	// Read the manifest as it exists on trunk, not from the checked-out env
	// branch. Promote finalize writes env state only to trunk, so the env branch
	// the hotfix merged into lags trunk and can record stale or absent state for
	// every env; trunk is the source of truth. The trunk manifest becomes the
	// WRITE basis below so mutating only the target env preserves every other
	// env's recorded trunk state. Writing the lagging env-branch manifest to
	// trunk would clobber the non-target envs.
	trunkRaw, trunkCICD, err := f.readTrunkManifest(trunk)
	if err != nil {
		return err
	}
	f.cicd = trunkCICD
	f.trunkRaw = trunkRaw
	cfg = f.cicd.Config
	if cfg == nil {
		return fmt.Errorf("trunk manifest has no config block")
	}

	if f.cicd.State == nil {
		f.cicd.State = make(map[string]*config.EnvState)
	}
	// For a component-scoped hotfix the prior env state lives under
	// state.components.<component>.<env>, not the flat state.<env> node. Overlay it
	// into the flat map so the prior-state read, idempotency gate, and Previous
	// ring below see the component's recorded row.
	if err := overlayComponentState(f.cicd, trunkRaw, f.manifestKey, f.component); err != nil {
		return err
	}
	prior := f.cicd.State[targetEnv]
	if prior == nil || prior.SHA == "" {
		return fmt.Errorf("environment %q has no recorded state SHA", targetEnv)
	}

	branch := f.envBranch(targetEnv)

	// Idempotency gate: if state already records the merge SHA, finalize already
	// committed the state marker for these inputs. Re-running must not
	// double-apply state, but it must not blind-return success either: the state
	// commit precedes tag/release creation below, so a rerun can land here with
	// the release step never completed (a release-API failure reddened the prior
	// run after the marker was pushed). Converge instead of skip: re-invoke the
	// release step idempotently with the version the prior run recorded, so the
	// rerun completes the missing tag/release. When everything already exists
	// the re-invocation is a no-op-shaped update.
	if prior.SHA == mergeSHA {
		if f.dryRun {
			return nil
		}
		return f.convergeRelease(cfg, targetEnv, mergeSHA, prior, fixSHAs[0])
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

	// Capture the base version before prior.Version is overwritten below.
	baseVersion := prior.Version

	// Allocate the next free hotfix version over the prior version.
	hotfixVersion, err := f.allocateVersion(prior.Version)
	if err != nil {
		return err
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)

	if f.dryRun {
		return nil
	}

	// Apply the state mutation in place onto the trunk manifest, snapshotting the
	// prior state into the Previous ring and writing the divergence fields and
	// substates. Extracted so the same change can be re-applied against freshly
	// fetched trunk bytes inside the optimistic-lock retry loop below.
	if err := f.applyHotfixState(f.cicd, targetEnv, mergeSHA, hotfixVersion, baseSHA, timestamp, fixSHAs); err != nil {
		return err
	}

	if err := f.writeConfig(targetEnv); err != nil {
		return err
	}

	message := fmt.Sprintf("chore: record hotfix %s on %s [skip ci]", hotfixVersion, targetEnv)

	pusher := f.pusher
	if !f.pusherInjected && isRealGitHub() {
		pusher = apiStatePusher{
			author: gitIdentity(f.cicd.Config),
			mutate: f.hotfixMutation(targetEnv, mergeSHA, hotfixVersion, baseSHA, timestamp, fixSHAs),
		}
	}
	if err := pusher.CommitAndPush(f.configPath, trunk, message); err != nil {
		return fmt.Errorf("committing hotfix state: %w", err)
	}

	// Create the hotfix tag and release object. The release body references the
	// first carried commit as the representative fix SHA. This runs AFTER the
	// state commit: the recorded version pins the allocation, so a failure here
	// is recovered by a rerun that re-creates the same tag through the
	// idempotency-gate convergence above, never by re-allocating a new version.
	if err := f.createRelease(cfg, targetEnv, mergeSHA, hotfixVersion, fixSHAs[0], baseVersion, release.ActionCreate); err != nil {
		return err
	}

	return nil
}

// convergeRelease completes the tag/release step for a finalize whose state
// marker is already recorded on trunk. The hotfix version is the one the prior
// run allocated and committed (prior.Version), so a rerun converges on the same
// tag rather than minting a new one; the base version for the release body is
// recovered from the Previous ring snapshot that same run pushed. The release
// step runs as a find-or-create update, so it creates whatever is missing (git
// tag, release object, prerelease promotion) and is a no-op-shaped update when
// everything already exists.
func (f *Finalizer) convergeRelease(cfg *config.TrunkConfig, targetEnv, mergeSHA string, prior *config.EnvState, fixSHA string) error {
	var baseVersion string
	if len(prior.Previous) > 0 {
		baseVersion = prior.Previous[0].Version
	}
	return f.createRelease(cfg, targetEnv, mergeSHA, prior.Version, fixSHA, baseVersion, release.ActionUpdate)
}

// applyHotfixState applies the hotfix state mutation for targetEnv onto cicd
// using pre-computed values, so it can be re-applied against freshly fetched
// trunk bytes inside the optimistic-lock retry loop. It is idempotent: when the
// manifest already records mergeSHA for targetEnv the call is a no-op, so a retry
// (or rerun) neither double-appends patches nor re-snapshots the Previous ring.
func (f *Finalizer) applyHotfixState(cicd *config.CICDFile, targetEnv, mergeSHA, hotfixVersion, baseSHA, timestamp string, fixSHAs []string) error {
	if cicd.State == nil {
		cicd.State = make(map[string]*config.EnvState)
	}
	prior := cicd.State[targetEnv]
	if prior == nil {
		prior = &config.EnvState{}
		cicd.State[targetEnv] = prior
	}
	if prior.SHA == mergeSHA {
		return nil
	}
	prior.PushPreviousSnapshot(mergeSHA)
	if prior.BaseSHA == "" {
		prior.BaseSHA = baseSHA
	}
	prior.Patches = append(prior.Patches, fixSHAs...)
	prior.Ref = f.envBranch(targetEnv)
	prior.SHA = mergeSHA
	prior.Version = hotfixVersion
	prior.CommittedAt = timestamp
	prior.CommittedBy = f.actor
	f.recordSubstates(prior, mergeSHA, hotfixVersion, timestamp)
	return nil
}

// hotfixMutation returns the re-appliable CommitWithRetry closure that merges
// this hotfix's owned env state onto whatever trunk bytes the loop fetches.
//
// Single-component form (component == ""): re-parse the fetched bytes, re-apply
// the hotfix state mutation for the target env, and reconcile the whole flat
// state node via WriteManifestState. Sibling envs survive because the re-read
// carries them into the typed map. This is byte-identical to the historical
// closure.
//
// Component-scoped form (component != ""): overlay the component's persisted env
// rows from the fetched bytes, re-apply the hotfix mutation, then node-patch only
// state.components.<component>.<targetEnv> via WriteScopedState. It never
// deserializes or rebuilds a sibling component subtree, so on a 409 the loser
// re-reads the winner's committed sibling rows and re-applies only its own leaf,
// leaving every sibling component verbatim, including keys this binary does not
// model.
func (f *Finalizer) hotfixMutation(targetEnv, mergeSHA, hotfixVersion, baseSHA, timestamp string, fixSHAs []string) statewrite.Mutate {
	key := f.manifestKey
	component := f.component
	capturedFixSHAs := append([]string(nil), fixSHAs...)
	return func(current []byte) ([]byte, error) {
		fresh, err := config.ParseManifestBytes(current, key)
		if err != nil {
			return nil, fmt.Errorf("parsing current manifest: %w", err)
		}
		if err := overlayComponentState(fresh, current, key, component); err != nil {
			return nil, err
		}
		if err := f.applyHotfixState(fresh, targetEnv, mergeSHA, hotfixVersion, baseSHA, timestamp, capturedFixSHAs); err != nil {
			return nil, err
		}
		if component != "" {
			data, err := config.WriteScopedState(current, key, f.hotfixStateWrites(fresh, targetEnv)...)
			if err != nil {
				return nil, fmt.Errorf("marshaling merged manifest: %w", err)
			}
			return data, nil
		}
		data, err := config.WriteManifestState(current, key, fresh.State, fresh.LatestRelease)
		if err != nil {
			return nil, fmt.Errorf("marshaling merged manifest: %w", err)
		}
		return data, nil
	}
}

// hotfixStateWrites builds the component-scoped write this finalize owns from its
// already-mutated env state: one directive addressing
// state.components.<component>.<targetEnv>. It is re-appliable, so CommitWithRetry
// re-derives the same owned leaf on a 409 and never rebuilds a sibling subtree. A
// nil target leaf (an unexpected miss) contributes no directive rather than an
// accidental node delete.
func (f *Finalizer) hotfixStateWrites(cicd *config.CICDFile, targetEnv string) []config.StateWrite {
	st := cicd.State[targetEnv]
	if st == nil {
		return nil
	}
	return []config.StateWrite{{
		Component: f.component,
		Env:       targetEnv,
		State:     st,
	}}
}

// readTrunkManifest fetches the manifest as it exists on the trunk branch and
// returns the parsed manifest. It is read from trunk because promote finalize
// writes env state only to trunk; the env branch the hotfix merged into lags
// trunk and can record stale or absent state. The returned manifest is both the
// source of the prior env state and the WRITE basis, so mutating only the target
// env preserves every other env's recorded trunk state.
func (f *Finalizer) readTrunkManifest(trunk string) ([]byte, *config.CICDFile, error) {
	data, err := f.trunkReader.ReadManifest(f.configPath, trunk)
	if err != nil {
		return nil, nil, fmt.Errorf("reading trunk state: %w", err)
	}
	cicd, err := config.ParseManifestBytes(data, f.manifestKey)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing trunk manifest: %w", err)
	}
	return data, cicd, nil
}

// overlayComponentState overlays the named component's recorded per-env rows,
// read from state.components.<component>.<env> in raw, into cicd's flat state map
// so every State[env] lookup in Finalize transparently sees that component's
// seed. It is a no-op when component is empty, keeping the single-component path
// byte-identical. It mirrors promote's component-state overlay: the read
// counterpart to the component-scoped WriteScopedState writes.
func overlayComponentState(cicd *config.CICDFile, raw []byte, manifestKey, component string) error {
	if component == "" {
		return nil
	}
	compState, err := config.ReadComponentState(raw, manifestKey, component)
	if err != nil {
		return fmt.Errorf("reading component %q state: %w", component, err)
	}
	if cicd.State == nil {
		cicd.State = make(map[string]*config.EnvState)
	}
	for env, st := range compState {
		cicd.State[env] = st
	}
	return nil
}

// envBranch returns the integration branch name for env under this finalize's
// component. The default (empty) component yields env/<env>, byte-identical to
// the historical single-component name; a named component yields
// env/<component>/<env> so each component's integration branches occupy a
// disjoint namespace.
func (f *Finalizer) envBranch(env string) string {
	return EnvBranchName(f.component, env)
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
	spec, err := resolveFinalizeSpec(f.cicd, f.component)
	if err != nil {
		return "", err
	}
	v, err := version.ParseWithGrammar(spec, priorVersion)
	if err != nil {
		return "", fmt.Errorf("parsing target version %q: %w", priorVersion, err)
	}
	v = v.WithGrammar(spec)

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
//
// action selects the release verb: the first finalize run passes ActionCreate
// (the release cannot pre-exist, and create skips the find-release lookup and
// its eventual-consistency retry window); the idempotency-gate convergence
// passes ActionUpdate, whose find-or-create shape completes a partially created
// release (tag creation treats an existing tag as success) instead of erroring
// or duplicating it.
func (f *Finalizer) createRelease(cfg *config.TrunkConfig, targetEnv, sha, hotfixVersion, fixSHA, baseVersion string, action release.Action) error {
	mgr, err := f.resolveReleaseManager()
	if err != nil {
		return err
	}

	body := fmt.Sprintf("Hotfix based on %s, carries trunk commit %s.", baseVersion, short(fixSHA))

	created, err := mgr.Manage(release.Options{
		Action:      action,
		Environment: targetEnv,
		SHA:         sha,
		Tag:         hotfixVersion,
		Changelog:   body,
		CreateTag:   true,
	})
	if err != nil {
		return fmt.Errorf("creating hotfix release: %w", err)
	}

	if f.isPrereleaseEnv(cfg, targetEnv) {
		// Thread the created release ID through to avoid a re-lookup: the
		// by-tag endpoint returns 404 for drafts and the list endpoint has a
		// consistency window, so the second env can fail if we re-discover.
		var knownID int64
		if created != nil {
			knownID = created.ReleaseID
		}
		if _, err := mgr.Manage(release.Options{
			Action:         release.ActionPrerelease,
			Environment:    targetEnv,
			SHA:            sha,
			Tag:            hotfixVersion,
			KnownReleaseID: knownID,
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
// environment, preferring an explicit RELEASE_TOKEN, then GITHUB_TOKEN, then
// GH_TOKEN. GH_TOKEN is the reliable fallback in workflows: GITHUB_TOKEN is a
// reserved name that the runner does not always propagate as a step env var.
func releaseToken() string {
	for _, key := range []string{"RELEASE_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if t := os.Getenv(key); t != "" {
			return t
		}
	}
	return ""
}

// isPrereleaseEnv reports whether env is the prerelease env (second from top),
// mirroring promote's prerelease-env detection.
func (f *Finalizer) isPrereleaseEnv(cfg *config.TrunkConfig, env string) bool {
	prerelease := cfg.PrereleaseEnvironment()
	return prerelease != "" && env == prerelease
}

// writeConfig writes the updated manifest back to disk, rewriting only the
// mutable state subtree so any configuration this binary does not model is
// preserved rather than dropped. It matches the layout promote's finalize
// produces.
//
// Single-component form (component == ""): reconcile the whole flat state node
// against the on-disk manifest via WriteManifestState, byte-identical to the
// historical behavior.
//
// Component-scoped form (component != ""): node-patch only
// state.components.<component>.<targetEnv> onto the trunk manifest bytes via
// WriteScopedState. The trunk bytes are the write basis (not the lagging
// env-branch checkout) so every sibling component's trunk-recorded subtree is
// preserved verbatim when the git push lands this file on trunk.
func (f *Finalizer) writeConfig(targetEnv string) error {
	if f.component != "" {
		data, err := config.WriteScopedState(f.trunkRaw, f.manifestKey, f.hotfixStateWrites(f.cicd, targetEnv)...)
		if err != nil {
			return fmt.Errorf("marshaling manifest: %w", err)
		}
		if err := os.WriteFile(f.configPath, data, 0o600); err != nil {
			return fmt.Errorf("writing manifest: %w", err)
		}
		return nil
	}
	current, err := os.ReadFile(f.configPath)
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}
	data, err := config.WriteManifestState(current, f.manifestKey, f.cicd.State, f.cicd.LatestRelease)
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	if err := os.WriteFile(f.configPath, data, 0o600); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	return nil
}
