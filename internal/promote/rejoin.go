package promote

import (
	"fmt"
	"os"
	"time"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/hotfix"
	"github.com/stablekernel/cascade/internal/release"
	"github.com/stablekernel/cascade/internal/statewrite"
	"github.com/stablekernel/cascade/internal/taggrammar"
)

// CleanReleasesRequest describes the hotfix release objects to remove when an
// environment rejoins trunk. BaseVersion is the version the environment held
// while diverged; its rc base identifies the hotfix tags (vX.Y.Z-rc.N.hotfix.M)
// and drafts that the RC-shaped cleanup deliberately cannot see.
type CleanReleasesRequest struct {
	Environment string
	BaseVersion string
	// SHA is the commit the environment pointed at while diverged (the hotfix
	// merge SHA). It is passed to the release lookup as a fallback so a hotfix
	// release whose tag was already deleted by a prior partial run can still be
	// resolved by its target_commitish and removed, rather than leaking.
	SHA string
	// Spec is the resolved tag grammar. It lets the hotfix-tag match parse a
	// custom pre-release token. A zero value is normalized to the historical
	// default by the cleaner, so callers with no custom grammar can omit it.
	Spec taggrammar.Spec
}

// LifecycleCleaner performs the side effects of ending a divergence: deleting
// the per-environment integration branch and removing the hotfix tags and
// release objects minted for that base. It is a small interface with a no-op
// default so a normal promotion into a non-diverged environment is never forced
// to provide one; the production implementation is wired only when finalize runs
// in a repository with GitHub context.
type LifecycleCleaner interface {
	// DeleteEnvBranch deletes the integration branch for env within component:
	// env/<component>/<env>, or env/<env> for the default empty component. The
	// component is recovered from the branch the rejoin is cleaning up so a
	// component's branch is deleted in its own namespace and a sibling's branch is
	// never cross-deleted.
	DeleteEnvBranch(component, env string) error
	// CleanHotfixReleases deletes the hotfix tags and release drafts for the
	// rejoining environment's prior base version.
	CleanHotfixReleases(req CleanReleasesRequest) error
}

// noopLifecycleCleaner is the default cleaner. It performs no side effects, so a
// Finalizer constructed without WithLifecycleCleaner behaves exactly as before
// for non-diverged promotions.
type noopLifecycleCleaner struct{}

func (noopLifecycleCleaner) DeleteEnvBranch(string, string) error           { return nil }
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

// WithClock injects the wall clock used to stamp the audit timestamps written
// to state during finalization. The default is time.Now; supplying a fixed
// clock makes the emitted CommittedAt/DeployedAt/ReleasedOn values deterministic
// for tests and golden output. A nil argument is ignored so the default stands.
func WithClock(now func() time.Time) FinalizeOption {
	return func(f *Finalizer) {
		if now != nil {
			f.now = now
		}
	}
}

// WithComponent scopes finalization to the named declared component, so its
// promoted state is recorded under state.components.<name>.<env> (and, on a
// publish, latest_release.components.<name>) via the scoped serializer rather
// than the flat single-component state.<env> form. An empty name is a no-op,
// preserving the single-component path byte-identically. It is set by a
// per-component generated promote workflow passing --component.
func WithComponent(name string) FinalizeOption {
	return func(f *Finalizer) {
		f.component = name
	}
}

// withContentsClient injects the GitHub Contents client the API state-write path
// uses, so the optimistic-lock retry loop can be exercised against a fake that
// simulates a concurrent finalizer's 409. Production leaves it unset and the
// default gh-CLI client is used. It is unexported because it exists only for the
// concurrent-finalize tests, not the public API surface.
func withContentsClient(c statewrite.ContentsClient) FinalizeOption {
	return func(f *Finalizer) {
		if c != nil {
			f.contentsClient = c
		}
	}
}

// rejoinEvent records that a diverged environment rejoined trunk during
// finalization, carrying the data the cleaner needs to remove its branch, tags,
// and drafts.
type rejoinEvent struct {
	env string
	// component is the declared component that owns the rejoining integration
	// branch, recovered from the recorded ref (env/<component>/<env>) via
	// hotfix.ParseEnvBranch. The default single-component form (env/<env>) yields
	// an empty component, keeping branch deletion and tag collection
	// byte-identical to the pre-component behavior.
	component   string
	baseVersion string
	// sha is the commit the env pointed at while diverged (its hotfix merge SHA),
	// passed through to the release cleanup as a lookup fallback so a hotfix
	// release can still be resolved if its tag was removed by a prior partial run.
	sha string
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

// DeleteEnvBranch deletes the integration branch for env within component on the
// configured remote. The branch name is composed with hotfix.EnvBranchName, so a
// named component targets env/<component>/<env> and the default empty component
// targets env/<env>, matching exactly what the hotfix finalize created.
func (c *gitReleaseCleaner) DeleteEnvBranch(component, env string) error {
	branch := hotfix.EnvBranchName(component, env)
	if err := git.DeleteRemoteBranch(c.remote, branch); err != nil {
		return fmt.Errorf("deleting integration branch %s: %w", branch, err)
	}
	return nil
}

// resolveRejoinCleanupSpec returns the tag grammar the divergence-end release
// cleanup collects hotfix tags under for the rejoining branch's component. The
// default (empty) component yields the manifest's permissive grammar, so a
// single-component rejoin collects tags byte-identically to the pre-component
// behavior. A named component yields that component's resolved grammar with its
// strict tag prefix, so the cleanup sees only that component's own hotfix tags
// and never cross-matches a sibling component's namespace.
func resolveRejoinCleanupSpec(f *config.CICDFile, component string) (taggrammar.Spec, error) {
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

// CleanHotfixReleases deletes the hotfix release objects for the prior base
// version and then the matching tags. The release object is removed FIRST and the
// tag is deleted only when the release delete succeeded (or the release was
// already gone): a failed release delete leaves the tag intact so a rerun can
// still resolve the release object by tag rather than orphaning a now-tagless
// release. The hotfix release may be a non-draft prerelease (the prerelease env
// promotes its hotfix release to draft:false), so AllowPublishedDelete is set;
// the recorded SHA is passed as a lookup fallback for a tag that a prior partial
// run already removed. Cleanup is best-effort per item so one stale object does
// not block the others; the first hard error is returned.
func (c *gitReleaseCleaner) CleanHotfixReleases(req CleanReleasesRequest) error {
	tags, err := c.listTags()
	if err != nil {
		return fmt.Errorf("listing tags for hotfix cleanup: %w", err)
	}
	spec := req.Spec
	if spec == (taggrammar.Spec{}) {
		spec = taggrammar.Default()
	}
	hotfixTags := hotfix.HotfixTagsForBase(spec, req.BaseVersion, tags)

	var firstErr error
	for _, tag := range hotfixTags {
		// Delete the release object first. Only delete the tag if that succeeded,
		// so a failed release delete leaves the tag for a rerun to retry.
		releaseDeleted := true
		if c.releaseMgr != nil {
			if _, err := c.releaseMgr.Manage(release.Options{
				Action:               release.ActionDelete,
				Tag:                  tag,
				SHA:                  req.SHA,
				AllowPublishedDelete: true,
			}); err != nil {
				releaseDeleted = false
				if firstErr == nil {
					firstErr = fmt.Errorf("deleting hotfix release %s: %w", tag, err)
				}
			}
		}
		if !releaseDeleted {
			// Leave the tag so the next run can still resolve the release by tag.
			continue
		}
		if err := c.deleteTag(c.remote, tag); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("deleting hotfix tag %s: %w", tag, err)
		}
	}
	return firstErr
}
