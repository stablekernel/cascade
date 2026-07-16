// Package rollback implements the `cascade rollback` command: explicit
// re-promotion of a prior version or SHA to a target environment.
//
// Rollback does not introduce a new deploy code path. It resolves a prior
// deployment target from existing state and then re-applies that target's
// SHA/version to the environment using the same state-write machinery the
// promote/finalize flow uses. Target resolution walks the environment's live
// state, then its deploy-history ring (state.<env>.previous, newest first),
// then the git history of the manifest.
package rollback

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/promote"
	"github.com/stablekernel/cascade/internal/statewrite"
)

// Target describes a resolved rollback destination: the SHA and version a
// re-promotion should re-apply, plus where it was found (for human output).
type Target struct {
	SHA     string `json:"sha"`
	Version string `json:"version,omitempty"`
	// Source explains how the target was resolved, e.g. "state" or
	// "git-history". Useful for operator-facing output and tests.
	Source string `json:"source"`
	// Deployable is set when the target was scoped to a single deployable.
	Deployable string `json:"deployable,omitempty"`
}

// Plan is the read-only result of resolving a rollback. It captures exactly
// what a subsequent Apply would write, without mutating any state.
type Plan struct {
	Environment string `json:"environment"`
	Target      Target `json:"target"`
	// Deployable, when non-empty, scopes the rollback to a single deployable's
	// per-deployable version state rather than the whole environment.
	Deployable string `json:"deployable,omitempty"`
	// CurrentSHA / CurrentVersion record the environment's state before the
	// rollback, for operator visibility and "rollback from" reporting.
	CurrentSHA     string `json:"current_sha,omitempty"`
	CurrentVersion string `json:"current_version,omitempty"`
	// NoOp is true when the environment (or deployable) is already at the
	// requested target; Apply becomes a no-op in that case.
	NoOp bool `json:"no_op"`
}

// Options configures a Rollbacker.
type Options struct {
	// ConfigPath is the manifest path. When empty, the caller is expected to
	// have resolved it (e.g. via config.FindConfigFile) before constructing.
	ConfigPath string
	// ManifestKey is the top-level manifest key (default: config.DefaultManifestKey).
	ManifestKey string
	// Actor is recorded as committed_by / deployed_by on the re-promotion.
	Actor string
	// Component, when non-empty, names the declared component this rollback is
	// scoped to. An empty value selects the single-component path, byte-identical
	// to today. When set, the rollback reads and records state under
	// state.components.<component>.<env>, resolves the deploy-history ring and
	// first-environment eligibility against the component's own environment
	// subset, and namespaces the divergence ref as rollback/<component>/<env>.
	Component string
	// HistoryReader resolves prior states from manifest git history. When nil,
	// a git-backed reader rooted at the manifest is used.
	HistoryReader HistoryReader
}

// HistoryReader yields prior environment states recorded in the manifest's
// history, newest first. It exists so rollback can recover a target the live
// manifest has already moved past, and so tests can supply deterministic
// history without a git repository.
type HistoryReader interface {
	// PriorStates returns historical EnvState snapshots for env, newest first,
	// excluding the current (HEAD) state. Implementations should return an
	// empty slice (not an error) when no history is available.
	PriorStates(env string) ([]*config.EnvState, error)
}

// Rollbacker resolves and applies rollbacks against a manifest.
type Rollbacker struct {
	configPath  string
	manifestKey string
	actor       string
	cicdFile    *config.CICDFile
	history     HistoryReader

	// component names the declared component this rollback is scoped to, or "" for
	// the single-component path. When set, state is read and recorded under
	// state.components.<component>.<env> via config.WriteScopedState, whose
	// node-patch preserves every sibling component verbatim under the
	// concurrent-finalize retry loop.
	component string
	// environments is the effective environment ladder eligibility and first-env
	// checks are judged against: the component's own subset when a component is
	// selected, otherwise the global ladder. It is empty when no config is parsed,
	// leaving the guards inert.
	environments []string
	// appliedEnv records the environment the most recent Apply mutated, so the
	// component-scoped state write and the trunk commit message address the right
	// leaf. It is set by Apply before any serialization.
	appliedEnv string
	// contentsClient overrides the GitHub Contents client used by the
	// component-scoped API write path. It is nil in production (the default gh-CLI
	// client is constructed on demand) and set only by tests to drive the
	// optimistic-lock retry against a fake that simulates a concurrent finalizer's
	// 409.
	contentsClient statewrite.ContentsClient
}

// New constructs a Rollbacker, loading and parsing the manifest.
func New(opts Options) (*Rollbacker, error) {
	key := opts.ManifestKey
	if key == "" {
		key = config.DefaultManifestKey
	}

	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = config.FindConfigFile("")
	}

	cicdFile, err := config.ParseManifestFile(configPath, key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	actor := opts.Actor
	if actor == "" {
		actor = getEnv("GITHUB_ACTOR", "github-actions[bot]")
	}

	history := opts.HistoryReader
	if history == nil {
		history = newGitHistoryReader(configPath, key, opts.Component)
	}

	// Resolve the effective environment ladder the eligibility and first-env guards
	// judge against. For a component it is the component's own subset (its override
	// or the inherited default); for the single-component path it is the global
	// ladder, so the guards behave byte-identically to before.
	var environments []string
	if cicdFile.Config != nil {
		environments = cicdFile.Config.EnvironmentNames()
		if opts.Component != "" {
			resolved, err := cicdFile.Config.ResolveComponent(opts.Component)
			if err != nil {
				return nil, fmt.Errorf("resolving component %q: %w", opts.Component, err)
			}
			environments = resolved.Config.EnvironmentNames()
		}
	}

	// Overlay the component's recorded per-env rows, read from
	// state.components.<component>.<env>, into the flat working state map so every
	// State[env] lookup (target resolution, the deploy-history ring, current state)
	// transparently sees that component's seed. It is a no-op for the empty
	// component, keeping the single-component path byte-identical. This mirrors the
	// promote/hotfix component-state overlay: the read counterpart to the
	// component-scoped WriteScopedState writes.
	if opts.Component != "" {
		raw, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("reading manifest for component state: %w", err)
		}
		compState, err := config.ReadComponentState(raw, key, opts.Component)
		if err != nil {
			return nil, fmt.Errorf("reading component %q state: %w", opts.Component, err)
		}
		if cicdFile.State == nil {
			cicdFile.State = make(map[string]*config.EnvState)
		}
		for env, st := range compState {
			cicdFile.State[env] = st
		}
	}

	return &Rollbacker{
		configPath:   configPath,
		manifestKey:  key,
		actor:        actor,
		cicdFile:     cicdFile,
		history:      history,
		component:    opts.Component,
		environments: environments,
	}, nil
}

// GitIdentity returns the commit identity for the post-rollback state write,
// taken from the manifest git config so an automated rollback commit is
// attributed to the configured bot rather than the token owner. An absent or
// empty git config resolves to the github-actions[bot] default.
func (r *Rollbacker) GitIdentity() statewrite.Identity {
	if r.cicdFile == nil || r.cicdFile.Config == nil {
		return statewrite.Identity{}
	}
	return statewrite.Identity{
		Name:  r.cicdFile.Config.GetGitUserName(),
		Email: r.cicdFile.Config.GetGitUserEmail(),
	}
}

// DeployNames returns the names of the deploys declared in the manifest, in
// declaration order. The finalize subcommand uses it to gate the state write on
// each deploy job's reported result. It returns nil when no deploys are
// configured (a state-only, deploy-less rollback).
func (r *Rollbacker) DeployNames() []string {
	if r.cicdFile.Config == nil {
		return nil
	}
	names := make([]string, 0, len(r.cicdFile.Config.Deploys))
	for _, d := range r.cicdFile.Config.Deploys {
		names = append(names, d.Name)
	}
	return names
}

// Plan resolves the rollback target for env and (optionally) a single
// deployable, without mutating any state. It returns a clear error when the
// environment is unknown or the requested target cannot be resolved.
//
// to is matched against both SHA and version. Matching is exact for full
// values and prefix-based for SHAs (so a short SHA resolves to the recorded
// full SHA). When to is empty, Plan resolves the previous version (the N-1
// entry in the deploy-history ring, or the newest distinct prior state from
// manifest history when the ring has no distinct entry).
func (r *Rollbacker) Plan(env, to, deployable string) (*Plan, error) {
	if env == "" {
		return nil, fmt.Errorf("rollback requires --env")
	}

	if !r.knownEnvironment(env) {
		return nil, fmt.Errorf("unknown environment %q (not declared in config.environments and has no recorded state)", env)
	}

	if err := r.firstEnvErr(env); err != nil {
		return nil, err
	}

	if deployable == "" {
		if err := r.hotfixDivergenceErr(env); err != nil {
			return nil, err
		}
	}

	current := r.cicdFile.State[env]
	plan := &Plan{
		Environment: env,
		Deployable:  deployable,
	}
	if current != nil {
		plan.CurrentSHA = current.SHA
		plan.CurrentVersion = current.Version
		if deployable != "" {
			if ds := current.Deploys[deployable]; ds != nil {
				plan.CurrentSHA = ds.SHA
				plan.CurrentVersion = ds.Version
			}
		}
	}

	if deployable != "" && !r.knownDeployable(deployable) {
		return nil, fmt.Errorf("unknown deployable %q (not declared in config.deploys and has no recorded state in %q)", deployable, env)
	}

	var target *Target
	var err error
	if to == "" {
		target, err = r.resolveDefaultTarget(env, deployable)
	} else {
		target, err = r.resolveTarget(env, to, deployable)
	}
	if err != nil {
		return nil, err
	}
	plan.Target = *target

	plan.NoOp = plan.CurrentSHA == target.SHA &&
		(target.Version == "" || plan.CurrentVersion == target.Version)

	return plan, nil
}

// resolveTarget finds a prior SHA/version matching `to`, searching live state
// first, the environment's deploy-history ring (state.<env>.previous) next, and
// manifest git history last. The ring is env-scoped only: snapshots carry no
// per-deployable data, so a deployable-scoped resolution skips it and falls
// straight through to git history.
func (r *Rollbacker) resolveTarget(env, to, deployable string) (*Target, error) {
	// 1. Live state for the environment (and the scoped deployable).
	if cur := r.cicdFile.State[env]; cur != nil {
		if deployable != "" {
			if ds := cur.Deploys[deployable]; ds != nil {
				if t := matchDeploy(ds, to, deployable, "state"); t != nil {
					return t, nil
				}
			}
		} else if t := matchEnv(cur, to, "state"); t != nil {
			return t, nil
		}
	}

	// 2. Deploy-history ring, newest first. Env-scoped only: snapshots have no
	//    per-deployable data, so a deployable-scoped rollback skips the ring.
	if deployable == "" {
		if cur := r.cicdFile.State[env]; cur != nil {
			for i := range cur.Previous {
				if t := matchSnapshot(cur.Previous[i], to); t != nil {
					return t, nil
				}
			}
		}
	}

	// 3. Manifest git history, newest first. This recovers a prior deployment
	//    the live manifest has already advanced past (the core rollback case).
	priors, err := r.history.PriorStates(env)
	if err != nil {
		return nil, fmt.Errorf("reading manifest history for %q: %w", env, err)
	}
	for _, prior := range priors {
		if prior == nil {
			continue
		}
		if deployable != "" {
			if ds := prior.Deploys[deployable]; ds != nil {
				if t := matchDeploy(ds, to, deployable, "git-history"); t != nil {
					return t, nil
				}
			}
			continue
		}
		if t := matchEnv(prior, to, "git-history"); t != nil {
			return t, nil
		}
	}

	scope := fmt.Sprintf("environment %q", env)
	if deployable != "" {
		scope = fmt.Sprintf("deployable %q in environment %q", deployable, env)
	}
	return nil, fmt.Errorf("rollback target %q not found for %s in current state or manifest history", to, scope)
}

// matchEnv returns a Target when `to` matches the env state's SHA or version.
func matchEnv(s *config.EnvState, to, source string) *Target {
	if s == nil {
		return nil
	}
	if shaMatches(s.SHA, to) || (s.Version != "" && s.Version == to) {
		return &Target{SHA: s.SHA, Version: s.Version, Source: source}
	}
	return nil
}

// matchDeploy returns a Target when `to` matches a per-deployable SHA/version.
// Version comes from the per-deployable Version field (#22); rollbacks scoped
// to a deployable re-apply that recorded version.
func matchDeploy(ds *config.DeployState, to, deployable, source string) *Target {
	if ds == nil {
		return nil
	}
	if shaMatches(ds.SHA, to) || (ds.Version != "" && ds.Version == to) {
		return &Target{SHA: ds.SHA, Version: ds.Version, Source: source, Deployable: deployable}
	}
	return nil
}

// matchSnapshot returns a Target when `to` matches a deploy-history ring
// snapshot's SHA (full or >=7-char prefix) or exact version. The ring is
// env-scoped, so the resulting Target carries no Deployable.
func matchSnapshot(snap config.EnvStateSnapshot, to string) *Target {
	if shaMatches(snap.SHA, to) || (snap.Version != "" && snap.Version == to) {
		return &Target{SHA: snap.SHA, Version: snap.Version, Source: "previous-ring"}
	}
	return nil
}

// resolveDefaultTarget resolves the implicit "previous version" target used when
// no --to is given. Env-scoped: it picks the newest deploy-history ring entry
// whose SHA differs from the current state (the N-1), falling back to the newest
// distinct git-history entry. Deployable-scoped: the ring is env-only, so it
// uses the newest git-history entry carrying a distinct per-deployable SHA.
func (r *Rollbacker) resolveDefaultTarget(env, deployable string) (*Target, error) {
	cur := r.cicdFile.State[env]
	currentSHA := ""
	if cur != nil {
		currentSHA = cur.SHA
		if deployable != "" {
			if ds := cur.Deploys[deployable]; ds != nil {
				currentSHA = ds.SHA
			}
		}
	}

	if deployable == "" {
		if cur != nil {
			for i := range cur.Previous {
				snap := cur.Previous[i]
				if snap.SHA != "" && snap.SHA != currentSHA {
					return &Target{SHA: snap.SHA, Version: snap.Version, Source: "previous-ring"}, nil
				}
			}
		}

		priors, err := r.history.PriorStates(env)
		if err != nil {
			return nil, fmt.Errorf("reading manifest history for %q: %w", env, err)
		}
		for _, prior := range priors {
			if prior == nil {
				continue
			}
			if prior.SHA != "" && prior.SHA != currentSHA {
				return &Target{SHA: prior.SHA, Version: prior.Version, Source: "git-history"}, nil
			}
		}

		return nil, fmt.Errorf("no prior version to roll back to for environment %q (deploy-history ring and manifest history are empty)", env)
	}

	// Deployable-scoped default: the ring carries no per-deployable data, so the
	// only source of a distinct prior per-deployable SHA is git history.
	priors, err := r.history.PriorStates(env)
	if err != nil {
		return nil, fmt.Errorf("reading manifest history for %q: %w", env, err)
	}
	for _, prior := range priors {
		if prior == nil {
			continue
		}
		ds := prior.Deploys[deployable]
		if ds != nil && ds.SHA != "" && ds.SHA != currentSHA {
			return &Target{SHA: ds.SHA, Version: ds.Version, Source: "git-history", Deployable: deployable}, nil
		}
	}

	return nil, fmt.Errorf("no prior version to roll back to for deployable %q in environment %q (deploy-history ring and manifest history are empty)", deployable, env)
}

// hotfixDivergenceErr refuses an environment-scoped rollback when env's
// recorded divergence did not originate from a rollback. A hotfix divergence
// record (ref env/<env> or env/<component>/<env>, plus base_sha and patches) is
// the only authorization for the rejoin teardown that deletes the integration
// branch and removes the hotfix tags and release objects; overwriting the ref
// with rollback/<env> makes the next rejoin skip that teardown as
// rollback-origin, stranding those artifacts with no later cleanup path. The
// rollback verb has no lifecycle cleaner of its own (Apply is a state-only
// write), so it cannot perform the teardown itself; the safe move is to refuse
// and direct the operator to rejoin first. An env already diverged BY a
// rollback carries no such record and may be rolled back again; a
// deployable-scoped rollback never touches the env-level divergence fields, so
// the guard does not apply to it.
func (r *Rollbacker) hotfixDivergenceErr(env string) error {
	state := r.cicdFile.State[env]
	if state == nil || !state.IsDiverged() || promote.IsRollbackRef(state.Ref) {
		return nil
	}
	return fmt.Errorf("environment %q is diverged from trunk by a hotfix (ref %q, %d recorded patch(es)); rolling it back would overwrite the divergence record that authorizes the hotfix teardown, stranding its integration branch, tags, and release objects. Rejoin the environment first by promoting a trunk commit that contains every recorded patch (the rejoin tears the hotfix artifacts down), then roll back if still needed. If the patches never merged to trunk (an abandoned hotfix), promote with --force instead: containment is skipped with a recorded warning and the rejoin teardown still runs",
		env, state.Ref, len(state.Patches))
}

// shaMatches reports whether candidate matches the requested value exactly or
// as a SHA prefix (min 7 chars, the conventional short-SHA length).
func shaMatches(candidate, requested string) bool {
	if candidate == "" || requested == "" {
		return false
	}
	if candidate == requested {
		return true
	}
	if len(requested) >= 7 && strings.HasPrefix(candidate, requested) {
		return true
	}
	return false
}

// Apply writes the resolved plan into the manifest and persists it, recording
// the actor. It is a no-op when plan.NoOp is true. Apply is the only mutating
// operation; Plan never writes.
func (r *Rollbacker) Apply(plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("nil plan")
	}
	if err := r.firstEnvErr(plan.Environment); err != nil {
		return err
	}
	if plan.NoOp {
		return nil
	}

	// Record the env this Apply mutates so the component-scoped state write and the
	// trunk commit message address the right leaf.
	r.appliedEnv = plan.Environment

	timestamp := time.Now().UTC().Format(time.RFC3339)

	if r.cicdFile.State == nil {
		r.cicdFile.State = make(map[string]*config.EnvState)
	}
	if r.cicdFile.State[plan.Environment] == nil {
		r.cicdFile.State[plan.Environment] = &config.EnvState{}
	}
	env := r.cicdFile.State[plan.Environment]

	if plan.Deployable != "" {
		// Deployable-scoped rollback: re-apply the recorded per-deployable
		// SHA/version without touching the env-level pointer.
		if env.Deploys == nil {
			env.Deploys = make(map[string]*config.DeployState)
		}
		if env.Deploys[plan.Deployable] == nil {
			env.Deploys[plan.Deployable] = &config.DeployState{}
		}
		ds := env.Deploys[plan.Deployable]
		ds.SHA = plan.Target.SHA
		ds.Version = plan.Target.Version
		ds.DeployedAt = timestamp
		ds.DeployedBy = r.actor
	} else {
		// Environment-scoped rollback: re-apply the env pointer and mirror the
		// SHA onto every recorded deployable so change-detection compares
		// against the rolled-back base.
		//
		// Re-check the hotfix-divergence guard at the mutation site: Plan is the
		// normal gate, but Apply is the only writer, so a hand-built or stale
		// Plan (for example one resolved before a concurrent hotfix finalize
		// recorded the divergence) must not overwrite a hotfix divergence record
		// either.
		if err := r.hotfixDivergenceErr(plan.Environment); err != nil {
			return err
		}

		// Capture the outgoing (pre-rollback) SHA before any field is mutated;
		// it becomes the divergence base recorded below.
		prevSHA := env.SHA

		// Record the outgoing state in the deploy-history ring before the env
		// pointer advances. No-op when there is no prior SHA or the rollback
		// target equals the current SHA.
		env.PushPreviousSnapshot(plan.Target.SHA)
		env.SHA = plan.Target.SHA
		env.Version = plan.Target.Version
		env.CommittedAt = timestamp
		env.CommittedBy = r.actor
		for _, ds := range env.Deploys {
			if ds == nil {
				continue
			}
			ds.SHA = plan.Target.SHA
			ds.Version = plan.Target.Version
			ds.DeployedAt = timestamp
			ds.DeployedBy = r.actor
		}

		// Mark the environment diverged so forward-promotion guards treat it as
		// off-trunk until a promotion rejoins it. The rollback ref distinguishes
		// this from a hotfix divergence (no integration branch, tags, or drafts),
		// so the rejoin cleanup can skip the hotfix-specific teardown. Patches
		// are cleared, not merely left unset: they assert fix commits deployed
		// in the env, and the rollback just re-pointed the env at a prior SHA
		// where no such assertion holds. The guard above means a legal caller
		// never reaches here with patches set (a hotfix divergence is refused),
		// so the clear is a self-healing invariant for irregular state rather
		// than a lifecycle-record overwrite. The ref is namespaced to the
		// component when one is selected (rollback/<component>/<env>), mirroring
		// the hotfix env-branch namespacing, and is byte-identical
		// (rollback/<env>) otherwise.
		env.Ref = promote.RollbackRef(r.component, plan.Environment)
		env.BaseSHA = prevSHA
		env.Patches = nil
	}

	return r.writeConfig()
}

// writeConfig writes the manifest back to disk, rewriting only the mutable state
// subtree so any configuration this binary does not model is preserved rather
// than dropped on the round-trip. It matches the promote/finalize write path.
func (r *Rollbacker) writeConfig() error {
	key := r.manifestKey
	if key == "" {
		key = config.DefaultManifestKey
	}
	if r.cicdFile.Config != nil && r.cicdFile.Config.ManifestKey != "" {
		key = r.cicdFile.Config.ManifestKey
	}

	current, err := os.ReadFile(r.configPath)
	if err != nil {
		return fmt.Errorf("failed to read manifest: %w", err)
	}
	data, err := r.serializeState(current, key)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}
	if err := os.WriteFile(r.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}
	return nil
}

// serializeState rewrites current with this rollback's owned state. In the
// single-component form (component == "") it reconciles the whole flat state node
// via WriteManifestState, byte-identical to the historical behavior. In the
// component-scoped form it node-patches only state.components.<component>.<env>
// through WriteScopedState, so a sibling component present in current survives
// verbatim.
func (r *Rollbacker) serializeState(current []byte, key string) ([]byte, error) {
	if r.component != "" {
		return config.WriteScopedState(current, key, r.ownedStateWrites()...)
	}
	return config.WriteManifestState(current, key, r.cicdFile.State, r.cicdFile.LatestRelease)
}

// ownedStateWrites builds the component-scoped write this rollback owns from its
// already-mutated in-memory state: a single directive addressing
// state.components.<component>.<appliedEnv>. It is re-appliable, so
// CommitWithRetry invokes it again against re-fetched trunk bytes on a 409 and
// deterministically re-derives the same owned leaf, leaving a concurrent sibling
// component's subtree untouched. It returns nil when no env has been applied or
// its state is unexpectedly absent, so an empty write never becomes an accidental
// node delete.
func (r *Rollbacker) ownedStateWrites() []config.StateWrite {
	if r.appliedEnv == "" {
		return nil
	}
	st := r.cicdFile.State[r.appliedEnv]
	if st == nil {
		return nil
	}
	return []config.StateWrite{{Component: r.component, Env: r.appliedEnv, State: st}}
}

// stateMutation returns the re-appliable CommitWithRetry closure that merges
// this rollback's owned state onto whatever trunk bytes the retry loop fetches.
//
// Component-scoped form: node-patch only state.components.<component>.<appliedEnv>
// via WriteScopedState. It never deserializes or rebuilds a sibling component, so
// on a 409 the loser re-reads the winner's committed sibling subtree and
// re-applies only its own leaf, leaving the sibling verbatim, including keys this
// binary does not model.
//
// Single-component form: re-parse the fetched bytes into a full manifest, overlay
// only the applied env (overlayOwnedState), and reconcile the flat state node via
// WriteManifestState. Sibling envs and latest_release survive because the re-read
// carries them into the typed map, mirroring the promote finalizer.
func (r *Rollbacker) stateMutation(key string) statewrite.Mutate {
	return func(current []byte) ([]byte, error) {
		if r.component != "" {
			data, err := config.WriteScopedState(current, key, r.ownedStateWrites()...)
			if err != nil {
				return nil, fmt.Errorf("marshaling merged manifest: %w", err)
			}
			return data, nil
		}
		into, err := config.ParseManifestBytes(current, key)
		if err != nil {
			return nil, fmt.Errorf("parsing current manifest: %w", err)
		}
		r.overlayOwnedState(into)
		data, err := config.WriteManifestState(current, key, into.State, into.LatestRelease)
		if err != nil {
			return nil, fmt.Errorf("marshaling merged manifest: %w", err)
		}
		return data, nil
	}
}

// overlayOwnedState copies the one env state this rollback owns (the applied
// env) from its already-mutated in-memory manifest onto into, the freshly
// fetched trunk manifest. Every sibling env and latest_release stay exactly as
// fetched, so a concurrent writer's committed keys are preserved when the
// mutation is re-applied after a 409. A missing applied env or state is a no-op
// rather than an accidental delete.
func (r *Rollbacker) overlayOwnedState(into *config.CICDFile) {
	if r.appliedEnv == "" {
		return
	}
	st := r.cicdFile.State[r.appliedEnv]
	if st == nil {
		return
	}
	if into.State == nil {
		into.State = make(map[string]*config.EnvState)
	}
	into.State[r.appliedEnv] = st
}

// resolvedManifestKey returns the manifest key state writes address, honoring an
// explicit config override and falling back to the configured default.
func (r *Rollbacker) resolvedManifestKey() string {
	key := r.manifestKey
	if key == "" {
		key = config.DefaultManifestKey
	}
	if r.cicdFile.Config != nil && r.cicdFile.Config.ManifestKey != "" {
		key = r.cicdFile.Config.ManifestKey
	}
	return key
}

// CommitAndPush persists the post-rollback manifest back to the trunk branch.
// On real GitHub both the single-component and the component-scoped paths go
// through the shared statewrite.CommitWithRetry optimistic-lock retry, so a
// rollback finalize racing any concurrent state writer merges rather than
// clobbers (or spuriously aborts) on the file blob SHA: the mutation re-applies
// only this rollback's owned leaf over the re-fetched trunk bytes. In the
// act/gitea environment the on-disk file is committed with plain git.
func (r *Rollbacker) CommitAndPush() error {
	status, err := exec.Command("git", "status", "--porcelain", r.configPath).Output()
	if err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}
	if len(status) == 0 {
		return nil // No changes
	}

	message := fmt.Sprintf("chore: update state after rollback of %s [skip ci]", r.appliedEnv)
	if isRealGitHub() {
		return r.writeStateViaAPI(message)
	}
	return commitAndPushGit(r.configPath, message, r.GitIdentity())
}

// writeStateViaAPI writes the manifest to the trunk branch through the GitHub
// Contents REST API using the shared optimistic-lock retry loop, re-applying
// only this rollback's owned leaf so a concurrent writer's committed state is
// preserved rather than clobbered. The Contents client is injectable so the
// 409 re-apply can be exercised without a live API.
func (r *Rollbacker) writeStateViaAPI(message string) error {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return fmt.Errorf("GITHUB_REPOSITORY is not set; cannot write state via API")
	}
	client := r.contentsClient
	if client == nil {
		client = statewrite.NewGHClient()
	}
	return statewrite.CommitWithRetry(statewrite.Options{
		Client:  client,
		Repo:    repo,
		Path:    r.configPath,
		Ref:     trunkBranchFromEnv(),
		Message: message,
		Author:  r.GitIdentity(),
		Mutate:  r.stateMutation(r.resolvedManifestKey()),
	})
}

// firstEnvErr returns a guard error when env is the first (build target)
// environment and nil otherwise. The first environment tracks trunk and is
// never promoted into, so its deploy-history ring is structurally always empty:
// a rollback there has no recorded prior target and would resolve a silent wrong
// one from an empty or stale ring. The trunk-native undo for the first
// environment is a revert merge to the trunk branch, not a rollback. The guard
// is inert when no parsed config is available to identify the first environment,
// so a state-only manifest still resolves through the normal path.
//
// Eligibility is judged against the effective ladder: the selected component's own
// environment subset when a component is set, otherwise the global ladder. A
// component whose ladder starts at a different environment than the global build
// target is thus judged on its own first environment, not the global one. The
// guard is inert when no ladder is available (a state-only manifest), so it
// resolves through the normal path.
func (r *Rollbacker) firstEnvErr(env string) error {
	if len(r.environments) == 0 {
		return nil
	}
	if r.environments[0] == env {
		return fmt.Errorf("environment %q is the first environment; it tracks trunk and is never promoted into, so it has no rollback history. Revert it with a merge to the trunk branch instead of a rollback", env)
	}
	return nil
}

// knownEnvironment reports whether env is in the effective environment ladder (the
// selected component's subset, or the global ladder for the single-component path)
// or has recorded state.
func (r *Rollbacker) knownEnvironment(env string) bool {
	if r.cicdFile.State[env] != nil {
		return true
	}
	for _, e := range r.environments {
		if e == env {
			return true
		}
	}
	return false
}

// knownDeployable reports whether the named deployable is declared in
// config.deploys or has recorded state in any environment.
func (r *Rollbacker) knownDeployable(name string) bool {
	if r.cicdFile.Config != nil {
		for _, d := range r.cicdFile.Config.Deploys {
			if d.Name == name {
				return true
			}
		}
	}
	for _, st := range r.cicdFile.State {
		if st != nil && st.Deploys[name] != nil {
			return true
		}
	}
	return false
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
