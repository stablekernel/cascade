package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// releaseIdempotencyScript locates release-idempotency.sh by walking up from the
// test's working directory to the repo's go.mod, then joining the known script
// path. This keeps the test independent of where go test runs it from.
func releaseIdempotencyScript(t *testing.T) string {
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

	script := filepath.Join(dir, ".github", "scripts", "release-idempotency.sh")
	_, err = os.Stat(script)
	require.NoError(t, err, "release-idempotency.sh not found at %s", script)
	return script
}

// runReleaseIdempotency drives the script with an injected release JSON (via
// CASCADE_RELEASE_JSON, so the test needs no gh or network) and the given tag,
// returning the trimmed stdout decision ("true" to skip, "false" to build).
func runReleaseIdempotency(t *testing.T, tag, releaseJSON string) string {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; skipping shell script test")
	}
	if _, jqErr := exec.LookPath("jq"); jqErr != nil {
		t.Skip("jq not available; skipping shell script test")
	}

	script := releaseIdempotencyScript(t)
	cmd := exec.Command(bash, script, tag)
	cmd.Env = append(os.Environ(), "CASCADE_RELEASE_JSON="+releaseJSON)

	out, runErr := cmd.CombinedOutput()
	require.NoError(t, runErr, "script should succeed; output: %s", out)
	return strings.TrimSpace(string(out))
}

func TestReleaseIdempotency_PublishedReleaseWithChecksumsSkips(t *testing.T) {
	// A live (non-draft) release carrying the checksum manifest is the winner of
	// the double-trigger: a duplicate run must skip and no-op green rather than
	// racing GoReleaser into a 422.
	json := `{"isDraft":false,"assets":[{"name":"cascade_1.0.0_linux_amd64.tar.gz"},{"name":"checksums.txt"}]}`
	require.Equal(t, "true", runReleaseIdempotency(t, "v1.0.0", json))
}

func TestReleaseIdempotency_AbsentReleaseBuilds(t *testing.T) {
	// No release yet (empty JSON injected): this run is the one that must build.
	require.Equal(t, "false", runReleaseIdempotency(t, "v1.0.0", ""))
}

func TestReleaseIdempotency_DraftReleaseBuilds(t *testing.T) {
	// A draft release is not a completed publish, so the job must proceed. This
	// covers the stray-draft residue a failed loser run can leave behind.
	json := `{"isDraft":true,"assets":[{"name":"checksums.txt"}]}`
	require.Equal(t, "false", runReleaseIdempotency(t, "v1.0.0", json))
}

func TestReleaseIdempotency_ReleaseMissingChecksumsBuilds(t *testing.T) {
	// A release that exists but has not yet received its checksum manifest is the
	// winner still mid-upload. Skipping here would drop assets, so the job builds
	// and the release.yaml 422 belt absorbs any simultaneous double-publish.
	json := `{"isDraft":false,"assets":[{"name":"cascade_1.0.0_linux_amd64.tar.gz"}]}`
	require.Equal(t, "false", runReleaseIdempotency(t, "v1.0.0", json))
}

func TestReleaseIdempotency_ReleaseWithNoAssetsBuilds(t *testing.T) {
	json := `{"isDraft":false,"assets":[]}`
	require.Equal(t, "false", runReleaseIdempotency(t, "v1.0.0", json))
}

func TestReleaseIdempotency_RequiresExactlyOneArgument(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; skipping shell script test")
	}
	script := releaseIdempotencyScript(t)

	cmd := exec.Command(bash, script)
	out, runErr := cmd.CombinedOutput()
	require.Error(t, runErr, "missing argument must be a usage error; output: %s", out)
}
