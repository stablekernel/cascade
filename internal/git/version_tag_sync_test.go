package git_test

import (
	"testing"

	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/version"
)

// TestIsValidVersionTag_InSyncWithVersionParse guards against the git package's
// local version predicate drifting from the canonical parser in internal/version.
// The git package cannot import internal/version directly (that would create an
// import cycle through internal/changelog), so this external test asserts the two
// agree across a representative corpus of tag strings.
func TestIsValidVersionTag_InSyncWithVersionParse(t *testing.T) {
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
			_, err := version.Parse(tag)
			wantValid := err == nil
			if got := git.IsValidVersionTag(tag); got != wantValid {
				t.Errorf("IsValidVersionTag(%q) = %v, but version.Parse success = %v (predicate drifted from canonical regex)", tag, got, wantValid)
			}
		})
	}
}
