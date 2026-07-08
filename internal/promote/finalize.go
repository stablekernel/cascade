package promote

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/statewrite"
)

// getEnv returns the value of an environment variable or a default value.
func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// Finalizer handles post-deployment state updates after a promotion completes.
// It updates the manifest state based on:
// - Deploy job results (from GitHub Actions)
// - Promotion result (from preflight output)
// - Release publishing status
type Finalizer struct {
	configPath      string
	targetEnv       string
	cicdFile        *config.CICDFile
	deployResults   map[string]string // deploy name -> conclusion ("success", "failure", "skipped")
	promotionResult *PromotionResult
	actor           string
	overrideSHA     string // non-empty when an auto-committing callback advanced HEAD

	// component, when non-empty, names the declared component this finalization is
	// scoped to. It is set only via WithComponent by a per-component generated
	// promote workflow; an empty value selects the single-component path,
	// byte-identical to today. When set, promoted state is serialized under
	// state.components.<component>.<env> (and latest_release.components.<component>)
	// through config.WriteScopedState, whose node-patch preserves every sibling
	// component verbatim under the concurrent-finalize retry loop.
	component string
	// contentsClient overrides the GitHub Contents client used by writeStateViaAPI.
	// It is nil in production (the default gh-CLI client is constructed on demand)
	// and set only by tests via withContentsClient to drive the optimistic-lock
	// retry against a fake that simulates a concurrent finalizer's 409.
	contentsClient statewrite.ContentsClient

	// cleaner performs the divergence-end side effects (delete env branch, hotfix
	// tags, drafts) when a promotion rejoins a diverged env to trunk. The default
	// is a no-op so non-diverged promotions are unaffected.
	cleaner LifecycleCleaner
	// pendingRejoins collects the diverged environments that rejoined trunk during
	// the in-memory state update, to be cleaned up after the manifest is written.
	pendingRejoins []rejoinEvent

	// now supplies the wall clock used to stamp the audit timestamps written to
	// state. It defaults to time.Now; tests and deterministic golden output can
	// override it through WithClock so the emitted timestamps are exact.
	now func() time.Time
}

// NewFinalizer creates a new Finalizer instance.
// It loads the manifest from configPath and prepares for state updates.
// The manifest must have the ci: key at the top level.
//
// Optional, additive behavior (such as the divergence-end lifecycle cleanup) is
// supplied through functional options so the required inputs stay positional.
func NewFinalizer(configPath, targetEnv string, opts ...FinalizeOption) (*Finalizer, error) {
	return NewFinalizerWithKey(configPath, targetEnv, config.DefaultManifestKey, opts...)
}

// NewFinalizerWithKey creates a Finalizer with a custom manifest key.
func NewFinalizerWithKey(configPath, targetEnv, manifestKey string, opts ...FinalizeOption) (*Finalizer, error) {
	cicdFile, err := config.ParseManifestFile(configPath, manifestKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	actor := getEnv("GITHUB_ACTOR", "github-actions[bot]")

	f := &Finalizer{
		configPath:    configPath,
		targetEnv:     targetEnv,
		cicdFile:      cicdFile,
		deployResults: make(map[string]string),
		actor:         actor,
		cleaner:       noopLifecycleCleaner{},
		now:           time.Now,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f, nil
}

// SetDeployResult records the result of a deploy job.
// Valid results: "success", "failure", "skipped", "cancelled"
func (f *Finalizer) SetDeployResult(name, result string) {
	f.deployResults[name] = result
}

// SetPromotionResult sets the promotion result from preflight output.
// This contains information about which environments to update and release actions.
func (f *Finalizer) SetPromotionResult(pr *PromotionResult) {
	f.promotionResult = pr
}

// SetActor sets the actor performing the finalization (for audit trail).
func (f *Finalizer) SetActor(actor string) {
	f.actor = actor
}

// SetHeadSHA overrides the SHA written to state.<env>.sha during finalization.
// Call this when one or more callbacks declared auto_commits: true, meaning they
// pushed additional commits after the workflow started. Passing the post-callback
// HEAD ensures the recorded state matches what was actually built/deployed rather
// than the triggering commit.
//
// When sha is empty (the default) the SHA from the promotion result is used
// unchanged, preserving existing behavior for runs without auto-committing callbacks.
func (f *Finalizer) SetHeadSHA(sha string) {
	f.overrideSHA = sha
}

// Run executes the finalization logic:
// 1. Updates environment state for all promoted environments
// 2. Updates per-deploy state for successful deploys only
// 3. Updates latest_release state if publishing a release
// 4. Writes the updated manifest back to disk
//
// Note: This updates the in-memory state and writes to disk.
// Call this only when you want to persist changes.
func (f *Finalizer) Run() error {
	f.updateState()
	if err := f.WriteConfig(); err != nil {
		return err
	}
	return f.runLifecycleCleanup()
}

// runLifecycleCleanup performs the divergence-end side effects for every env
// that rejoined trunk during this finalization. It runs only after the manifest
// is persisted, so the source of truth is updated before any branch, tag, or
// release object is removed.
//
// Cleanup is best-effort and never aborts the finalize: the objects it removes
// (integration branch, hotfix tags and release objects) are disposable
// superseded artifacts, and every individual delete is idempotent, so a transient
// failure on one env must not strand the others or wedge an already-persisted
// state write. A failure on one env is logged loudly and cleanup continues with
// the rest; hard-fail is reserved for the state write, which Run performs before
// reaching here. When no env rejoined (the common, non-diverged case) this is a
// no-op and the injected cleaner is never called.
func (f *Finalizer) runLifecycleCleanup() error {
	for _, ev := range f.pendingRejoins {
		if ev.rollbackOrigin {
			// A manual rollback creates no integration branch, hotfix tags, or
			// release objects. The divergence fields were already cleared above,
			// so the rejoin is complete with no side effects to undo.
			continue
		}
		if err := f.cleaner.DeleteEnvBranch(ev.env); err != nil {
			fmt.Printf("Warning: rejoin cleanup for %s: deleting integration branch: %v\n", ev.env, err)
		}
		if err := f.cleaner.CleanHotfixReleases(CleanReleasesRequest{
			Environment: ev.env,
			BaseVersion: ev.baseVersion,
			SHA:         ev.sha,
			Spec:        resolveTagGrammar(f.cicdFile),
		}); err != nil {
			fmt.Printf("Warning: rejoin cleanup for %s: cleaning hotfix releases: %v\n", ev.env, err)
		}
	}
	return nil
}

// updateState performs the in-memory state updates.
// This is separated from Run() to allow dry-run mode.
func (f *Finalizer) updateState() {
	timestamp := f.now().UTC().Format(time.RFC3339)

	// Ensure state maps exist
	if f.cicdFile.State == nil {
		f.cicdFile.State = make(map[string]*config.EnvState)
	}

	// Update environment state from promotion result
	if f.promotionResult != nil {
		for _, promo := range f.promotionResult.Promotions {
			if f.cicdFile.State[promo.Environment] == nil {
				f.cicdFile.State[promo.Environment] = &config.EnvState{}
			}
			state := f.cicdFile.State[promo.Environment]
			// Capture whether this env was diverged BEFORE overwriting its state.
			// A promotion into a diverged env that reaches finalize has already
			// passed the preflight patch-containment gate (the incoming trunk SHA
			// contains every recorded patch), so the env is rejoining trunk: its
			// divergence fields must be cleared and its integration branch, tags,
			// and drafts cleaned up. The base version to clean is the version the
			// env held while diverged, captured here before it is overwritten.
			wasDiverged := state.IsDiverged()
			priorVersion := state.Version
			priorSHA := state.SHA

			// When an auto-committing callback ran, overrideSHA holds the
			// post-callback HEAD; use it so the recorded state points at the
			// commit that was actually built/deployed rather than the triggering SHA.
			newSHA := promo.SHA
			if f.overrideSHA != "" {
				newSHA = f.overrideSHA
			}

			// Record the outgoing state in the deploy-history ring before the
			// env pointer advances. No-op when there is no prior SHA or the env
			// is not actually transitioning.
			state.PushPreviousSnapshot(newSHA)

			state.SHA = newSHA
			state.Version = promo.Version
			state.CommittedAt = timestamp
			state.CommittedBy = f.actor

			// Rejoin: clear divergence fields and schedule cleanup. This is gated
			// on the env having been diverged, so a normal promotion into a
			// non-diverged env touches none of the lifecycle logic.
			if wasDiverged {
				// Capture the divergence origin before clearing the ref: a
				// rollback-origin rejoin clears the same fields but skips the
				// integration-branch and hotfix-release cleanup, which apply
				// only to hotfix divergences.
				rollbackOrigin := IsRollbackRef(state.Ref)
				state.Ref = ""
				state.BaseSHA = ""
				state.Patches = nil
				f.pendingRejoins = append(f.pendingRejoins, rejoinEvent{
					env:            promo.Environment,
					baseVersion:    priorVersion,
					sha:            priorSHA,
					rollbackOrigin: rollbackOrigin,
				})
			}

			// Initialize deploys map if needed
			if state.Deploys == nil {
				state.Deploys = make(map[string]*config.DeployState)
			}
		}
	}

	// Update per-deploy state for successful deploys. In cascade mode the
	// promotion result includes every env in the path (e.g., dev→uat
	// produces promotions for qa and uat); each env's per-deploy state must
	// reflect the new SHA so subsequent change-detection compares against
	// the right base. Without this, state[intermediateEnv].deploys[name]
	// stays empty and the next promotion re-runs the deploy unnecessarily.
	//
	// Guarded on a non-nil promotion result: the per-deploy update reads
	// f.promotionResult.Promotions (and external deploys resolve their SHA
	// from the promotion path), so with no promotion result there is nothing
	// to record. This mirrors the guard on the env-promotion block above and
	// keeps the exported SetDeployResult/Run API from panicking when a deploy
	// result is set without a promotion result.
	if f.promotionResult != nil {
		for name, result := range f.deployResults {
			if result != "success" {
				continue
			}

			// Check if this is an external deploy
			if f.isExternalDeploy(name) {
				f.updateExternalDeployState(name, timestamp)
				continue
			}

			for _, promo := range f.promotionResult.Promotions {
				if promo.Environment == "" || promo.Environment == "release" {
					// Skip the release marker; it tracks publish state, not deploys.
					continue
				}
				if promo.SHA == "" {
					continue
				}
				if f.cicdFile.State[promo.Environment] == nil {
					f.cicdFile.State[promo.Environment] = &config.EnvState{}
				}
				if f.cicdFile.State[promo.Environment].Deploys == nil {
					f.cicdFile.State[promo.Environment].Deploys = make(map[string]*config.DeployState)
				}
				if f.cicdFile.State[promo.Environment].Deploys[name] == nil {
					f.cicdFile.State[promo.Environment].Deploys[name] = &config.DeployState{}
				}

				ds := f.cicdFile.State[promo.Environment].Deploys[name]
				if f.overrideSHA != "" {
					ds.SHA = f.overrideSHA
				} else {
					ds.SHA = promo.SHA
				}
				// Record the version this deployable deployed, mirroring the
				// env-level Version write above. This is what lets state answer
				// "which version of <name> is live in <env>" per deployable,
				// independent of the env-level version (the data foundation for
				// per-deployable rollback). Empty promo.Version leaves the prior
				// value untouched so non-versioned promotions stay non-breaking.
				if promo.Version != "" {
					ds.Version = promo.Version
				}
				ds.DeployedAt = timestamp
				ds.DeployedBy = f.actor
			}
		}
	}

	// Update latest_release if publishing
	if f.promotionResult != nil && f.promotionResult.ReleaseAction == "publish" {
		if f.cicdFile.LatestRelease == nil {
			f.cicdFile.LatestRelease = &config.LatestReleaseState{}
		}
		f.cicdFile.LatestRelease.Version = f.promotionResult.ReleaseData.SemVersion
		f.cicdFile.LatestRelease.SHA = f.promotionResult.ReleaseData.SHA
		f.cicdFile.LatestRelease.ReleasedOn = timestamp
		f.cicdFile.LatestRelease.ReleasedBy = f.actor

		// Update "release" state marker (for multi-environment repos)
		// The "release" state marker tracks what version has been published
		if f.cicdFile.State["release"] == nil {
			f.cicdFile.State["release"] = &config.EnvState{}
		}
		f.cicdFile.State["release"].SHA = f.promotionResult.ReleaseData.SHA
		f.cicdFile.State["release"].Version = f.promotionResult.ReleaseData.SemVersion
		f.cicdFile.State["release"].CommittedAt = timestamp
		f.cicdFile.State["release"].CommittedBy = f.actor

		// Clear prerelease tracking state after final release is published
		// This prevents stale RC state from persisting in the manifest
		delete(f.cicdFile.State, "prerelease")
	}
}

// WriteConfig writes the updated manifest back to disk. It rewrites only the
// mutable state subtree of the on-disk manifest, so any configuration this binary
// does not model is preserved rather than dropped on the round-trip.
func (f *Finalizer) WriteConfig() error {
	// Get the manifest key from config (defaults to "ci")
	key := config.DefaultManifestKey
	if f.cicdFile.Config != nil && f.cicdFile.Config.ManifestKey != "" {
		key = f.cicdFile.Config.ManifestKey
	}

	current, err := os.ReadFile(f.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}
	data, err := f.serializeState(current, key)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := os.WriteFile(f.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	return nil
}

// serializeState rewrites current with this finalizer's owned state. In the
// single-component form (component == "") it reconciles the whole flat state node
// via WriteManifestState, byte-identical to the historical behavior. In the
// component-scoped form it node-patches only state.components.<component>.<env>
// (and latest_release.components.<component>) through WriteScopedState, so a
// sibling component present in current survives verbatim.
func (f *Finalizer) serializeState(current []byte, key string) ([]byte, error) {
	if f.component != "" {
		return config.WriteScopedState(current, key, f.componentStateWrites()...)
	}
	return config.WriteManifestState(current, key, f.cicdFile.State, f.cicdFile.LatestRelease)
}

// componentStateWrites builds the component-scoped writes this finalizer owns
// from its already-mutated in-memory state: one state directive per promoted env
// addressing state.components.<component>.<env>, plus a latest_release directive
// addressing latest_release.components.<component> on a publish. It is
// re-appliable: CommitWithRetry invokes it again against re-fetched trunk bytes
// on a 409, and each call deterministically re-derives the same owned leaves, so
// a concurrent sibling component's subtree is never rebuilt or dropped.
//
// The global "release"/"prerelease" markers that updateState maintains in the
// flat in-memory map are intentionally NOT emitted here: whether those markers
// become per-component or stay global is a release-pipeline decision, so this
// scopes the component write to the env ladder plus
// latest_release.components.<component>.
func (f *Finalizer) componentStateWrites() []config.StateWrite {
	if f.promotionResult == nil {
		return nil
	}
	writes := make([]config.StateWrite, 0, len(f.promotionResult.Promotions))
	for _, promo := range f.promotionResult.Promotions {
		// A promoted env is always populated in State by updateState. Guard against
		// a nil state so an unexpected miss never becomes an accidental node delete
		// (a nil State on a StateWrite means delete).
		st := f.cicdFile.State[promo.Environment]
		if st == nil {
			continue
		}
		writes = append(writes, config.StateWrite{
			Component: f.component,
			Env:       promo.Environment,
			State:     st,
		})
	}
	if f.promotionResult.ReleaseAction == "publish" {
		writes = append(writes, config.StateWrite{
			Component: f.component,
			Latest:    f.cicdFile.LatestRelease,
		})
	}
	return writes
}

// CommitAndPush persists the manifest changes back to the trunk branch.
//
// On real GitHub the write goes through the Contents REST API (via the gh CLI):
// API-created commits are signed by GitHub (shown as Verified) and, when made
// with a bypass-capable token, update the trunk even when a required status
// check protects the branch. In the act/gitea e2e environment there is no
// GitHub API, so the change is committed and pushed with plain git. The
// environment is detected exactly as the generated dispatch steps do, by
// GITHUB_SERVER_URL != https://github.com.
//
// Note: We skip git pull because the workflow just checked out the repo,
// so it should already be at the latest state. The finalize job runs after
// all deploy jobs complete, and they don't modify the manifest.
func (f *Finalizer) CommitAndPush() error {
	// Check for changes
	cmd := exec.Command("git", "status", "--porcelain", f.configPath)
	status, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}
	if len(status) == 0 {
		return nil // No changes
	}

	message := fmt.Sprintf("chore: update state after promotion to %s [skip ci]", f.targetEnv)

	if isRealGitHub() {
		return f.writeStateViaAPI(message)
	}
	return f.commitAndPushGit(message)
}

// isRealGitHub reports whether the workflow is running on github.com rather than
// an act/gitea e2e environment. This mirrors the "Only dispatch on real GitHub"
// detection used by the generated workflows.
func isRealGitHub() bool {
	server := os.Getenv("GITHUB_SERVER_URL")
	return server == "" || server == "https://github.com"
}

// writeStateViaAPI writes the manifest file to the trunk branch through the
// GitHub Contents REST API using the shared optimistic-lock retry loop. This
// produces a signed (Verified) commit and, with a bypass-capable token, can
// update a protected branch. The mutation re-parses whatever trunk bytes the
// loop fetches and overlays only this finalizer's owned env state, so two
// concurrent env finalizers merge rather than clobber each other on the file
// blob SHA.
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

func (f *Finalizer) writeStateViaAPI(message string) error {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return fmt.Errorf("GITHUB_REPOSITORY is not set; cannot write state via API")
	}
	branch := trunkBranchFromEnv()
	key := config.DefaultManifestKey
	if f.cicdFile.Config != nil && f.cicdFile.Config.ManifestKey != "" {
		key = f.cicdFile.Config.ManifestKey
	}
	client := f.contentsClient
	if client == nil {
		client = statewrite.NewGHClient()
	}
	return statewrite.CommitWithRetry(statewrite.Options{
		Client:  client,
		Repo:    repo,
		Path:    f.configPath,
		Ref:     branch,
		Message: message,
		Author:  gitIdentity(f.cicdFile.Config),
		Mutate:  f.stateMutation(key),
	})
}

// stateMutation returns the re-appliable CommitWithRetry closure that merges this
// finalizer's owned state onto whatever trunk bytes the retry loop fetches.
//
// Single-component form: re-parse the fetched bytes into a full manifest, overlay
// only the owned envs (overlayOwnedState), and reconcile the whole flat state node
// via WriteManifestState. Sibling envs survive because the re-read carries them
// into the typed map.
//
// Component-scoped form: node-patch only state.components.<component>.<env> (and
// latest_release.components.<component>) via WriteScopedState. It never
// deserializes or rebuilds a sibling component, so on a 409 the loser re-reads the
// winner's committed sibling subtree and re-applies only its own leaf, leaving the
// sibling verbatim, including keys this binary does not model.
func (f *Finalizer) stateMutation(key string) statewrite.Mutate {
	return func(current []byte) ([]byte, error) {
		if f.component != "" {
			data, err := config.WriteScopedState(current, key, f.componentStateWrites()...)
			if err != nil {
				return nil, fmt.Errorf("marshaling merged manifest: %w", err)
			}
			return data, nil
		}
		into, err := config.ParseManifestBytes(current, key)
		if err != nil {
			return nil, fmt.Errorf("parsing current manifest: %w", err)
		}
		f.overlayOwnedState(into)
		data, err := config.WriteManifestState(current, key, into.State, into.LatestRelease)
		if err != nil {
			return nil, fmt.Errorf("marshaling merged manifest: %w", err)
		}
		return data, nil
	}
}

// overlayOwnedState copies the state this finalizer owns from its in-memory,
// already-mutated manifest onto into, the freshly fetched trunk manifest. It
// overlays only the promoted envs (and, on a publish, the release marker and
// latest_release) so a concurrent finalizer's keys on into are preserved. It is
// re-appliable: CommitWithRetry calls it again against re-fetched trunk bytes on
// a 409, and each call deterministically re-derives the same owned keys.
//
// The publish transition deletes the prerelease marker from into.State by
// omission. That deletion is realized through WriteManifestState's
// reconcile-to-map contract: the wrapper rebuilds the state node from into.State
// alone and emits an explicit StateWrite delete (State == nil) for any env or
// marker key present in the fetched bytes but absent from the map, so the dropped
// prerelease key is removed rather than surviving as a stale node. The caller must
// therefore express a removal as a map deletion here, not rely on any whole-node
// replacement.
func (f *Finalizer) overlayOwnedState(into *config.CICDFile) {
	if into.State == nil {
		into.State = make(map[string]*config.EnvState)
	}
	if f.promotionResult != nil {
		for _, promo := range f.promotionResult.Promotions {
			into.State[promo.Environment] = f.cicdFile.State[promo.Environment]
		}
	}
	if f.promotionResult != nil && f.promotionResult.ReleaseAction == "publish" {
		into.LatestRelease = f.cicdFile.LatestRelease
		into.State["release"] = f.cicdFile.State["release"]
		// Removal by map deletion; the wrapper's reconcile-to-map contract turns
		// this into an explicit node delete (see the doc comment above).
		delete(into.State, "prerelease")
	}
}

// commitAndPushGit commits the manifest and pushes with plain git. Used in the
// act/gitea e2e environment, which enforces neither branch protection nor
// commit signatures.
func (f *Finalizer) commitAndPushGit(message string) error {
	// Configure git (use default bot identity)
	cmd := exec.Command("git", "config", "user.name", "github-actions[bot]")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git config user.name failed: %w", err)
	}

	cmd = exec.Command("git", "config", "user.email", "github-actions[bot]@users.noreply.github.com")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git config user.email failed: %w", err)
	}

	// Add the manifest file
	cmd = exec.Command("git", "add", f.configPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	cmd = exec.Command("git", "commit", "-m", message)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git commit failed: %w", err)
	}

	// Push HEAD explicitly to the trunk branch. The finalize job checks out the
	// triggering SHA, so on a workflow_dispatch run HEAD is detached and a bare
	// "git push" fails (exit 128) with no upstream tracking branch. Pushing
	// HEAD:refs/heads/<trunk> works regardless of detached state and targets the
	// branch the state belongs on. Capture combined output so the real git error
	// surfaces in the workflow log rather than just the exit status.
	branch := trunkBranchFromEnv()
	cmd = exec.Command("git", "push", "origin", "HEAD:refs/heads/"+branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}

// trunkBranchFromEnv resolves the branch to write state to. Push events check
// out in detached HEAD, so the branch is taken from GITHUB_REF when present,
// falling back to "main".
func trunkBranchFromEnv() string {
	ref := os.Getenv("GITHUB_REF")
	if strings.HasPrefix(ref, "refs/heads/") {
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	if ref != "" {
		return ref
	}
	return "main"
}

// isExternalDeploy checks if a deploy is an external deploy (from satellite repo)
func (f *Finalizer) isExternalDeploy(name string) bool {
	for _, ext := range f.cicdFile.Config.External {
		for _, d := range ext.Deploys {
			if d.Name == name {
				return true
			}
		}
	}
	return false
}

// getExternalDeployRepo returns the repo name for an external deploy
func (f *Finalizer) getExternalDeployRepo(name string) string {
	for _, ext := range f.cicdFile.Config.External {
		for _, d := range ext.Deploys {
			if d.Name == name {
				return ext.Repo
			}
		}
	}
	return ""
}

// updateExternalDeployState updates the state for an external deploy
func (f *Finalizer) updateExternalDeployState(name, timestamp string) {
	// For external deploys, get SHA from the source environment's external state
	// (the satellite notified us with this SHA via external-update)
	sha := f.getExternalDeploySHA(name)
	if sha == "" {
		return
	}

	if f.cicdFile.State[f.targetEnv] == nil {
		f.cicdFile.State[f.targetEnv] = &config.EnvState{}
	}
	if f.cicdFile.State[f.targetEnv].External == nil {
		f.cicdFile.State[f.targetEnv].External = make(map[string]*config.ExternalDeployState)
	}
	if f.cicdFile.State[f.targetEnv].External[name] == nil {
		f.cicdFile.State[f.targetEnv].External[name] = &config.ExternalDeployState{}
	}

	es := f.cicdFile.State[f.targetEnv].External[name]
	es.Repo = f.getExternalDeployRepo(name)
	es.SHA = sha
	// Carry the satellite-reported version forward alongside the SHA so the
	// external deployable's per-env state records which version is live, in
	// parity with internal per-deploy version tracking. Empty version (older
	// satellites that report SHA only) leaves the prior value untouched.
	if version := f.getExternalDeployVersion(name); version != "" {
		es.Version = version
	}
	es.DeployedAt = timestamp
	es.DeployedBy = f.actor
}

// getExternalDeployVersion retrieves the version for an external deploy from
// the source environment state (mirrors getExternalDeploySHA).
func (f *Finalizer) getExternalDeployVersion(name string) string {
	if f.promotionResult == nil || len(f.promotionResult.Promotions) == 0 {
		return ""
	}

	sourceEnv := f.promotionResult.Promotions[0].SourceEnv

	if state := f.cicdFile.State[sourceEnv]; state != nil {
		if es := state.External[name]; es != nil {
			return es.Version
		}
	}
	return ""
}

// getExternalDeploySHA retrieves the SHA for an external deploy from the source environment state
func (f *Finalizer) getExternalDeploySHA(name string) string {
	if f.promotionResult == nil || len(f.promotionResult.Promotions) == 0 {
		return ""
	}

	// Get source environment (first promotion's source)
	sourceEnv := f.promotionResult.Promotions[0].SourceEnv

	// Look up the external deploy's SHA in the source environment
	if state := f.cicdFile.State[sourceEnv]; state != nil {
		if es := state.External[name]; es != nil {
			return es.SHA
		}
	}
	return ""
}
