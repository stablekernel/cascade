package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// anchorWorkflowPath is the dependabot anchor workflow, relative to the repo's
// .github directory. It gives the github-actions updater one well-known file
// that references every action pinned in action_pins.yaml.
const anchorWorkflowPath = "workflows/action-pins.yml"

// TestActionPinsAnchorCoversManifest keeps the dependabot anchor a complete and
// correct mirror of action_pins.yaml.
//
// Completeness matters because Dependabot only bumps actions it can see in a
// scanned file. The generated workflows (orchestrate.yaml, promote.yaml) are
// excluded from Dependabot in .github/dependabot.yml, so an action that appears
// only there (for example actions/create-github-app-token) would never get a
// bump PR without a tracked home in the anchor.
//
// Correctness (each anchor ref matches the manifest sha and version) is also
// enforced repo-wide by TestWorkflowsConsistentWithActionPins; asserting it here
// too keeps the anchor's purpose self-contained, so a stale anchor is reported
// against the manifest directly rather than only as one row in the repo sweep.
func TestActionPinsAnchorCoversManifest(t *testing.T) {
	manifest := loadActionPinsManifest(t)
	githubDir := repoGitHubDir(t)

	content, err := os.ReadFile(filepath.Join(githubDir, anchorWorkflowPath)) //nolint:gosec // fixed path under the repo's .github tree.
	require.NoError(t, err)

	// Correctness: no governed uses: line in the anchor may diverge from the manifest.
	mismatches := scanUsesForPinDrift(anchorWorkflowPath, string(content), manifest)
	require.Emptyf(t, mismatches, "dependabot anchor refs diverge from action_pins.yaml: %v", mismatches)

	// Completeness: every manifest action must appear in the anchor so Dependabot
	// has a tracked bump entrypoint for it.
	anchored := make(map[string]bool)
	for _, line := range strings.Split(string(content), "\n") {
		if groups := usesRefRe.FindStringSubmatch(line); groups != nil {
			anchored[groups[1]] = true
		}
	}
	for action := range manifest {
		require.Truef(t, anchored[action],
			"action %q is in action_pins.yaml but missing from the dependabot anchor %s; "+
				"add a `uses: %s@<sha> # <version>` step so Dependabot can bump it",
			action, anchorWorkflowPath, action)
	}
}
