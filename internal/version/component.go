package version

import (
	"fmt"

	"github.com/stablekernel/cascade/internal/changelog"
	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
)

// ComponentVersionInputs holds everything a component's next version is derived
// from: its current version and release base read from its own strict tag
// namespace, the conventional commits in its path-scoped range, the paths that
// scoped that range, and a calculator under the component's tag grammar.
type ComponentVersionInputs struct {
	// CurrentDevVersion is the component's latest tag (the RC-counter base).
	CurrentDevVersion string
	// NextEnvVersion is the component's latest published (non-prerelease)
	// release tag (the bump base).
	NextEnvVersion string
	// Commits are the conventional commits in the component's scoped range.
	Commits []changelog.ConventionalCommit
	// Paths is the commit scope: the component's path plus its effective extra
	// paths (its extra_paths unioned with top-level shared_paths).
	Paths []string
	// Calc calculates versions under the component's resolved tag grammar.
	Calc *Calculator
}

// ResolveComponentVersionInputs derives the version inputs for a named
// component. It is the single source of the component version scope: both
// `cascade next-version --component` and orchestrate's component version
// calculation call it, so the CLI preview and the production pipeline cannot
// drift.
//
// The component reads and emits under its own strict tag prefix only, so a
// sibling's tags are invisible to it. The commit range is scoped to the
// component's path plus its effective extra paths (its extra_paths unioned with
// top-level shared_paths), so a commit that touches only a shared dependency the
// component declares still registers a bump.
//
// Failing lookups surface as errors rather than reading as "no tags" or "no
// commits": a component read as tagless restarts at its v0.1.0-rc.0, a
// regressive mint that can collide with published tags, and an empty commit
// range silently under-bumps the calculated version.
//
// baseDir scopes the tag lookups (empty means the working directory). baseSHA,
// when non-empty, overrides the release-tag-derived base of the commit range;
// headSHA is the range head (callers pass "HEAD" for the current tip).
func ResolveComponentVersionInputs(cfg *config.TrunkConfig, component, baseDir, baseSHA, headSHA string) (*ComponentVersionInputs, error) {
	resolved, err := cfg.ResolveComponent(component)
	if err != nil {
		return nil, fmt.Errorf("resolving component %q: %w", component, err)
	}
	return ResolveComponentVersionInputsFor(resolved, baseDir, baseSHA, headSHA)
}

// ResolveComponentVersionInputsFor is ResolveComponentVersionInputs for a
// component whose config the caller has ALREADY resolved. It is the entry point
// for a runtime that plans against the component's resolved config: such a
// config declares no nested components of its own, so the component can no
// longer be resolved out of it and the root-config entry point above cannot be
// used. Both entry points share this one derivation, so the CLI preview and the
// production pipeline cannot drift.
func ResolveComponentVersionInputsFor(resolved *config.ResolvedComponent, baseDir, baseSHA, headSHA string) (*ComponentVersionInputs, error) {
	component := resolved.Name
	spec := resolved.TagGrammarSpec()

	inputs := &ComponentVersionInputs{
		Paths: append([]string{resolved.Path}, resolved.ExtraPaths...),
		Calc:  NewCalculatorWithGrammar(spec),
	}

	// Current version (RC-counter base) from the component's own tag namespace.
	latestTag, _, err := git.GetLatestTagSpec(baseDir, spec)
	if err != nil {
		return nil, fmt.Errorf("reading latest tag for component %q: %w", component, err)
	}
	if latestTag != "" {
		inputs.CurrentDevVersion = latestTag
	}

	// Base version from the component's latest published (non-prerelease) release.
	latestRelease, releaseSHA, err := git.GetLatestReleaseTagSpec(baseDir, spec)
	if err != nil {
		return nil, fmt.Errorf("reading latest release tag for component %q: %w", component, err)
	}
	if latestRelease != "" {
		inputs.NextEnvVersion = latestRelease
	}

	// Commit range: from the caller's override, else the component's release
	// base, else the repo's initial commit (fresh component), to headSHA.
	if baseSHA == "" {
		baseSHA = releaseSHA
	}
	if baseSHA == "" {
		initial, ierr := git.GetInitialCommit()
		if ierr != nil {
			return nil, fmt.Errorf("resolving initial commit for component %q version range: %w", component, ierr)
		}
		baseSHA = initial
	}
	if baseSHA != "" {
		gitCommits, cerr := git.GetCommitsForPaths(baseSHA, headSHA, inputs.Paths)
		if cerr != nil {
			return nil, fmt.Errorf("reading commits for component %q version calculation: %w", component, cerr)
		}
		for _, gc := range gitCommits {
			if cc := changelog.ParseCommit(gc); cc != nil {
				inputs.Commits = append(inputs.Commits, *cc)
			}
		}
	}

	return inputs, nil
}
