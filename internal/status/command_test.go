package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureManifest writes a multi-env manifest to a temp dir and returns its path.
func fixtureManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	content := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - staging
      - prod
    builds:
      - name: app
        workflow: .github/workflows/build.yaml
        triggers:
          - "src/**"
    deploys:
      - name: services
        workflow: .github/workflows/deploy.yaml
        triggers:
          - "deploy/**"
  state:
    dev:
      sha: abc123def456
      version: v1.2.3-rc.1
      committed_at: "2026-01-01T10:00:00Z"
      committed_by: alice
      builds:
        app:
          sha: abc123def456
          built_at: "2026-01-01T09:50:00Z"
          built_by: alice
          artifact_id: sha256:aabbcc
      deploys:
        services:
          sha: abc123def456
          deployed_at: "2026-01-01T10:05:00Z"
          deployed_by: alice
    staging:
      sha: 111aaa
      version: v1.2.2
      committed_at: "2025-12-20T08:00:00Z"
      committed_by: bob
    prod:
      sha: ""
      version: ""
  latest_release:
    version: v1.2.2
    sha: 111aaa
    released_on: "2025-12-20T08:00:00Z"
    released_by: bob
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// captureOutput redirects os.Stdout to a pipe, runs fn, then returns the
// printed text.  JSON or human-readable output is captured the same way.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	require.NoError(t, w.Close())
	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, rerr := r.Read(tmp)
		buf.Write(tmp[:n])
		if rerr != nil {
			break
		}
	}
	return buf.String()
}

// -------- all-envs summary --------

func TestRunStatusAll_HumanReadable(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runStatusAll(path, "ci", false)
		require.NoError(t, err)
	})

	assert.Contains(t, out, "environment: dev")
	assert.Contains(t, out, "v1.2.3-rc.1")
	assert.Contains(t, out, "abc123def456")
	assert.Contains(t, out, "environment: staging")
	assert.Contains(t, out, "environment: prod")
	assert.Contains(t, out, "latest_release:")
	assert.Contains(t, out, "v1.2.2")
}

func TestRunStatusAll_JSON(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runStatusAll(path, "ci", true)
		require.NoError(t, err)
	})

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	envs, ok := result["environments"].(map[string]interface{})
	require.True(t, ok, "environments key should be a map")
	assert.Contains(t, envs, "dev")
	assert.Contains(t, envs, "staging")
	assert.Contains(t, envs, "prod")

	lr, ok := result["latest_release"].(map[string]interface{})
	require.True(t, ok, "latest_release key should be a map")
	assert.Equal(t, "v1.2.2", lr["version"])
}

func TestRunStatusAll_EmptyState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	content := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	out := captureOutput(t, func() {
		err := runStatusAll(path, "ci", false)
		require.NoError(t, err)
	})
	// dev env should appear even though state fields are empty
	assert.Contains(t, out, "environment: dev")
}

func TestRunStatusAll_MissingFile(t *testing.T) {
	err := runStatusAll("/no/such/manifest.yaml", "ci", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading manifest")
}

// -------- status env --------

func TestRunEnv_KnownEnv(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runEnv(path, "ci", false, "dev")
		require.NoError(t, err)
	})

	assert.Contains(t, out, "environment: dev")
	assert.Contains(t, out, "v1.2.3-rc.1")
	assert.Contains(t, out, "abc123def456")
	assert.Contains(t, out, "2026-01-01T10:00:00Z")
	// builds and deploys inline
	assert.Contains(t, out, "app")
	assert.Contains(t, out, "services")
}

func TestRunEnv_FilterJSON(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runEnv(path, "ci", true, "staging")
		require.NoError(t, err)
	})

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, "staging", result["environment"])
	state, ok := result["state"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "111aaa", state["sha"])
}

func TestRunEnv_UnknownEnv_HumanReadable(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runEnv(path, "ci", false, "nonexistent")
		require.NoError(t, err)
	})
	assert.Contains(t, out, "nonexistent")
	// Should print placeholder, not panic
	assert.Contains(t, out, "-")
}

func TestRunEnv_UnknownEnv_JSON(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runEnv(path, "ci", true, "ghost")
		require.NoError(t, err)
	})
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, "ghost", result["environment"])
}

func TestRunEnv_EmptyProdState(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runEnv(path, "ci", false, "prod")
		require.NoError(t, err)
	})
	// No panic; dashes printed for empty fields
	assert.Contains(t, out, "environment: prod")
	assert.Contains(t, out, "-")
}

// -------- status build --------

func TestRunBuild_KnownBuild(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runBuild(path, "ci", false, "dev", "app")
		require.NoError(t, err)
	})
	assert.Contains(t, out, "app")
	assert.Contains(t, out, "abc123def456")
	assert.Contains(t, out, "sha256:aabbcc")
}

func TestRunBuild_JSON(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runBuild(path, "ci", true, "dev", "app")
		require.NoError(t, err)
	})
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, "dev", result["environment"])
	assert.Equal(t, "app", result["build"])
	state := result["state"].(map[string]interface{})
	assert.Equal(t, "sha256:aabbcc", state["artifact_id"])
}

func TestRunBuild_MissingBuild(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runBuild(path, "ci", false, "dev", "no-such-build")
		require.NoError(t, err)
	})
	assert.Contains(t, out, "no-such-build")
}

func TestRunBuild_MissingBuild_JSON(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runBuild(path, "ci", true, "dev", "absent")
		require.NoError(t, err)
	})
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Nil(t, result["state"])
}

func TestRunBuild_MissingEnv(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runBuild(path, "ci", false, "nowhere", "app")
		require.NoError(t, err)
	})
	assert.Contains(t, out, "nowhere")
}

// -------- status deploy --------

func TestRunDeploy_KnownDeploy(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runDeploy(path, "ci", false, "dev", "services")
		require.NoError(t, err)
	})
	assert.Contains(t, out, "services")
	assert.Contains(t, out, "abc123def456")
	assert.Contains(t, out, "2026-01-01T10:05:00Z")
}

func TestRunDeploy_JSON(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runDeploy(path, "ci", true, "dev", "services")
		require.NoError(t, err)
	})
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, "dev", result["environment"])
	assert.Equal(t, "services", result["deploy"])
	state := result["state"].(map[string]interface{})
	assert.Equal(t, "2026-01-01T10:05:00Z", state["deployed_at"])
}

func TestRunDeploy_MissingDeploy(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runDeploy(path, "ci", false, "dev", "no-such-deploy")
		require.NoError(t, err)
	})
	assert.Contains(t, out, "no-such-deploy")
}

func TestRunDeploy_MissingEnv(t *testing.T) {
	path := fixtureManifest(t)
	out := captureOutput(t, func() {
		err := runDeploy(path, "ci", false, "nowhere", "services")
		require.NoError(t, err)
	})
	assert.Contains(t, out, "nowhere")
}
