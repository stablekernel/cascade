package release

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// bumpSuiteBootstrapPinScript locates the bootstrap-pin rewrite script by
// walking up from the test's working directory to the repo's go.mod, then
// joining the known script path. This keeps the test independent of the
// directory go test happens to run it from.
func bumpSuiteBootstrapPinScript(t *testing.T) string {
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

	script := filepath.Join(dir, ".github", "scripts", "bump-suite-bootstrap-pin.sh")
	_, err = os.Stat(script)
	require.NoError(t, err, "bump-suite-bootstrap-pin.sh not found at %s", script)
	return script
}

// runBumpPin writes content to a temp suite file, runs the rewrite script
// against it with the given target version, and returns the script's exit code,
// combined output, and the file's contents afterward.
func runBumpPin(t *testing.T, content, target string) (exitCode int, output, after string) {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; skipping shell rewrite test")
	}

	script := bumpSuiteBootstrapPinScript(t)
	suite := filepath.Join(t.TempDir(), "scenario-suite.yaml")
	require.NoError(t, os.WriteFile(suite, []byte(content), 0o600))

	cmd := exec.Command(bash, script, suite, target)
	out, runErr := cmd.CombinedOutput()

	code := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("unexpected error running script: %v (output: %s)", runErr, out)
		}
	}

	afterBytes, readErr := os.ReadFile(suite)
	require.NoError(t, readErr)
	return code, string(out), string(afterBytes)
}

func TestBumpSuiteBootstrapPin_RewritesBothPins(t *testing.T) {
	before := "" +
		"      - name: Setup CLI\n" +
		"        uses: stablekernel/cascade/.github/actions/setup-cli@v0.1.0\n" +
		"        with:\n" +
		"          version: v0.1.0\n"

	code, out, after := runBumpPin(t, before, "v0.8.0")

	require.Equal(t, 0, code, "script should succeed; output: %s", out)
	require.Contains(t, after, "setup-cli@v0.8.0", "action ref should be bumped")
	require.Contains(t, after, "version: v0.8.0", "version input should be bumped")
	require.NotContains(t, after, "v0.1.0", "no stale pin should remain")
}

func TestBumpSuiteBootstrapPin_Idempotent(t *testing.T) {
	before := "" +
		"        uses: stablekernel/cascade/.github/actions/setup-cli@v0.8.0\n" +
		"          version: v0.8.0\n"

	code, out, after := runBumpPin(t, before, "v0.8.0")

	require.Equal(t, 0, code, "script should succeed; output: %s", out)
	require.Equal(t, before, after, "a suite already at the target must be left byte-for-byte unchanged")
	require.Contains(t, out, "no change")
}

func TestBumpSuiteBootstrapPin_LeavesMovingRefUntouched(t *testing.T) {
	before := "" +
		"        uses: stablekernel/cascade/.github/actions/setup-cli@main\n" +
		"          # no pinned version input\n"

	code, out, after := runBumpPin(t, before, "v0.8.0")

	require.Equal(t, 0, code, "script should succeed; output: %s", out)
	require.Equal(t, before, after, "a moving ref carries no semver pin and must not be rewritten")
}

func TestBumpSuiteBootstrapPin_MultiDigitVersions(t *testing.T) {
	before := "" +
		"        uses: stablekernel/cascade/.github/actions/setup-cli@v1.20.30\n" +
		"          version: v1.20.30\n"

	code, out, after := runBumpPin(t, before, "v2.100.5")

	require.Equal(t, 0, code, "script should succeed; output: %s", out)
	require.Contains(t, after, "setup-cli@v2.100.5")
	require.Contains(t, after, "version: v2.100.5")
	require.NotContains(t, after, "v1.20.30")
}

func TestBumpSuiteBootstrapPin_RejectsPrereleaseTarget(t *testing.T) {
	before := "        uses: stablekernel/cascade/.github/actions/setup-cli@v0.1.0\n"

	for _, bad := range []string{"v0.8.0-rc.1", "v0.8.0-dryrun.2", "0.8.0", "latest", "main"} {
		t.Run(bad, func(t *testing.T) {
			code, out, after := runBumpPin(t, before, bad)
			require.Equal(t, 2, code, "a non-final target must be a usage error; output: %s", out)
			require.Equal(t, before, after, "the suite must not be touched when the target is rejected")
		})
	}
}

func TestBumpSuiteBootstrapPin_MissingFile(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; skipping shell rewrite test")
	}
	script := bumpSuiteBootstrapPinScript(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	cmd := exec.Command(bash, script, missing, "v0.8.0")
	out, runErr := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	require.True(t, errors.As(runErr, &exitErr), "expected a non-zero exit; output: %s", out)
	require.Equal(t, 1, exitErr.ExitCode(), "a missing suite file exits 1; output: %s", out)
}
