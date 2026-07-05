package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// previousStableTagScript locates previous-stable-tag.sh by walking up from the
// test's working directory to the repo's go.mod, then joining the known script
// path. This keeps the test independent of where go test runs it from.
func previousStableTagScript(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			break
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "reached filesystem root without finding go.mod")
		dir = parent
	}

	script := filepath.Join(dir, ".github", "scripts", "previous-stable-tag.sh")
	_, err = os.Stat(script)
	require.NoError(t, err, "previous-stable-tag.sh not found at %s", script)
	return script
}

// runPreviousStableTag drives the script with an injected tag list (via
// CASCADE_TAG_LIST, so the test needs no git repo) and the given current tag,
// returning the trimmed stdout.
func runPreviousStableTag(t *testing.T, tags []string, current string) string {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; skipping shell script test")
	}

	script := previousStableTagScript(t)
	cmd := exec.Command(bash, script, current)
	cmd.Env = append(os.Environ(), "CASCADE_TAG_LIST="+strings.Join(tags, "\n"))

	out, runErr := cmd.CombinedOutput()
	require.NoError(t, runErr, "script should succeed; output: %s", out)
	return strings.TrimSpace(string(out))
}

func TestPreviousStableTag_FinalWithPriorStableSkipsPrereleases(t *testing.T) {
	// A final current tag returns the previous stable, ignoring the candidate
	// and dry-run tags that share the release cycle (the bug's exact shape:
	// v0.8.0 and v0.8.0-rc.7 point at the same commit).
	tags := []string{"v0.6.0", "v0.7.0", "v0.8.0-rc.1", "v0.8.0-rc.7", "v0.8.0-dryrun.2", "v0.8.0"}
	require.Equal(t, "v0.7.0", runPreviousStableTag(t, tags, "v0.8.0"))
}

func TestPreviousStableTag_FinalWithOnlyPrereleasesBelowReturnsEmpty(t *testing.T) {
	tags := []string{"v0.8.0-rc.1", "v0.8.0-rc.7", "v0.8.0"}
	require.Equal(t, "", runPreviousStableTag(t, tags, "v0.8.0"))
}

func TestPreviousStableTag_FirstStableReturnsEmpty(t *testing.T) {
	tags := []string{"v0.1.0"}
	require.Equal(t, "", runPreviousStableTag(t, tags, "v0.1.0"))
}

func TestPreviousStableTag_MultiplePriorStablesReturnsHighestBelow(t *testing.T) {
	// v0.10.0 sorts above v0.8.0 by semver (not lexically) and must be excluded;
	// v0.7.0 is the greatest stable strictly below the current tag.
	tags := []string{"v0.5.0", "v0.6.0", "v0.7.0", "v0.10.0", "v0.8.0"}
	require.Equal(t, "v0.7.0", runPreviousStableTag(t, tags, "v0.8.0"))
}

func TestPreviousStableTag_PrereleaseCurrentComputesStableBelow(t *testing.T) {
	// A prerelease current tag still resolves the stable release below its base
	// version (the workflow only uses the value for finals, but the behaviour is
	// well defined either way).
	tags := []string{"v0.6.0", "v0.7.0", "v0.8.0-rc.7"}
	require.Equal(t, "v0.7.0", runPreviousStableTag(t, tags, "v0.8.0-rc.7"))
}

func TestPreviousStableTag_EmptyTagListReturnsEmpty(t *testing.T) {
	require.Equal(t, "", runPreviousStableTag(t, []string{}, "v0.8.0"))
}

func TestPreviousStableTag_RequiresExactlyOneArgument(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; skipping shell script test")
	}
	script := previousStableTagScript(t)

	cmd := exec.Command(bash, script)
	out, runErr := cmd.CombinedOutput()
	require.Error(t, runErr, "missing argument must be a usage error; output: %s", out)
}
