package release

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/taggrammar"
)

// newReapTestManager builds a Manager whose tag-list endpoint returns listedTags
// and whose git-ref DELETE endpoint records the deleted tag names into the
// returned slice pointer. Options thread the per-component grammar under test.
func newReapTestManager(t *testing.T, listedTags []string, opts ...Option) (*Manager, *[]string) {
	t.Helper()
	deleted := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/git/refs/tags" {
			refs := make([]map[string]string, 0, len(listedTags))
			for _, tag := range listedTags {
				refs = append(refs, map[string]string{"ref": "refs/tags/" + tag})
			}
			_ = json.NewEncoder(w).Encode(refs)
			return
		}
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/git/refs/tags/") {
			tag := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/git/refs/tags/")
			*deleted = append(*deleted, tag)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	mgr := NewManagerWithURL("owner/repo", "test-token", server.URL, opts...)
	return mgr, deleted
}

// componentSpec returns a strict per-component grammar with the given prefix and
// pre-release token, mirroring ResolvedComponent.TagGrammarSpec (StrictPrefix on).
func componentSpec(prefix, token string) taggrammar.Spec {
	spec := taggrammar.Default()
	spec.Prefix = prefix
	spec.PreReleaseToken = token
	spec.StrictPrefix = true
	return spec
}

// TestCleanupRCTags_ComponentNamespaceIsolation proves that a component's reaper,
// threaded with its strict tag grammar, reaps only its own RC tags and never a
// sibling component's tags, even when the sibling's base version is lower than the
// published version (the superseded-base enumeration must not cross namespaces)
// and even when the sibling uses a different pre-release token.
func TestCleanupRCTags_ComponentNamespaceIsolation(t *testing.T) {
	listed := []string{
		// Component A ("api-") - the publishing component.
		"api-0.9.0-rc.0", // superseded earlier base - reap
		"api-1.0.0-rc.0", // below published base - reap
		"api-1.0.1-rc.0", // equal to published base - reap
		"api-1.0.1-rc.3", // equal to published base - reap
		"api-1.1.0-rc.0", // higher base, future work - preserve
		// Component B ("web-") default token, lower and equal bases - must be
		// preserved despite being <= the published numeric base.
		"web-0.5.0-rc.0",
		"web-1.0.0-rc.0",
		"web-1.0.1-rc.0",
		// Component C ("svc-") custom "beta" token, lower base - must be preserved.
		"svc-0.8.0-beta.0",
		"svc-1.0.0-beta.0",
	}

	mgr, deleted := newReapTestManager(t, listed, WithTagGrammar(componentSpec("api-", "rc")))

	err := mgr.cleanupRCTags("api-1.0.1")
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{
		"api-0.9.0-rc.0",
		"api-1.0.0-rc.0",
		"api-1.0.1-rc.0",
		"api-1.0.1-rc.3",
	}, *deleted, "only component A's RC tags at or below the published base are reaped")

	// No sibling tag is ever touched, including lower-base siblings.
	for _, sibling := range []string{
		"web-0.5.0-rc.0", "web-1.0.0-rc.0", "web-1.0.1-rc.0",
		"svc-0.8.0-beta.0", "svc-1.0.0-beta.0", "api-1.1.0-rc.0",
	} {
		assert.NotContains(t, *deleted, sibling)
	}
}

// TestCleanupRCTags_CustomGrammarIsReaped proves the accumulation bug is fixed: a
// component whose tag grammar uses a non-default pre-release token ("beta") has
// its RC tags matched and reaped, where the hardcoded "-rc." matcher never would.
func TestCleanupRCTags_CustomGrammarIsReaped(t *testing.T) {
	listed := []string{
		"svc-1.0.0-beta.0",
		"svc-1.0.0-beta.1",
		"svc-1.0.1-beta.0",
		"svc-1.1.0-beta.0", // higher base - preserve
	}

	mgr, deleted := newReapTestManager(t, listed, WithTagGrammar(componentSpec("svc-", "beta")))

	err := mgr.cleanupRCTags("svc-1.0.1")
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{
		"svc-1.0.0-beta.0",
		"svc-1.0.0-beta.1",
		"svc-1.0.1-beta.0",
	}, *deleted, "custom-token RC tags at or below the published base are reaped")
	assert.NotContains(t, *deleted, "svc-1.1.0-beta.0")
}

// TestCleanupRCTags_CustomGrammarSkipsHotfixVariants proves a nested hotfix
// variant under a custom grammar is not treated as a plain RC tag, matching the
// default-grammar contract that hotfix tags are reaped by the hotfix-rejoin path.
func TestCleanupRCTags_CustomGrammarSkipsHotfixVariants(t *testing.T) {
	listed := []string{
		"svc-1.0.1-beta.0",
		"svc-1.0.1-beta.1.hotfix.1", // hotfix variant - preserve
	}

	mgr, deleted := newReapTestManager(t, listed, WithTagGrammar(componentSpec("svc-", "beta")))

	err := mgr.cleanupRCTags("svc-1.0.1")
	require.NoError(t, err)

	assert.Equal(t, []string{"svc-1.0.1-beta.0"}, *deleted)
	assert.NotContains(t, *deleted, "svc-1.0.1-beta.1.hotfix.1")
}

// TestCleanupRCTags_NoGrammarMatchesLegacyReaper proves the single-component (no
// component context) path is behavior-identical to the historical reaper: the
// permissive prefix and hardcoded "-rc." matching reap exactly the same tags as
// before threading was introduced.
func TestCleanupRCTags_NoGrammarMatchesLegacyReaper(t *testing.T) {
	listed := []string{
		"v0.9.0-rc.0",          // below published base - reap
		"v1.0.0-rc.0",          // superseded earlier base - reap
		"v1.0.1-rc.0",          // equal to published base - reap
		"v1.0.1-rc.2",          // equal to published base - reap
		"v1.1.0-rc.0",          // higher base - preserve
		"rel-1.0.0-rc.0",       // different prefix - preserve
		"v1.0.1-rc.1.hotfix.1", // hotfix variant - preserve
	}

	// No WithTagGrammar option: the legacy permissive path.
	mgr, deleted := newReapTestManager(t, listed)

	err := mgr.cleanupRCTags("v1.0.1")
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{
		"v0.9.0-rc.0",
		"v1.0.0-rc.0",
		"v1.0.1-rc.0",
		"v1.0.1-rc.2",
	}, *deleted)
	assert.NotContains(t, *deleted, "v1.1.0-rc.0")
	assert.NotContains(t, *deleted, "rel-1.0.0-rc.0")
	assert.NotContains(t, *deleted, "v1.0.1-rc.1.hotfix.1")
}
