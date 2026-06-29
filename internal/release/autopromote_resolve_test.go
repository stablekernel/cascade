package release

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// autoPromoteResolveScript locates the auto-promote decision script by walking
// up from the test's working directory until it finds the repo's go.mod, then
// joining the known script path. This keeps the test independent of the
// directory go test happens to run it from.
func autoPromoteResolveScript(t *testing.T) string {
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

	script := filepath.Join(dir, ".github", "scripts", "auto-promote-resolve.sh")
	_, err = os.Stat(script)
	require.NoError(t, err, "auto-promote-resolve.sh not found at %s", script)
	return script
}

// runAutoPromoteResolve runs the decision script in an isolated temp directory
// with the given marker files and environment, then returns the parsed
// key=value pairs the script appended to $GITHUB_OUTPUT.
func runAutoPromoteResolve(t *testing.T, files, env map[string]string) map[string]string {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; skipping shell decision test")
	}

	script := autoPromoteResolveScript(t)
	workDir := t.TempDir()

	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(workDir, name), []byte(content), 0o600))
	}

	outputPath := filepath.Join(workDir, "github_output")
	require.NoError(t, os.WriteFile(outputPath, nil, 0o600))

	cmd := exec.Command(bash, script)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+outputPath)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "script failed: %s", out)

	return parseGitHubOutput(t, outputPath)
}

func parseGitHubOutput(t *testing.T, path string) map[string]string {
	t.Helper()

	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	result := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		result[key] = value
	}
	require.NoError(t, scanner.Err())
	return result
}

// TestAutoPromoteResolve_DecisionMatrix exercises the promote/no-promote
// decision the auto-promote workflow makes when a Fleet E2E run completes. Each
// case feeds mock marker files and workflow_run env and asserts the resolved
// outputs, covering every gate without needing a real release.
func TestAutoPromoteResolve_DecisionMatrix(t *testing.T) {
	const fullRun = "full-run.txt"
	const versionUnderTest = "version-under-test.txt"

	tests := []struct {
		name        string
		files       map[string]string
		env         map[string]string
		wantPromote string
		wantRC      string
		wantBase    string
	}{
		{
			name:        "rc tag full green run promotes stripped version",
			files:       map[string]string{fullRun: "true", versionUnderTest: "v1.2.3-rc.4"},
			env:         map[string]string{"CONCLUSION": "success"},
			wantPromote: "true",
			wantRC:      "v1.2.3-rc.4",
			wantBase:    "v1.2.3",
		},
		{
			name:        "dryrun version is gated out by the rc-only gate",
			files:       map[string]string{fullRun: "true", versionUnderTest: "v1.2.3-dryrun.4"},
			env:         map[string]string{"CONCLUSION": "success"},
			wantPromote: "false",
		},
		{
			name:        "selective run is gated out by the full_run gate",
			files:       map[string]string{fullRun: "false", versionUnderTest: "v1.2.3-rc.4"},
			env:         map[string]string{"CONCLUSION": "success"},
			wantPromote: "false",
		},
		{
			name:        "non-success fleet conclusion never promotes",
			files:       map[string]string{fullRun: "true", versionUnderTest: "v1.2.3-rc.4"},
			env:         map[string]string{"CONCLUSION": "failure"},
			wantPromote: "false",
		},
		{
			name:        "multi-digit version and rc number strip cleanly",
			files:       map[string]string{fullRun: "true", versionUnderTest: "v1.20.3-rc.10"},
			env:         map[string]string{"CONCLUSION": "success"},
			wantPromote: "true",
			wantRC:      "v1.20.3-rc.10",
			wantBase:    "v1.20.3",
		},
		{
			name:        "head_branch fallback resolves the rc when no artifact is present",
			files:       map[string]string{fullRun: "true"},
			env:         map[string]string{"CONCLUSION": "success", "WR_HEAD_BRANCH": "v2.0.0-rc.1"},
			wantPromote: "true",
			wantRC:      "v2.0.0-rc.1",
			wantBase:    "v2.0.0",
		},
		{
			name:        "non-rc head_branch with no artifact is a clean no-op",
			files:       map[string]string{fullRun: "true"},
			env:         map[string]string{"CONCLUSION": "success", "WR_HEAD_BRANCH": "main"},
			wantPromote: "false",
		},
		{
			name:        "missing full-run marker defaults to a full run (pre-selector artifact)",
			files:       map[string]string{versionUnderTest: "v3.4.5-rc.2"},
			env:         map[string]string{"CONCLUSION": "success"},
			wantPromote: "true",
			wantRC:      "v3.4.5-rc.2",
			wantBase:    "v3.4.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runAutoPromoteResolve(t, tt.files, tt.env)

			require.Equal(t, tt.wantPromote, got["promote"], "promote decision")

			if tt.wantPromote != "true" {
				require.NotContains(t, got, "rc_version", "no rc_version when not promoting")
				require.NotContains(t, got, "base_version", "no base_version when not promoting")
				return
			}

			require.Equal(t, tt.wantRC, got["rc_version"], "rc_version")
			require.Equal(t, tt.wantBase, got["base_version"], "base_version")
		})
	}
}
