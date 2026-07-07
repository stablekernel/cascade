package git

import (
	"testing"

	"github.com/stablekernel/cascade/internal/taggrammar"
)

// TestIsValidVersionTag_MatchesDefaultGrammar pins the git predicate to the
// canonical grammar. Both the git package and version discovery now read tag
// shape from internal/taggrammar, so the old cross-package drift class is gone;
// this test keeps a representative corpus honest against the default spec.
func TestIsValidVersionTag_MatchesDefaultGrammar(t *testing.T) {
	spec := taggrammar.Default()
	corpus := []string{
		"v1.2.3",
		"v0.5.1",
		"v0.0.0",
		"v10.20.30",
		"v1.0.1-rc.0",
		"v1.0.1-rc.42",
		"v1.0.1-rc.4.hotfix.5",
		"1.2.3",
		"release1.2.3",
		"v0.6.0-dryrun.1",
		"v0.6.0-dryrun.10",
		"vnightly",
		"nightly",
		"latest",
		"v1.2",
		"v1.2.3.4",
		"v1.2.3-rc",
		"v1.2.3-rc.x",
		"v1.2.3-hotfix.1",
		"v-1.2.3",
		"",
		"vlatest",
	}

	for _, tag := range corpus {
		tag := tag
		t.Run(tag, func(t *testing.T) {
			want := spec.IsVersionTag(tag)
			if got := IsValidVersionTag(tag); got != want {
				t.Errorf("IsValidVersionTag(%q) = %v, want %v (predicate drifted from the canonical grammar)", tag, got, want)
			}
		})
	}
}
