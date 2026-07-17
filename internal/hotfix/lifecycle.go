package hotfix

import (
	"fmt"
	"slices"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/taggrammar"
	"github.com/stablekernel/cascade/internal/version"
)

// resolveTagGrammar folds a manifest into its tag grammar, falling back to the
// historical default when the manifest or its config is absent so callers never
// dereference a nil config.
func resolveTagGrammar(f *config.CICDFile) taggrammar.Spec {
	if f == nil || f.Config == nil {
		return taggrammar.Default()
	}
	return f.Config.ResolveTagGrammar()
}

// resolveFinalizeSpec returns the tag grammar a hotfix finalize reads and emits
// its versions and tags under. For the default (empty) component it is the
// manifest's permissive grammar, byte-identical to the single-component
// behavior. For a named component it is that component's resolved grammar with a
// strict prefix, so a hotfix version and tag land in and are looked up from the
// component's own namespace and never cross-match a sibling component's tags.
func resolveFinalizeSpec(f *config.CICDFile, component string) (taggrammar.Spec, error) {
	if component == "" {
		return resolveTagGrammar(f), nil
	}
	if f == nil || f.Config == nil {
		return taggrammar.Spec{}, fmt.Errorf("component %q requested but manifest has no config block", component)
	}
	resolved, err := f.Config.ResolveComponent(component)
	if err != nil {
		return taggrammar.Spec{}, fmt.Errorf("resolving component %q tag grammar: %w", component, err)
	}
	return resolved.TagGrammarSpec(), nil
}

// resolveEnvLadder returns the environment ladder a hotfix validates its target
// against and elevates along, as an ordered list of environment names. For the
// default (empty) component it is the manifest's global ladder, byte-identical
// to the single-component behavior. For a named component it is that component's
// resolved ladder: `environments` is an inheritable per-component override, so a
// component may declare a subset of the global ladder (or omit the block and
// inherit it whole).
//
// The generator emits a component's hotfix workflow from that component's
// resolved config, so the workflow's target_env choices, its apply lane, and
// even whether the workflow exists at all are decided by the resolved ladder. A
// runtime reading the global ladder therefore disagrees with the workflow it is
// driving: it accepts a target the workflow never offered, and it elevates
// through global-only environments the component has no state for.
//
// This is deliberately a ladder resolver rather than a wholesale config swap
// like the promote, rollback, and orchestrate runtimes perform. Both hotfix
// verbs resolve the component out of the working config a second time (via
// resolveFinalizeSpec), and ResolveComponent clears Components on the config it
// returns, so a swapped-in resolved config could not be resolved from again.
//
// An undeclared component is refused rather than silently falling back to the
// global ladder, matching resolveFinalizeSpec: a hotfix elevates along a
// component's resolved ladder, and a component the config does not declare has
// none.
func resolveEnvLadder(f *config.CICDFile, component string) ([]string, error) {
	if f == nil || f.Config == nil {
		return nil, fmt.Errorf("manifest has no config block")
	}
	if component == "" {
		return f.Config.EnvironmentNames(), nil
	}
	resolved, err := f.Config.ResolveComponent(component)
	if err != nil {
		return nil, fmt.Errorf("resolving component %q environments: %w", component, err)
	}
	return resolved.Config.EnvironmentNames(), nil
}

// ladderIndex returns the position of env in ladder, or -1 when the ladder does
// not carry it. It is the ladder-slice counterpart of
// config.TrunkConfig.GetEnvironmentIndex, which a component-scoped caller cannot
// use because it always reads the global ladder.
func ladderIndex(ladder []string, env string) int {
	return slices.Index(ladder, env)
}

// isFirstLadderEnv reports whether env is the first entry of ladder. It mirrors
// config.TrunkConfig.IsFirstEnvironment, including its false-on-empty behavior,
// against a resolved ladder rather than the global one. The first environment is
// the one a fix reaches by merging to trunk, so it is never a hotfix target, and
// which environment holds that position is a per-component fact.
func isFirstLadderEnv(ladder []string, env string) bool {
	return len(ladder) > 0 && ladder[0] == env
}

// EnvBranchPrefix is the prefix of the per-environment integration branches a
// hotfix creates (for example env/test). A branch carrying this prefix exists
// only while its environment is diverged; once the env rejoins trunk the branch
// is deleted.
const EnvBranchPrefix = "env/"

// EnvBranchName returns the integration branch name for env within component.
// The default (empty) component yields env/<env>, byte-identical to the
// historical single-component form; a named component yields
// env/<component>/<env> so each component's integration branches occupy a
// disjoint namespace and a hotfix on one component can never touch another's.
func EnvBranchName(component, env string) string {
	if component == "" {
		return EnvBranchPrefix + env
	}
	return EnvBranchPrefix + component + "/" + env
}

// ParseEnvBranch splits an integration branch name into its component and env,
// the inverse of EnvBranchName. It reports ok=false for any branch that does not
// carry EnvBranchPrefix or whose remainder is not a well-formed env/<env> or
// env/<component>/<env>.
//
// env/<env> parses to an empty component and <env>, preserving the historical
// single-component reading. env/<component>/<env> parses to <component> and
// <env>. The segment count after the prefix disambiguates the two forms, so a
// nested branch never has its component folded into the env: env/web/staging
// parses to component "web" and env "staging", not the naive
// strings.TrimPrefix("env/") result of env "web/staging". A bare prefix or a
// more deeply nested name is malformed and reports ok=false.
func ParseEnvBranch(branch string) (component, env string, ok bool) {
	rest, found := strings.CutPrefix(branch, EnvBranchPrefix)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return "", "", false
		}
		return "", parts[0], true
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

// OrphanEnvBranches returns the integration branches in branches that belong to
// component and have no matching divergence in state. state is component's own
// env subtree (state.components.<component>.<env>), and for the default empty
// component it is the historical env-keyed state map. A branch is healthy only
// while state[<env>] reports IsDiverged(); a branch with no diverged env behind
// it is an orphan left over from an interrupted hotfix or manual meddling and is
// flagged.
//
// Only branches whose parsed component equals component are considered, so a
// component never inspects, and never flags, a sibling's env/<other>/<env>
// branch, and the default component ignores every component-nested branch. Non
// env/* branches are ignored. The returned slice preserves the input order and
// is nil when nothing is orphaned, so callers can treat a nil result as
// "consistent".
func OrphanEnvBranches(component string, branches []string, state map[string]*config.EnvState) []string {
	var orphans []string
	for _, branch := range branches {
		comp, env, ok := ParseEnvBranch(branch)
		if !ok || comp != component {
			continue
		}
		st := state[env]
		if st != nil && st.IsDiverged() {
			continue
		}
		orphans = append(orphans, branch)
	}
	return orphans
}

// HealOrphanEnvBranches deletes every integration branch that OrphanEnvBranches
// flags for component as having no matching divergence, calling del to remove
// each branch on remote. In production del is git.DeleteRemoteBranch, whose
// delete of an absent branch is a no-op success, so HealOrphanEnvBranches is
// idempotent: re-running it, or running it against an orphan that is already
// gone, deletes nothing further and never errors.
//
// Only orphans in component's own namespace are deleted. A branch backing a
// diverged environment, and every sibling component's branch, is never touched,
// because the deletion set comes from OrphanEnvBranches, which excludes them by
// the component filter and the same IsDiverged() predicate the hotfix preflight
// and status consistency classify on. The returned slice lists the branches
// deleted, in input order, and is nil when nothing was orphaned. On the first
// delete error the heal stops and returns that error with a nil healed slice.
func HealOrphanEnvBranches(component string, branches []string, state map[string]*config.EnvState, remote string, del func(remote, branch string) error) ([]string, error) {
	var healed []string
	for _, branch := range OrphanEnvBranches(component, branches, state) {
		if err := del(remote, branch); err != nil {
			return nil, fmt.Errorf("deleting orphan branch %s on %s: %w", branch, remote, err)
		}
		healed = append(healed, branch)
	}
	return healed, nil
}

// HotfixTagsForBase returns the hotfix tags in tags that belong to the
// pre-release base of baseVersion under spec. A hotfix tag has the dotted shape
// <base>-<token><sep>N.hotfix.M (for example vX.Y.Z-rc.N.hotfix.M); it shares the
// pre-release base of the version the environment held while diverged. The
// pre-release-shaped cleanup in internal/release deliberately cannot see these
// tags, so divergence-end cleanup must collect them explicitly.
//
// baseVersion may itself be a hotfix version; it is normalized to its
// pre-release base before matching. Parsing uses spec so a custom pre-release
// token still resolves. Tags that do not parse, are not hotfix tags, or belong to
// a different pre-release base are excluded. The result is nil when nothing
// matches.
func HotfixTagsForBase(spec taggrammar.Spec, baseVersion string, tags []string) []string {
	base, err := version.ParseWithGrammar(spec, baseVersion)
	if err != nil || base.PreRelease < 0 {
		return nil
	}
	base = base.WithGrammar(spec)
	// Normalize to the rc base so a hotfix baseVersion matches its siblings.
	rcBase := base.WithRC(base.PreRelease).String()

	var matched []string
	for _, tag := range tags {
		v, err := version.ParseWithGrammar(spec, tag)
		if err != nil || v.Hotfix < 0 {
			continue
		}
		v = v.WithGrammar(spec)
		if v.WithRC(v.PreRelease).String() == rcBase {
			matched = append(matched, tag)
		}
	}
	return matched
}
