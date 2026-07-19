package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/globals"
)

// TestManageRelease_LargeChangelogFlowsViaFile proves the manage-release command
// accepts a changelog larger than the execve single-env-var cap (near 128KB)
// when it arrives by file reference. The generated composite action passes the
// built-in changelog by path for exactly this reason: content on an input
// becomes an environment variable and a large changelog fails the step with
// E2BIG. Reading the file in-process cannot reproduce the shell limit, so this
// asserts the CLI reads the file without complaint; the generator tests prove
// built-in callers only ever place a path on the input.
func TestManageRelease_LargeChangelogFlowsViaFile(t *testing.T) {
	globals.SetDryRun(true)
	t.Cleanup(func() { globals.SetDryRun(false) })

	// 200KB comfortably exceeds the execve single-env-var cap near 128KB.
	large := strings.Repeat("changelog entry line\n", 10000)
	if len(large) < 128*1024 {
		t.Fatalf("test changelog must exceed the 128KB env cap, got %d bytes", len(large))
	}

	changelogPath := filepath.Join(t.TempDir(), "cascade-changelog.md")
	if err := os.WriteFile(changelogPath, []byte(large), 0644); err != nil {
		t.Fatalf("writing large changelog file: %v", err)
	}

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"--repo", "owner/repo",
		"--action", "update",
		"--environment", "prod",
		"--sha", "deadbeef",
		"--tag", "v1.2.3",
		"--token", "test-token",
		"--changelog-file", changelogPath,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("manage-release with a large changelog file must succeed under dry-run, got: %v", err)
	}
}
