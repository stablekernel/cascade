// Package release manages GitHub releases over the GitHub REST API:
// creating and updating the draft release that accumulates changes and
// publishing it when a version is released.
package release

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/stablekernel/cascade/internal/globals"
	"github.com/stablekernel/cascade/internal/taggrammar"
	"github.com/stablekernel/cascade/internal/version"
)

// Action represents the release management action to perform
type Action string

const (
	ActionCreate     Action = "create"
	ActionUpdate     Action = "update"
	ActionLock       Action = "lock"
	ActionPrerelease Action = "prerelease"
	ActionPublish    Action = "publish"
	ActionDelete     Action = "delete"
)

// Result contains the output of a release operation
type Result struct {
	ReleaseID  int64  `json:"release_id"`
	ReleaseURL string `json:"release_url"`
	HTMLURL    string `json:"html_url"`
}

// Manager handles GitHub release operations
type Manager struct {
	client  *http.Client
	baseURL string
	token   string
	repo    string
	// grammar, when set, is the resolved per-component tag grammar the RC-tag
	// reaper parses tags through. It is nil for the single-component (default)
	// path, which keeps the historical permissive matching so single-component
	// reaping is behavior-identical to before component threading existed. A
	// declared component supplies a strict-prefix grammar via WithTagGrammar so
	// reaping stays exact to that component's tag namespace.
	grammar *taggrammar.Spec
	// sleepFn is called between retry attempts in findReleaseByTagOrSHA to give
	// GitHub's release-list endpoint time to reflect a recently created draft.
	// Defaults to time.Sleep; tests inject a no-op to keep test runs fast.
	sleepFn func(time.Duration)
}

// Option configures a Manager at construction. Options follow the functional
// options pattern so new per-component capability is additive and never a
// breaking change to the constructor signature.
type Option func(*Manager)

// WithTagGrammar scopes the Manager's RC-tag reaper to a component's tag
// namespace by parsing candidate tags through spec instead of the historical
// permissive matcher. spec is expected to be a strict-prefix grammar (see
// config.ResolvedComponent.TagGrammarSpec), so a component reaps only its own RC
// tags and never a sibling component's. Omitting this option keeps the
// single-component permissive behavior unchanged.
func WithTagGrammar(spec taggrammar.Spec) Option {
	return func(m *Manager) {
		s := spec
		m.grammar = &s
	}
}

// NewManager creates a new release manager.
// It respects GITHUB_API_URL for GitHub Enterprise or test environments.
func NewManager(repo, token string, opts ...Option) *Manager {
	baseURL := "https://api.github.com"
	if envURL := os.Getenv("GITHUB_API_URL"); envURL != "" {
		baseURL = strings.TrimSuffix(envURL, "/")
	}
	m := &Manager{
		client:  &http.Client{},
		baseURL: baseURL,
		token:   token,
		repo:    repo,
		sleepFn: time.Sleep,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// NewManagerWithURL creates a release manager with a custom API URL.
// Use this for testing or when GITHUB_API_URL isn't set.
func NewManagerWithURL(repo, token, baseURL string, opts ...Option) *Manager {
	m := &Manager{
		client:  &http.Client{},
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		repo:    repo,
		sleepFn: time.Sleep,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// isGitHubHost reports whether the API base URL points at GitHub (github.com or
// a GitHub Enterprise host) rather than the Gitea e2e backend. GitHub exposes
// the git-data refs API; Gitea does not, and materializes tags from a release's
// target_commitish instead. Detection is by host substring: GitHub API hosts
// contain "github", which the Gitea test host (localhost/gitea) does not.
func isGitHubHost(baseURL string) bool {
	return strings.Contains(baseURL, "github")
}

// Options contains the parameters for release operations
type Options struct {
	Action      Action
	Environment string
	SHA         string
	Tag         string
	Changelog   string
	PreviousTag string // Tag to compare against for "What's Changed" link
	NewTag      string // New tag for publish (semver) - replaces short-sha tag
	DeleteTag   string // Tag to delete after publish (short-sha cleanup)
	CreateTag   bool   // Whether to create the git tag (for initial release)
	// TagOnly makes create/update materialize the git tag only and skip the
	// draft-release POST. It exists for a self-publishing release pipeline (for
	// example GoReleaser) that is the sole creator of the release object: when a
	// separate release workflow publishes its own release for the same tag, a
	// draft pre-created here would never be reconciled and would orphan. In
	// tag-only mode the tag is still created when CreateTag is set, but no release
	// object is created or mutated, so a published release the release workflow
	// already made is never touched.
	TagOnly bool
	// KnownReleaseID is the GitHub release ID returned by a preceding ActionCreate
	// in the same workflow step. When set, ActionPrerelease and ActionLock use it
	// directly instead of re-discovering the release by tag, eliminating the
	// eventual-consistency window between draft creation and the list endpoint.
	KnownReleaseID int64
	// AllowPublishedDelete permits ActionDelete to remove a non-draft (published
	// or prerelease) release. It exists ONLY for hotfix-rejoin cleanup, where the
	// release is a superseded intermediate artifact: a hotfix on the prerelease
	// env is promoted to a GitHub prerelease (draft:false), so by the time a
	// later promote rejoins the env the hotfix release is non-draft and the
	// general guard would otherwise wedge the cleanup. Normal publish/promote
	// delete paths leave this false so a real published release is still protected.
	AllowPublishedDelete bool
}

// ValidateAction checks if the action is valid
func ValidateAction(action string) (Action, error) {
	switch Action(action) {
	case ActionCreate, ActionUpdate, ActionLock, ActionPrerelease, ActionPublish, ActionDelete:
		return Action(action), nil
	default:
		return "", fmt.Errorf("invalid action: %s (must be create, update, lock, prerelease, publish, or delete)", action)
	}
}

// Manage performs the specified release action
func (m *Manager) Manage(opts Options) (*Result, error) {
	switch opts.Action {
	case ActionCreate:
		return m.create(opts)
	case ActionUpdate:
		return m.update(opts)
	case ActionLock:
		return m.lock(opts)
	case ActionPrerelease:
		return m.prerelease(opts)
	case ActionPublish:
		return m.publish(opts)
	case ActionDelete:
		return m.delete(opts)
	default:
		return nil, fmt.Errorf("unknown action: %s", opts.Action)
	}
}

// GitHubRelease represents a GitHub release
type GitHubRelease struct {
	ID              int64  `json:"id"`
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish"`
	Name            string `json:"name"`
	Body            string `json:"body"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
	URL             string `json:"url"`
	HTMLURL         string `json:"html_url"`
}

// createGitTag creates a lightweight git tag pointing to a commit.
//
// On a non-GitHub host (the Gitea e2e backend) the GitHub git-data refs API is
// unavailable, and the release create that follows materializes the tag from
// target_commitish, so the explicit ref create is skipped there.
func (m *Manager) createGitTag(tagName, sha string) error {
	if !isGitHubHost(m.baseURL) {
		return nil
	}

	// Create a reference for the tag
	payload := map[string]interface{}{
		"ref": "refs/tags/" + tagName,
		"sha": sha,
	}

	req, err := m.newRequest("POST", "/git/refs", payload)
	if err != nil {
		return err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("create tag request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 201 Created or 422 (already exists) are acceptable
	if resp.StatusCode == http.StatusCreated {
		return nil
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		// Tag already exists - this is fine
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("create tag failed with status %d: %s", resp.StatusCode, string(body))
}

// deleteGitTag deletes a git tag
func (m *Manager) deleteGitTag(tagName string) error {
	endpoint := "/git/refs/tags/" + tagName
	req, err := m.newRequest("DELETE", endpoint, nil)
	if err != nil {
		return err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete tag request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 204 No Content or 404 (not found) are acceptable
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("delete tag failed with status %d: %s", resp.StatusCode, string(body))
}

// cleanupRCTags deletes every RC tag whose base version is at or below the
// published version. For example, publishing "v1.0.1" reaps v1.0.1-rc.0,
// v1.0.1-rc.1, and also any superseded earlier base such as v1.0.0-rc.0 left
// behind when v1.0.0 was never published. RC tags on a strictly higher base
// (e.g. v1.1.0-rc.0, staged for a future release) are preserved, as are tags
// carrying a different tag prefix, which name an unrelated release line.
//
// Hotfix-variant RC tags (v1.0.0-rc.2.hotfix.1) are not plain RC tags, so
// parseRCTag rejects them and this loop skips them; their lifecycle is handled
// by the hotfix-rejoin cleanup, not publish-time reaping.
//
// This is called after publishing a release to clean up the RC tags.
func (m *Manager) cleanupRCTags(publishedTag string) error {
	// Build the predicate that decides which listed RC tags this publish
	// supersedes. When a per-component grammar is threaded (WithTagGrammar) the
	// predicate parses candidates through that strict grammar so reaping stays
	// exact to the component's namespace; otherwise it reproduces the historical
	// permissive matching byte for byte.
	supersedes, err := m.supersededRCMatcher(publishedTag)
	if err != nil {
		return err
	}

	// List all tags in the repository
	tags, err := m.listTags()
	if err != nil {
		return fmt.Errorf("listing tags: %w", err)
	}

	for _, tag := range tags {
		if !supersedes(tag) {
			continue
		}
		fmt.Printf("Cleaning up RC tag: %s\n", tag)
		if err := m.deleteGitTag(tag); err != nil {
			fmt.Printf("Warning: failed to delete RC tag %s: %v\n", tag, err)
			// Continue with other tags
		}
	}

	return nil
}

// supersededRCMatcher returns a predicate reporting whether a listed tag is a
// plain RC tag in the published tag's namespace whose base version is at or below
// it, and therefore superseded by the publish. When the Manager carries a
// per-component grammar the match is strict to that component's prefix and
// pre-release token; otherwise it is the historical permissive match. It errors
// only when the published tag itself is not parseable under the active grammar,
// so a misconfigured publish fails loudly rather than reaping nothing.
func (m *Manager) supersededRCMatcher(publishedTag string) (func(tag string) bool, error) {
	if m.grammar != nil {
		return strictSupersededRCMatcher(*m.grammar, publishedTag)
	}
	return legacySupersededRCMatcher(publishedTag)
}

// legacySupersededRCMatcher reproduces the historical permissive reaper: it
// splits the numeric core off any prefix, requires a string-equal prefix, and
// reaps every plain RC tag whose base is at or below the published base. It is
// the single-component path and is behavior-identical to the pre-threading code.
func legacySupersededRCMatcher(publishedTag string) (func(string) bool, error) {
	publishedPrefix, published, err := splitVersionPrefix(publishedTag)
	if err != nil {
		return nil, fmt.Errorf("parsing published version %q: %w", publishedTag, err)
	}
	return func(tag string) bool {
		tagBase, _, ok := parseRCTag(tag)
		if !ok {
			return false // Not a plain RC tag (or a hotfix variant)
		}
		basePrefix, base, err := splitVersionPrefix(tagBase)
		if err != nil {
			return false // Unparseable base - leave it alone
		}
		// A different prefix names a separate release line; never compare across
		// prefixes since version.Compare is prefix-agnostic.
		if basePrefix != publishedPrefix {
			return false
		}
		// Preserve bases strictly greater than the published version (future work).
		return base.Compare(published) <= 0
	}, nil
}

// strictSupersededRCMatcher parses candidates through a component's strict tag
// grammar. Because the grammar's prefix is matched literally, a sibling
// component's tags never parse and so are never enumerated, let alone reaped;
// this closes the cross-namespace hazard the permissive string-prefix compare
// left open. A custom pre-release token is matched because the token comes from
// the grammar rather than a hardcoded "-rc.", so custom-grammar RC tags reap
// instead of accumulating.
func strictSupersededRCMatcher(spec taggrammar.Spec, publishedTag string) (func(string) bool, error) {
	published, ok := spec.Parse(publishedTag)
	if !ok {
		return nil, fmt.Errorf("published version %q is not a valid tag under the component tag grammar", publishedTag)
	}
	return func(tag string) bool {
		p, ok := spec.Parse(tag)
		if !ok {
			return false // Foreign namespace or foreign shape - not ours.
		}
		if p.PreRelease < 0 {
			return false // A published base tag, not an RC tag.
		}
		if p.Hotfix >= 0 {
			return false // Hotfix variant - reaped by the hotfix-rejoin path.
		}
		// Preserve bases strictly greater than the published version (future work).
		return compareParsedBase(p, published) <= 0
	}, nil
}

// compareParsedBase returns -1, 0, or +1 comparing only the numeric
// major.minor.patch cores of a and b, ignoring pre-release and hotfix segments.
// It mirrors the base comparison the permissive path performs via version.Compare
// on two pre-release-stripped versions.
func compareParsedBase(a, b taggrammar.Parsed) int {
	if c := compareInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := compareInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	return compareInt(a.Patch, b.Patch)
}

// compareInt returns -1, 0, or +1 reporting whether a is less than, equal to, or
// greater than b.
func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// splitVersionPrefix splits a base version tag into its tag prefix and the
// parsed numeric core. version.Parse only recognizes an alphabetic prefix, so a
// non-letter prefix such as "rel-" or "release/" is peeled off here before
// parsing the numeric "major.minor.patch" remainder. The returned Version
// carries no prefix; callers compare the numeric core via version.Compare and
// compare prefixes separately as strings.
func splitVersionPrefix(baseTag string) (prefix string, v *version.Version, err error) {
	loc := versionCorePattern.FindStringIndex(baseTag)
	if loc == nil {
		return "", nil, fmt.Errorf("no version core in %q", baseTag)
	}
	prefix = baseTag[:loc[0]]
	parsed, err := version.Parse(baseTag[loc[0]:])
	if err != nil {
		return "", nil, err
	}
	return prefix, parsed, nil
}

// versionCorePattern locates the trailing numeric "major.minor.patch" core of a
// base version tag so any tag prefix (alphabetic, "rel-", "release/", or none)
// can be split off before the core is parsed.
var versionCorePattern = regexp.MustCompile(`\d+\.\d+\.\d+$`)

// listTags returns all tags in the repository
func (m *Manager) listTags() ([]string, error) {
	req, err := m.newRequest("GET", "/git/refs/tags", nil)
	if err != nil {
		return nil, err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 404 means no tags exist
	if resp.StatusCode == http.StatusNotFound {
		return []string{}, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var refs []struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refs); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	var tags []string
	for _, ref := range refs {
		// refs/tags/v1.0.0 -> v1.0.0
		tag := strings.TrimPrefix(ref.Ref, "refs/tags/")
		tags = append(tags, tag)
	}

	return tags, nil
}

func (m *Manager) create(opts Options) (*Result, error) {
	// Tag-only mode: the configured release workflow (for example GoReleaser) is
	// the sole creator of the release object, so materialize the git tag and skip
	// the draft-release POST entirely. Pre-creating a draft here would orphan when
	// the release workflow publishes its own release for the same tag, and the
	// draft would never be reconciled. No stale-draft sweep runs because tag-only
	// mode never creates drafts to accumulate.
	if opts.TagOnly {
		if opts.CreateTag {
			if err := m.createGitTag(opts.Tag, opts.SHA); err != nil {
				return nil, fmt.Errorf("creating git tag: %w", err)
			}
		}
		return &Result{}, nil
	}

	// Clean up any existing draft releases for this environment first
	// This prevents accumulation of stale drafts when new commits are pushed
	if err := m.cleanupStaleDrafts(opts.Environment, opts.Tag); err != nil {
		// Log warning but don't fail - cleanup is best-effort
		fmt.Printf("Warning: failed to cleanup stale drafts: %v\n", err)
	}

	// Create git tag if requested (for initial release on merge to trunk)
	if opts.CreateTag {
		if err := m.createGitTag(opts.Tag, opts.SHA); err != nil {
			return nil, fmt.Errorf("creating git tag: %w", err)
		}
	}

	releaseName := generateReleaseName(opts.Environment, opts.Tag)
	bodyWithStatus := m.capBody(addStatusLine(opts.Changelog, opts.Environment), opts.PreviousTag, opts.Tag)

	payload := map[string]interface{}{
		"tag_name":         opts.Tag,
		"target_commitish": opts.SHA,
		"name":             releaseName,
		"body":             bodyWithStatus,
		"draft":            true,
		"prerelease":       false,
	}

	// Set previous tag for "What's Changed" comparison link
	if opts.PreviousTag != "" {
		payload["generate_release_notes"] = false
		// Note: GitHub's make_latest is for releases, not drafts
		// The previous tag comparison is shown via the changelog we generate
	}

	// GitHub's Releases API (release objects) is unavailable on the Gitea backend
	// used by the e2e harness: its release-object endpoints reject the GitHub
	// release shape and Bearer auth. On a non-GitHub host the tag is materialized
	// via the env branch / git tag path, so the release-object create is skipped
	// and a synthetic success is returned. Real-GitHub release-object behavior is
	// exercised by the real-GitHub validation fleet; this gate does not change the
	// real-GitHub code path.
	if !isGitHubHost(m.baseURL) {
		return &Result{}, nil
	}

	release, err := m.apiRequest("POST", "/releases", payload)
	if err != nil {
		return nil, fmt.Errorf("creating release: %w", err)
	}

	return &Result{
		ReleaseID:  release.ID,
		ReleaseURL: release.URL,
		HTMLURL:    release.HTMLURL,
	}, nil
}

// rcTagPattern matches an RC tag of the form <prefix><major>.<minor>.<patch>-rc.<n>.
// The prefix is captured permissively so that any configured tag_grammar.prefix works
// (the default "v", a custom value like "rel-", or an empty prefix). The base
// version capture includes the prefix, so callers can compare it directly
// against the published release tag without reconstructing the prefix.
var rcTagPattern = regexp.MustCompile(`^(.*\d+\.\d+\.\d+)-rc\.(\d+)$`)

// parseRCTag extracts the base version (including its tag prefix) and RC number
// from an RC tag. It is prefix-aware so that custom tag_grammar.prefix values are
// handled, not just the default "v":
//
//	"v1.3.0-rc.3"     -> ("v1.3.0", 3, true)
//	"rel-0.1.0-rc.0"  -> ("rel-0.1.0", 0, true)
//	"1.0.0-rc.1"      -> ("1.0.0", 1, true)
//
// Returns empty string, -1, false if not a valid RC tag.
func parseRCTag(tag string) (baseVersion string, rcNumber int, ok bool) {
	matches := rcTagPattern.FindStringSubmatch(tag)
	if len(matches) != 3 {
		return "", -1, false
	}
	var rc int
	if _, err := fmt.Sscanf(matches[2], "%d", &rc); err != nil {
		return "", -1, false
	}
	return matches[1], rc, true
}

// parseRCTag extracts the base version and RC number from an RC tag under the
// Manager's active grammar. With a per-component grammar (WithTagGrammar) it
// parses strictly so a sibling component's tags and a custom pre-release token
// are handled; without one it falls back to the historical permissive
// package-level parseRCTag, keeping single-component behavior identical.
func (m *Manager) parseRCTag(tag string) (baseVersion string, rcNumber int, ok bool) {
	if m.grammar != nil {
		return parseRCTagStrict(*m.grammar, tag)
	}
	return parseRCTag(tag)
}

// isRCTag reports whether tag is a plain RC tag under the Manager's active
// grammar.
func (m *Manager) isRCTag(tag string) bool {
	_, _, ok := m.parseRCTag(tag)
	return ok
}

// parseRCTagStrict extracts the base version (including its literal prefix) and
// RC number from tag under spec. A tag that is not a version tag, that carries no
// pre-release, or that is a nested hotfix variant is rejected, matching the plain
// RC-tag contract of the permissive parseRCTag. The base is rendered through the
// grammar so it carries the component's prefix.
func parseRCTagStrict(spec taggrammar.Spec, tag string) (baseVersion string, rcNumber int, ok bool) {
	p, matched := spec.Parse(tag)
	if !matched || p.PreRelease < 0 || p.Hotfix >= 0 {
		return "", -1, false
	}
	base := spec.Format(taggrammar.Parsed{
		Major:      p.Major,
		Minor:      p.Minor,
		Patch:      p.Patch,
		PreRelease: -1,
		Hotfix:     -1,
	})
	return base, p.PreRelease, true
}

// cleanupStaleDrafts deletes draft releases with the SAME base version but LOWER RC number.
// For example, when creating v1.3.0-rc.3, it deletes v1.3.0-rc.0, v1.3.0-rc.1, v1.3.0-rc.2.
// Drafts with different base versions (e.g., v1.2.0-rc.5) are preserved - they represent
// work that has been promoted to a different environment.
func (m *Manager) cleanupStaleDrafts(environment, currentTag string) error {
	currentBase, currentRC, ok := m.parseRCTag(currentTag)
	if !ok {
		// Not an RC tag, nothing to clean up
		return nil
	}

	releases, err := m.listDraftReleases()
	if err != nil {
		return err
	}

	for _, release := range releases {
		// Skip if this is the current tag (match by tag_name or name for untagged drafts)
		if release.TagName == currentTag || release.Name == currentTag {
			continue
		}

		// Get the tag to check - prefer tag_name, fallback to name
		tagToCheck := release.TagName
		if !m.isRCTag(tagToCheck) && m.isRCTag(release.Name) {
			tagToCheck = release.Name
		}

		// Parse the release tag
		releaseBase, releaseRC, ok := m.parseRCTag(tagToCheck)
		if !ok {
			// Not an RC tag, skip
			continue
		}

		// Only delete if same base version AND lower RC number
		if releaseBase != currentBase {
			fmt.Printf("Preserving draft %s (different base version: %s vs %s)\n", release.Name, releaseBase, currentBase)
			continue
		}
		if releaseRC >= currentRC {
			// Same or higher RC - shouldn't happen, but skip just in case
			continue
		}

		fmt.Printf("Cleaning up stale draft: %s (tag: %s, RC %d < %d)\n", release.Name, tagToCheck, releaseRC, currentRC)

		// Note: Tags are preserved - they are only created during orchestration
		// and never deleted. Only the draft release is cleaned up.

		// Delete the stale draft release
		endpoint := fmt.Sprintf("/releases/%d", release.ID)
		req, err := m.newRequest("DELETE", endpoint, nil)
		if err != nil {
			return err
		}

		resp, err := m.client.Do(req)
		if err != nil {
			return fmt.Errorf("delete request failed: %w", err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			return fmt.Errorf("delete failed with status %d", resp.StatusCode)
		}
	}

	return nil
}

// listDraftReleases returns all draft releases in the repository
func (m *Manager) listDraftReleases() ([]GitHubRelease, error) {
	req, err := m.newRequest("GET", "/releases", nil)
	if err != nil {
		return nil, err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var releases []GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	// Filter to only draft releases
	var drafts []GitHubRelease
	for _, r := range releases {
		if r.Draft {
			drafts = append(drafts, r)
		}
	}

	return drafts, nil
}

func (m *Manager) update(opts Options) (*Result, error) {
	// Tag-only mode never resolves or mutates a release object. Delegating to
	// create keeps the tag-create-and-return logic in one place and, critically,
	// skips the findRelease lookup so an unrelated release the self-publishing
	// workflow already made (matched by tag or target SHA) is never PATCHed.
	if opts.TagOnly {
		return m.create(opts)
	}

	// On a non-GitHub host (the Gitea e2e backend) the release-object endpoints
	// reject the GitHub release shape and Bearer auth, so findRelease and the
	// PATCH below cannot run. Mirror create(): delegate so the tag is materialized
	// and a synthetic success is returned. Real-GitHub release-object convergence
	// is covered by the finalize rerun unit test and the live fleet.
	if !isGitHubHost(m.baseURL) {
		return m.create(opts)
	}

	existing, err := m.findRelease(opts.Tag, opts.SHA)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		// No existing release, create new one. create() cuts the git tag on this
		// branch when CreateTag is set, so tag materialization is covered here too.
		return m.create(opts)
	}

	// Cut the git tag unconditionally when requested, mirroring create(). A
	// pre-existing release object (typically a draft) is PATCHed below, but the
	// git tag must still be materialized on this branch: the orchestrate state
	// write is an unconditional CAS loop, so leaving tag creation on the
	// create-only path lets a pre-existing draft advance the state leaf while the
	// git tag stays permanently absent. createGitTag is idempotent (a 422 "already
	// exists" is treated as success), so re-cutting a tag that is already present
	// is a harmless no-op on a convergence rerun.
	if opts.CreateTag {
		if err := m.createGitTag(opts.Tag, opts.SHA); err != nil {
			return nil, fmt.Errorf("creating git tag: %w", err)
		}
	}

	releaseName := generateReleaseName(opts.Environment, opts.Tag)
	bodyWithStatus := m.capBody(addStatusLine(opts.Changelog, opts.Environment), opts.PreviousTag, opts.Tag)

	// Always include tag_name to ensure proper association
	// This fixes issues where draft releases may have "untagged-..." as tag_name
	payload := map[string]interface{}{
		"tag_name":         opts.Tag,
		"target_commitish": opts.SHA,
		"name":             releaseName,
		"body":             bodyWithStatus,
	}

	release, err := m.apiRequest("PATCH", fmt.Sprintf("/releases/%d", existing.ID), payload)
	if err != nil {
		return nil, fmt.Errorf("updating release: %w", err)
	}

	return &Result{
		ReleaseID:  release.ID,
		ReleaseURL: release.URL,
		HTMLURL:    release.HTMLURL,
	}, nil
}

func (m *Manager) lock(opts Options) (*Result, error) {
	existing, err := m.findRelease(opts.Tag, opts.SHA)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		return nil, fmt.Errorf("no release found for tag %s (sha: %s)", opts.Tag, opts.SHA)
	}

	payload := map[string]interface{}{
		"prerelease": true,
	}

	release, err := m.apiRequest("PATCH", fmt.Sprintf("/releases/%d", existing.ID), payload)
	if err != nil {
		return nil, fmt.Errorf("locking release: %w", err)
	}

	return &Result{
		ReleaseID:  release.ID,
		ReleaseURL: release.URL,
		HTMLURL:    release.HTMLURL,
	}, nil
}

// prerelease converts a draft release to a pre-release with a semantic version tag.
// This is used at the second-to-last environment (e.g., UAT) to signal release candidate status.
// If NewTag is provided, creates the new semver tag and updates the release to use it.
func (m *Manager) prerelease(opts Options) (*Result, error) {
	// GitHub's Releases API (release objects) is unavailable on the Gitea backend
	// used by the e2e harness, and no release object exists there to promote. On a
	// non-GitHub host the prerelease promotion is skipped and a synthetic success
	// is returned; the semver tag, when requested, is still materialized via the
	// git tag path. Real-GitHub prerelease promotion is exercised by the
	// real-GitHub validation fleet; this gate does not change the real-GitHub code
	// path.
	if !isGitHubHost(m.baseURL) {
		if opts.NewTag != "" {
			if err := m.createGitTag(opts.NewTag, opts.SHA); err != nil {
				return nil, fmt.Errorf("creating semver tag: %w", err)
			}
		}
		return &Result{}, nil
	}

	// Resolve the release to promote. When the caller supplies KnownReleaseID
	// (set by the immediately preceding ActionCreate in the same workflow step),
	// use it directly - the just-created release needs no re-discovery and
	// bypassing findRelease eliminates the eventual-consistency race between
	// draft creation and GitHub's list endpoint propagation.
	var existingID int64
	var existingBody string
	if opts.KnownReleaseID != 0 {
		existingID = opts.KnownReleaseID
		// Body will be built from scratch using opts.Changelog below.
	} else {
		existing, err := m.findRelease(opts.Tag, opts.SHA)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, fmt.Errorf("no release found for tag %s (sha: %s)", opts.Tag, opts.SHA)
		}
		existingID = existing.ID
		existingBody = existing.Body
	}

	// If a new tag is specified, create it and update the release to use it
	tagToUse := opts.Tag
	if opts.NewTag != "" {
		// Create the new semver tag pointing to the same commit
		if err := m.createGitTag(opts.NewTag, opts.SHA); err != nil {
			return nil, fmt.Errorf("creating semver tag: %w", err)
		}
		tagToUse = opts.NewTag
	}

	releaseName := tagToUse
	bodyWithStatus := addStatusLine(opts.Changelog, opts.Environment)
	if opts.Changelog == "" {
		// Preserve existing body if no new changelog provided
		bodyWithStatus = updateStatusLine(existingBody, opts.Environment)
	}
	bodyWithStatus = m.capBody(bodyWithStatus, opts.PreviousTag, tagToUse)

	payload := map[string]interface{}{
		"tag_name":   tagToUse,
		"name":       releaseName,
		"body":       bodyWithStatus,
		"draft":      false,
		"prerelease": true,
	}

	release, err := m.apiRequest("PATCH", fmt.Sprintf("/releases/%d", existingID), payload)
	if err != nil {
		return nil, fmt.Errorf("converting to prerelease: %w", err)
	}

	return &Result{
		ReleaseID:  release.ID,
		ReleaseURL: release.URL,
		HTMLURL:    release.HTMLURL,
	}, nil
}

func (m *Manager) publish(opts Options) (*Result, error) {
	// For publish, opts.Tag is the semver tag (v1.0.0) and opts.DeleteTag is the RC tag (v1.0.0-rc.5)
	// We need to find the release by the RC tag since that's what it's currently tagged as
	searchTag := opts.Tag
	if opts.DeleteTag != "" {
		searchTag = opts.DeleteTag
	}

	existing, err := m.findRelease(searchTag, opts.SHA)
	if err != nil {
		return nil, err
	}

	// Re-run convergence: a publish that already patched the release to the semver
	// tag and cleaned up the rc tags will not resolve by the rc tag on a rerun.
	// Fall back to the semver tag so a partially completed publish (semver tag
	// created, rc cleanup done, but the manifest write failed) still finds the
	// already-published object and converges instead of erroring.
	if existing == nil && searchTag != opts.Tag {
		existing, err = m.findRelease(opts.Tag, opts.SHA)
		if err != nil {
			return nil, err
		}
	}

	if existing == nil {
		return nil, fmt.Errorf("no release found for tag %s", searchTag)
	}

	// Create the semver tag (v1.0.0) pointing to the same commit
	if err := m.createGitTag(opts.Tag, opts.SHA); err != nil {
		return nil, fmt.Errorf("creating semver tag: %w", err)
	}

	cleanBody := removeStatusLine(existing.Body)

	// Update the release to use the semver tag and publish it
	payload := map[string]interface{}{
		"tag_name":   opts.Tag, // Update to semver tag (v1.0.0)
		"name":       opts.Tag,
		"body":       cleanBody,
		"draft":      false,
		"prerelease": false,
	}

	release, err := m.apiRequest("PATCH", fmt.Sprintf("/releases/%d", existing.ID), payload)
	if err != nil {
		return nil, fmt.Errorf("publishing release: %w", err)
	}

	// Clean up all RC tags for this base version (v1.0.0-rc.0, v1.0.0-rc.1, etc.)
	// This is safe because we've already created the semver tag
	if err := m.cleanupRCTags(opts.Tag); err != nil {
		// Log warning but don't fail - cleanup is best-effort
		fmt.Printf("Warning: failed to cleanup RC tags: %v\n", err)
	}

	return &Result{
		ReleaseID:  release.ID,
		ReleaseURL: release.URL,
		HTMLURL:    release.HTMLURL,
	}, nil
}

func (m *Manager) delete(opts Options) (*Result, error) {
	existing, err := m.findRelease(opts.Tag, opts.SHA)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		// No release to delete, return empty result
		return &Result{}, nil
	}

	// Guard against destroying a real published release. The hotfix-rejoin
	// cleanup opts in via AllowPublishedDelete because the release it removes is a
	// superseded hotfix prerelease (non-draft), not a release a user cares about.
	if !existing.Draft && !opts.AllowPublishedDelete {
		return nil, fmt.Errorf("cannot delete non-draft release %s without AllowPublishedDelete", opts.Tag)
	}

	result := &Result{
		ReleaseID:  existing.ID,
		ReleaseURL: existing.URL,
		HTMLURL:    existing.HTMLURL,
	}

	endpoint := fmt.Sprintf("/releases/%d", existing.ID)
	req, err := m.newRequest("DELETE", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("delete request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("delete failed with status %d: %s", resp.StatusCode, string(body))
	}

	return result, nil
}

// findRelease searches for a release by tag and/or SHA.
// It tries multiple strategies in order of reliability:
// 1. Direct tag endpoint (fast, works for published releases)
// 2. Search by tag_name or name field (handles untagged drafts)
// 3. Search by target_commitish/SHA (most reliable for drafts, avoids tag indexing race)
func (m *Manager) findRelease(tag, sha string) (*GitHubRelease, error) {
	// First try the direct endpoint (works for published releases)
	if tag != "" {
		endpoint := fmt.Sprintf("/releases/tags/%s", tag)
		req, err := m.newRequest("GET", endpoint, nil)
		if err != nil {
			return nil, err
		}

		resp, err := m.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("API request failed: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == http.StatusOK {
			var release GitHubRelease
			if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
				return nil, fmt.Errorf("decoding response: %w", err)
			}
			return &release, nil
		}

		if resp.StatusCode != http.StatusNotFound {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
		}
	}

	// Search all releases for matching tag or SHA
	return m.findReleaseByTagOrSHA(tag, sha)
}

// listRetryAttempts is the total number of attempts when scanning the release
// list for a recently created draft. GitHub's release-list endpoint has an
// eventual-consistency window of a few seconds after draft creation; bounded
// retries prevent a spurious "no release found" error during that window.
const listRetryAttempts = 4

// listRetryBackoff is the base backoff between consecutive list attempts. The
// actual sleep per attempt is attempt*listRetryBackoff (linear). Tests inject a
// no-op sleepFn so no real time passes.
const listRetryBackoff = 2 * time.Second

// findReleaseByTagOrSHA searches all releases (including drafts) for a matching
// tag_name, name, or SHA. SHA matching is most reliable for recently created
// drafts.
//
// When the first list response is empty (GitHub's release-list endpoint has an
// eventual-consistency window after draft creation), the function retries with a
// short backoff before concluding the release does not exist. The retry is
// bounded (listRetryAttempts total) so the function always terminates.
func (m *Manager) findReleaseByTagOrSHA(tag, sha string) (*GitHubRelease, error) {
	sleep := m.sleepFn
	if sleep == nil {
		sleep = time.Sleep
	}

	for attempt := 0; attempt < listRetryAttempts; attempt++ {
		if attempt > 0 {
			sleep(time.Duration(attempt) * listRetryBackoff)
		}

		req, err := m.newRequest("GET", "/releases?per_page=100", nil)
		if err != nil {
			return nil, err
		}

		resp, err := m.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("API request failed: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
		}

		var releases []GitHubRelease
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("decoding response: %w", err)
		}
		_ = resp.Body.Close()

		// Resolve the best match in priority order so a stale draft sharing a
		// target_commitish cannot win over the intended release:
		//   1. a draft whose tag matches exactly (strongest signal),
		//   2. a draft matched only by SHA (fallback for a tagless object whose
		//      tag a prior partial run already removed),
		//   3. any non-draft (published/prerelease) match.
		// An exact tag match is preferred over a SHA-only match because two drafts
		// can share a target_commitish; the tag disambiguates.
		var shaOnlyDraft *GitHubRelease
		var nonDraft *GitHubRelease
		for i := range releases {
			tagMatch := tag != "" && (releases[i].TagName == tag || releases[i].Name == tag)
			shaMatch := sha != "" && releases[i].TargetCommitish == sha
			if !tagMatch && !shaMatch {
				continue
			}
			if releases[i].Draft {
				if tagMatch {
					// Strongest signal - return immediately.
					return &releases[i], nil
				}
				if shaOnlyDraft == nil {
					shaOnlyDraft = &releases[i]
				}
				continue
			}
			if nonDraft == nil {
				nonDraft = &releases[i]
			}
		}

		if shaOnlyDraft != nil {
			return shaOnlyDraft, nil
		}
		if nonDraft != nil {
			return nonDraft, nil
		}
		// found == nil: list returned nothing matching - may be a consistency
		// window; retry on next iteration unless this was the last attempt.
	}

	// All attempts exhausted; release genuinely not found.
	return nil, nil
}

func (m *Manager) apiRequest(method, endpoint string, payload map[string]interface{}) (*GitHubRelease, error) {
	req, err := m.newRequest(method, endpoint, payload)
	if err != nil {
		return nil, err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &release, nil
}

func (m *Manager) newRequest(method, endpoint string, payload map[string]interface{}) (*http.Request, error) {
	// Dry-run backstop: every release and tag mutation funnels through here, so
	// refusing non-read methods under --dry-run catches any caller that missed
	// its explicit gate before the request can leave the process.
	if method != http.MethodGet && method != http.MethodHead {
		if err := globals.GuardMutation(fmt.Sprintf("%s %s on %s", method, endpoint, m.repo)); err != nil {
			return nil, err
		}
	}

	url := fmt.Sprintf("%s/repos/%s%s", m.baseURL, m.repo, endpoint)

	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encoding payload: %w", err)
		}
		body = strings.NewReader(string(data))
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return req, nil
}

// capitalizeFirst capitalizes the first letter of a string
func capitalizeFirst(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// generateReleaseName creates the release name from tag
func generateReleaseName(env, tag string) string {
	return tag
}

// generateStatusLine creates the status line for release body
func generateStatusLine(env string) string {
	return fmt.Sprintf("## Status: Deployed to %s", capitalizeFirst(env))
}

// addStatusLine prepends status line to changelog
// Skips status for prerelease (not a real deployment environment)
func addStatusLine(changelog, env string) string {
	if env == "" || env == "prerelease" {
		return changelog
	}
	statusLine := generateStatusLine(env)
	return statusLine + "\n\n" + changelog
}

// removeStatusLine removes the status line from body
func removeStatusLine(body string) string {
	// Remove lines starting with "## Status:"
	re := regexp.MustCompile(`(?m)^## Status:.*\n?`)
	result := re.ReplaceAllString(body, "")
	// Clean up extra blank lines at the start
	result = strings.TrimLeft(result, "\n")
	return result
}

// updateStatusLine replaces the status line in an existing body with a new environment
// For prerelease, removes status line entirely (not a real deployment)
func updateStatusLine(body, env string) string {
	cleanBody := removeStatusLine(body)
	if env == "" || env == "prerelease" {
		return cleanBody
	}
	return addStatusLine(cleanBody, env)
}
