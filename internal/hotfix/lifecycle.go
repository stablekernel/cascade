package hotfix

import (
	"fmt"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/version"
)

// EnvBranchPrefix is the prefix of the per-environment integration branches a
// hotfix creates (for example env/test). A branch carrying this prefix exists
// only while its environment is diverged; once the env rejoins trunk the branch
// is deleted.
const EnvBranchPrefix = "env/"

// OrphanEnvBranches returns the env/* branches in branches that have no matching
// divergence in state. A branch env/<name> is healthy only while state[<name>]
// reports IsDiverged(); a branch with no diverged env behind it is an orphan
// left over from an interrupted hotfix or manual meddling and should be flagged.
//
// Non env/* branches are ignored. The returned slice preserves the input order
// and is nil when nothing is orphaned, so callers can treat a nil result as
// "consistent".
func OrphanEnvBranches(branches []string, state map[string]*config.EnvState) []string {
	var orphans []string
	for _, branch := range branches {
		if !strings.HasPrefix(branch, EnvBranchPrefix) {
			continue
		}
		env := strings.TrimPrefix(branch, EnvBranchPrefix)
		st := state[env]
		if st != nil && st.IsDiverged() {
			continue
		}
		orphans = append(orphans, branch)
	}
	return orphans
}

// HealOrphanEnvBranches deletes every env/* branch that OrphanEnvBranches flags
// as having no matching divergence, calling del to remove each branch on remote.
// In production del is git.DeleteRemoteBranch, whose delete of an absent branch
// is a no-op success, so HealOrphanEnvBranches is idempotent: re-running it, or
// running it against an orphan that is already gone, deletes nothing further and
// never errors.
//
// Only orphans are deleted. A branch backing a diverged environment is never
// touched, because the deletion set comes from OrphanEnvBranches, which excludes
// it by the same IsDiverged() predicate the hotfix preflight and status
// consistency classify on. The returned slice lists the branches deleted, in
// input order, and is nil when nothing was orphaned. On the first delete error
// the heal stops and returns that error with a nil healed slice.
func HealOrphanEnvBranches(branches []string, state map[string]*config.EnvState, remote string, del func(remote, branch string) error) ([]string, error) {
	var healed []string
	for _, branch := range OrphanEnvBranches(branches, state) {
		if err := del(remote, branch); err != nil {
			return nil, fmt.Errorf("deleting orphan branch %s on %s: %w", branch, remote, err)
		}
		healed = append(healed, branch)
	}
	return healed, nil
}

// HotfixTagsForBase returns the hotfix tags in tags that belong to the rc base
// of baseVersion. A hotfix tag has the dotted shape vX.Y.Z-rc.N.hotfix.M; it
// shares the rc base (vX.Y.Z-rc.N) of the version the environment held while
// diverged. The RC-shaped cleanup in internal/release deliberately cannot see
// these tags (it matches only ^vX.Y.Z-rc.N$), so divergence-end cleanup must
// collect them explicitly.
//
// baseVersion may itself be a hotfix version (vX.Y.Z-rc.N.hotfix.M); it is
// normalized to its rc base before matching. Tags that do not parse, are not
// hotfix tags, or belong to a different rc base are excluded. The result is nil
// when nothing matches.
func HotfixTagsForBase(baseVersion string, tags []string) []string {
	base, err := version.Parse(baseVersion)
	if err != nil || base.PreRelease < 0 {
		return nil
	}
	// Normalize to the rc base so a hotfix baseVersion matches its siblings.
	rcBase := base.WithRC(base.PreRelease).String()

	var matched []string
	for _, tag := range tags {
		v, err := version.Parse(tag)
		if err != nil || v.Hotfix < 0 {
			continue
		}
		if v.WithRC(v.PreRelease).String() == rcBase {
			matched = append(matched, tag)
		}
	}
	return matched
}
