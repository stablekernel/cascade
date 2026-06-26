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
	"strings"
	"time"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/promote"
	"github.com/stablekernel/cascade/internal/statewrite"
	"gopkg.in/yaml.v3"
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
		history = newGitHistoryReader(configPath, key)
	}

	return &Rollbacker{
		configPath:  configPath,
		manifestKey: key,
		actor:       actor,
		cicdFile:    cicdFile,
		history:     history,
	}, nil
}

// ConfigPath returns the resolved manifest path the Rollbacker reads and writes.
// The finalize subcommand uses it to commit the post-rollback state back to the
// trunk branch.
func (r *Rollbacker) ConfigPath() string {
	return r.configPath
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
	if plan.NoOp {
		return nil
	}

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
		// so the rejoin cleanup can skip the hotfix-specific teardown. No patches
		// are recorded: a rollback re-points at a prior SHA, it does not stack
		// commits on a base.
		env.Ref = promote.RollbackRefPrefix + plan.Environment
		env.BaseSHA = prevSHA
	}

	return r.writeConfig()
}

// writeConfig marshals the manifest back to disk, wrapped in the manifest key,
// matching the promote/finalize write path.
func (r *Rollbacker) writeConfig() error {
	key := r.manifestKey
	if key == "" {
		key = config.DefaultManifestKey
	}
	if r.cicdFile.Config != nil && r.cicdFile.Config.ManifestKey != "" {
		key = r.cicdFile.Config.ManifestKey
	}

	wrapper := map[string]any{key: r.cicdFile}
	data, err := yaml.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}
	if err := os.WriteFile(r.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}
	return nil
}

// knownEnvironment reports whether env is declared in config.environments or
// has recorded state.
func (r *Rollbacker) knownEnvironment(env string) bool {
	if r.cicdFile.State[env] != nil {
		return true
	}
	if r.cicdFile.Config != nil {
		for _, e := range r.cicdFile.Config.Environments {
			if e == env {
				return true
			}
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
