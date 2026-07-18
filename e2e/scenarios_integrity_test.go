package e2e

import (
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/e2e/harness"
	"github.com/stretchr/testify/require"
)

// TestMultiRepoScenarios_RealCorpusPassesDiscovery is the multi-repo counterpart
// to TestMultiStepScenarios_RealCorpusPassesDiscovery. Discovery parses YAML and
// checks expectation subjects against each scenario's own config, so it needs no
// Docker and belongs in the inner loop. Without it the strict checks only ever
// ran where Docker is, long after the mistake.
func TestMultiRepoScenarios_RealCorpusPassesDiscovery(t *testing.T) {
	scenarios, err := harness.DiscoverMultiRepoScenarios("scenarios")
	require.NoError(t, err, "the checked-in multi-repo scenarios must pass discovery")
	require.NotEmpty(t, scenarios, "discovery found no multi-repo scenarios; the corpus or its path moved")
}

// TestScenarios_AreNotGitIgnored is the cheap, permanent guard against a whole
// class of invisible loss: a scenario that exists on disk, is git-ignored, and
// therefore never reaches CI. The author sees it pass (or sees nothing at all),
// git status hides it, and the coverage it claims does not exist.
//
// This is not hypothetical. The root .gitignore carried an unanchored `cascade-*`
// entry meant for build binaries, which also swallowed any scenario whose name
// began with "cascade-". One did, and it never ran.
func TestScenarios_AreNotGitIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Walk the whole tree so both tiers are covered: root-level scenarios
	// (scenarios/02-two-env-repo.yaml) and subdirectory scenarios
	// (scenarios/hotfix/x.yaml). A single-depth glob missed the root tier, the
	// very tier the `cascade-*` incident struck, leaving it invisible again.
	var files []string
	err := filepath.WalkDir("scenarios", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext == ".yaml" || ext == ".yml" {
			files = append(files, path)
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, files, "no scenario files found; the corpus or its path moved")

	// check-ignore exits 0 when it matched at least one path, 1 when it matched
	// none, and >1 on a real fault. Matching none is the passing case.
	cmd := exec.Command("git", "check-ignore", "--stdin")
	cmd.Stdin = strings.NewReader(strings.Join(files, "\n"))
	out, err := cmd.Output()

	if err == nil {
		t.Fatalf("these scenario files are git-ignored, so they can never reach CI:\n%s\n"+
			"Fix the ignore rule (anchor it, or scope it to the artifact's real location) rather than renaming the scenario.",
			strings.TrimSpace(string(out)))
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return // no scenario is ignored
	}
	require.NoError(t, err, "git check-ignore failed")
}
