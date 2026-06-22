package hotfix

import (
	"fmt"
	"slices"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
)

// PlanChainResult is the computed, validated plan for elevating a set of trunk
// commits across the bottom-up environment chain up to and including a target
// environment. It is the multi-commit, multi-env companion to PlanResult and is
// additive: the single-env Plan and PlanResult are unchanged.
type PlanChainResult struct {
	// Envs are the per-environment plans in bottom-up chain order (the first
	// environment is excluded; the target environment is the last entry).
	Envs []EnvPlan `json:"envs"`
}

// EnvPlan is the plan for one environment in the chain: the env branch, its
// recorded base SHA, and the ordered list of commits still to apply after
// per-(commit,env) idempotency skips. NoOp is true when every requested commit
// is already present in that environment.
type EnvPlan struct {
	Env     string `json:"env"`
	Branch  string `json:"branch"`
	BaseSHA string `json:"base_sha"`

	// Commits are the fix SHAs still to cherry-pick into this environment, in
	// the caller's requested ref order, after skipping commits already present.
	Commits []string `json:"commits"`

	// NoOp is true when the whole requested set is already present in this env.
	NoOp bool `json:"no_op"`

	// ConflictExpected hints whether a cherry-pick is likely to conflict. The
	// plan verb does not run the cherry-pick, so this is best-effort and false
	// by default; the workflow is authoritative.
	ConflictExpected bool `json:"conflict_expected"`
}

// parseCommitRefs splits a comma-delimited commit-ref input into an ordered
// slice. It trims surrounding whitespace on each entry, rejects empty input and
// any empty or duplicate entry, and preserves order. A single ref returns a
// one-element slice, keeping the single-commit call site back-compatible.
func parseCommitRefs(input string) ([]string, error) {
	if strings.TrimSpace(input) == "" {
		return nil, fmt.Errorf("no commit refs given")
	}

	parts := strings.Split(input, ",")
	refs := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, raw := range parts {
		ref := strings.TrimSpace(raw)
		if ref == "" {
			return nil, fmt.Errorf("empty commit ref in %q", input)
		}
		if _, dup := seen[ref]; dup {
			return nil, fmt.Errorf("duplicate commit ref %q", ref)
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs, nil
}

// expandTargetEnvs returns the bottom-up environment sequence to elevate
// through, from the second environment up to and including targetEnv. The first
// environment is excluded (a fix reaches it by merging to trunk) and any
// environment above targetEnv is excluded. It rejects an unknown target and the
// first environment.
func expandTargetEnvs(cfg *config.TrunkConfig, targetEnv string) ([]string, error) {
	idx := cfg.GetEnvironmentIndex(targetEnv)
	if idx == -1 {
		return nil, fmt.Errorf("%q is not a configured environment", targetEnv)
	}
	if cfg.IsFirstEnvironment(targetEnv) {
		return nil, fmt.Errorf("%q is the first environment; a fix reaches it by merging to trunk, not by hotfix", targetEnv)
	}
	// Environments[1..idx] inclusive: skip the first env, stop at the target.
	seq := make([]string, 0, idx)
	seq = append(seq, cfg.Environments[1:idx+1]...)
	return seq, nil
}

// resolveTrunkAncestors resolves each ref to a full SHA and verifies it is an
// ancestor of trunk tip, preserving order. The first ref that fails to resolve
// or is not on trunk aborts with an error naming that ref, so a mixed set
// surfaces the offending commit. The returned slice is aligned with refs.
func (p *Planner) resolveTrunkAncestors(refs []string) ([]string, error) {
	trunkSHA, err := p.gitRunner.ResolveSHA("HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolving trunk tip: %w", err)
	}

	shas := make([]string, 0, len(refs))
	for _, ref := range refs {
		sha, err := p.gitRunner.ResolveSHA(ref)
		if err != nil {
			return nil, fmt.Errorf("resolving fix commit %q: %w", ref, err)
		}
		onTrunk, err := git.IsAncestor(sha, trunkSHA)
		if err != nil {
			return nil, fmt.Errorf("checking trunk ancestry of %q: %w", ref, err)
		}
		if !onTrunk {
			return nil, fmt.Errorf("commit %s (%s) is not on trunk: a hotfix must apply a commit that is already an ancestor of trunk; merge it to trunk first", short(sha), ref)
		}
		shas = append(shas, sha)
	}
	return shas, nil
}

// commitPresentInEnv reports whether fixSHA is already present in an environment
// whose recorded state SHA is baseSHA and whose applied-patch list is patches. A
// commit counts as present when it is an ancestor of the state SHA OR it is
// already recorded in the patch list. The patch-list check is what makes
// re-elevation idempotent across the diverged env branch.
func commitPresentInEnv(fixSHA, baseSHA string, patches []string) (bool, error) {
	if slices.Contains(patches, fixSHA) {
		return true, nil
	}
	already, err := git.IsAncestor(fixSHA, baseSHA)
	if err != nil {
		return false, err
	}
	return already, nil
}

// PlanChain validates and computes the per-environment plan for elevating a set
// of trunk commits across the bottom-up environment chain up to and including
// targetEnv. Commits are kept in the caller's ref order; environments run
// bottom-up. Each ref must resolve and be a trunk ancestor; each (commit, env)
// pair is skipped when the commit is already present in that environment (an
// ancestor of its state SHA or already in its patch list). An environment whose
// whole requested set is already present is reported as a no-op.
//
// PlanChain is additive: it does not create branches or mutate state, and the
// single-env Plan and PlanResult are unchanged.
func (p *Planner) PlanChain(refs []string, targetEnv string) (*PlanChainResult, error) {
	cfg := p.cicd.Config
	if cfg == nil {
		return nil, fmt.Errorf("manifest has no config block")
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("no commit refs given")
	}

	// Per-ref trunk-ancestry gate over the whole set, order preserved.
	shas, err := p.resolveTrunkAncestors(refs)
	if err != nil {
		return nil, err
	}

	// Bottom-up environment sequence (excludes the first env, stops at target).
	envs, err := expandTargetEnvs(cfg, targetEnv)
	if err != nil {
		return nil, err
	}

	result := &PlanChainResult{Envs: make([]EnvPlan, 0, len(envs))}
	for _, env := range envs {
		state := p.cicd.State[env]
		if state == nil || state.SHA == "" {
			return nil, fmt.Errorf("environment %q has no recorded state SHA", env)
		}
		baseSHA := state.SHA

		ep := EnvPlan{
			Env:     env,
			Branch:  envBranch(env),
			BaseSHA: baseSHA,
			Commits: make([]string, 0, len(shas)),
		}
		for _, sha := range shas {
			present, err := commitPresentInEnv(sha, baseSHA, state.Patches)
			if err != nil {
				return nil, fmt.Errorf("checking whether commit is already in %q: %w", env, err)
			}
			if present {
				continue
			}
			ep.Commits = append(ep.Commits, sha)
		}
		ep.NoOp = len(ep.Commits) == 0
		result.Envs = append(result.Envs, ep)
	}
	return result, nil
}
