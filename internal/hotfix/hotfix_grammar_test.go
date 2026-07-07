package hotfix

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/taggrammar"
	"github.com/stretchr/testify/require"
)

func sptr(s string) *string { return &s }

// betaSpec is the tag grammar for a repo whose pre-release token is "beta"
// instead of the historical "rc".
func betaSpec() taggrammar.Spec {
	cfg := &config.TrunkConfig{
		TagGrammar: &config.TagGrammarConfig{PreReleaseToken: sptr("beta")},
	}
	return cfg.ResolveTagGrammar()
}

// TestAllocateVersion_CustomToken proves the finalize allocator can parse and
// advance a beta-token pre-release: the default-spec version.Parse would fail on
// the beta token and abort the hotfix entirely.
func TestAllocateVersion_CustomToken(t *testing.T) {
	f := &Finalizer{
		cicd: &config.CICDFile{Config: &config.TrunkConfig{
			TagGrammar: &config.TagGrammarConfig{PreReleaseToken: sptr("beta")},
		}},
		tagLister: stubTagLister{},
	}

	got, err := f.allocateVersion("1.4.0-beta.2")
	require.NoError(t, err)
	require.Equal(t, "1.4.0-beta.2.hotfix.1", got)
}

// TestHotfixTagsForBase_CustomToken proves cleanup collects beta-token hotfix
// tags for their beta base; the default-spec parse would reject the token and
// return nothing, leaking the tags.
func TestHotfixTagsForBase_CustomToken(t *testing.T) {
	tags := []string{
		"1.4.0-beta.2.hotfix.1",
		"1.4.0-beta.3.hotfix.1",
		"v2.0.0",
	}
	got := HotfixTagsForBase(betaSpec(), "1.4.0-beta.2", tags)
	require.Equal(t, []string{"1.4.0-beta.2.hotfix.1"}, got)
}

// TestHotfixVersionCandidate_CustomToken proves the planner computes the next
// hotfix version under a custom token instead of hard-failing on parse.
func TestHotfixVersionCandidate_CustomToken(t *testing.T) {
	got, err := hotfixVersionCandidate(betaSpec(), "1.4.0-beta.2")
	require.NoError(t, err)
	require.Equal(t, "1.4.0-beta.2.hotfix.1", got)
}
