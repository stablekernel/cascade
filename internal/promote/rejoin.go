package promote

import (
	"fmt"
	"os"

	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/hotfix"
	"github.com/stablekernel/cascade/internal/release"
)

// CleanReleasesRequest describes the hotfix release objects to remove when an
// environment rejoins trunk. BaseVersion is the version the environment held
// while diverged; its rc base identifies the hotfix tags (vX.Y.Z-rc.N.hotfix.M)
// and drafts that the RC-shaped cleanup deliberately cannot see.
type CleanReleasesRequest struct {
	Environment string
	BaseVersion string
}

// LifecycleCleaner performs the side effects of ending a divergence: deleting
// the per-environment integration branch and removing the hotfix tags and
// release objects minted for that base. It is a small interface with a no-op
// default so a normal promotion into a non-diverged environment is never forced
// to provide one; the production implementation is wired only when finalize runs
// in a repository with GitHub context.
type LifecycleCleaner interface {
	// DeleteEnvBranch deletes the env/<env> integration branch.
	DeleteEnvBranch(env string) error
	// CleanHotfixReleases deletes the hotfix tags and release drafts for the
	// rejoining environment's prior base version.
	CleanHotfixReleases(req CleanReleasesRequest) error
}

// noopLifecycleCleaner is the default cleaner. It performs no side effects, so a
// Finalizer constructed without WithLifecycleCleaner behaves exactly as before
// for non-diverged promotions.
type noopLifecycleCleaner struct{}

func (noopLifecycleCleaner) DeleteEnvBranch(string) error                   { return nil }
func (noopLifecycleCleaner) CleanHotfixReleases(CleanReleasesRequest) error { return nil }

// FinalizeOption customizes optional, additive Finalizer behavior. Required
// inputs stay positional on the constructor; cross-cutting concerns such as the
// divergence-end cleanup are threaded through options so existing callers and
// signatures are unaffected.
type FinalizeOption func(*Finalizer)

// WithLifecycleCleaner injects the divergence-end cleanup performed when a
// promotion rejoins a diverged environment to trunk. The default is a no-op, so
// promotions into non-diverged environments incur no cleanup behavior.
func WithLifecycleCleaner(c LifecycleCleaner) FinalizeOption {
	return func(f *Finalizer) {
		if c != nil {
			f.cleaner = c
		}
	}
}

// rejoinEvent records that a diverged environment rejoined trunk during
// finalization, carrying the data the cleaner needs to remove its branch, tags,
// and drafts.
type rejoinEvent struct {
	env         string
	baseVersion string
	// rollbackOrigin is true when the env diverged via a manual rollback rather
	// than a hotfix integration branch. The rejoin cleanup skips integration
	// branch and hotfix release deletion in that case, since a rollback creates
	// no such objects; the divergence fields are still cleared unconditionally.
	rollbackOrigin bool
}

// gitReleaseCleaner is the production LifecycleCleaner. It deletes the remote
// integration branch with git and removes the hotfix tags and drafts through the
// release API. It is constructed only when finalize has the GitHub context
// (repository and token) needed to act; without it, the no-op default is used.
type gitReleaseCleaner struct {
	remote     string
	releaseMgr *release.Manager
	listTags   func() ([]string, error)
	deleteTag  func(remote, name string) error
}

// newGitReleaseCleaner builds a production cleaner. remote is the git remote that
// hosts the env/* branches (typically "origin"); mgr performs release deletes.
func newGitReleaseCleaner(remote string, mgr *release.Manager) *gitReleaseCleaner {
	return &gitReleaseCleaner{
		remote:     remote,
		releaseMgr: mgr,
		listTags:   git.ListTags,
		deleteTag:  git.DeleteRemoteTag,
	}
}

// newFinalizeCleaner builds the production LifecycleCleaner from the workflow
// environment. It returns nil when GITHUB_REPOSITORY is unset (no GitHub context,
// for example an act/gitea run without the API), in which case the finalizer
// keeps its no-op default and clears the manifest fields without touching tags or
// drafts. The integration branch is still git-deletable in that environment, but
// without the repository the release-object cleanup cannot run, so the cleaner is
// only wired when both are possible.
func newFinalizeCleaner() LifecycleCleaner {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return nil
	}
	token := os.Getenv("RELEASE_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	return newGitReleaseCleaner("origin", release.NewManager(repo, token))
}

// DeleteEnvBranch deletes the env/<env> branch on the configured remote.
func (c *gitReleaseCleaner) DeleteEnvBranch(env string) error {
	branch := hotfix.EnvBranchPrefix + env
	if err := git.DeleteRemoteBranch(c.remote, branch); err != nil {
		return fmt.Errorf("deleting integration branch %s: %w", branch, err)
	}
	return nil
}

// CleanHotfixReleases deletes the hotfix tags for the prior base version and the
// matching draft release objects. Tag and draft deletion is best-effort per
// item so one stale object does not block the others; the first hard error is
// returned.
func (c *gitReleaseCleaner) CleanHotfixReleases(req CleanReleasesRequest) error {
	tags, err := c.listTags()
	if err != nil {
		return fmt.Errorf("listing tags for hotfix cleanup: %w", err)
	}
	hotfixTags := hotfix.HotfixTagsForBase(req.BaseVersion, tags)

	var firstErr error
	for _, tag := range hotfixTags {
		// Remove the draft release object for the hotfix tag, then the tag.
		if c.releaseMgr != nil {
			if _, err := c.releaseMgr.Manage(release.Options{
				Action: release.ActionDelete,
				Tag:    tag,
			}); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("deleting hotfix release %s: %w", tag, err)
			}
		}
		if err := c.deleteTag(c.remote, tag); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("deleting hotfix tag %s: %w", tag, err)
		}
	}
	return firstErr
}
