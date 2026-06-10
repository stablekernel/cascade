package rollback

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// onDiskManifest writes a manifest fixture into a real git repo, with prod's
// per-deployable "services" state having advanced past a prior version that
// lives only in an earlier commit. This exercises git-history resolution end to
// end through the cobra command. Returns the manifest path.
func onDiskManifest(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)

	rel := "manifest.yaml"
	prior := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    deploys:
      - name: services
        workflow: .github/workflows/deploy.yaml
        triggers:
          - "deploy/**"
  state:
    prod:
      sha: prodold1112223
      version: v1.9.0
      committed_at: "2026-02-15T11:00:00Z"
      committed_by: alice
      deploys:
        services:
          sha: prodold1112223
          version: v1.9.0
          deployed_at: "2026-02-15T11:05:00Z"
          deployed_by: alice
`
	current := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    deploys:
      - name: services
        workflow: .github/workflows/deploy.yaml
        triggers:
          - "deploy/**"
  state:
    prod:
      sha: prodnew7654321
      version: v2.0.0
      committed_at: "2026-03-01T11:00:00Z"
      committed_by: alice
      deploys:
        services:
          sha: prodnew7654321
          version: v2.0.0
          deployed_at: "2026-03-01T11:05:00Z"
          deployed_by: alice
`
	gitCommitFile(t, dir, rel, prior, "prod v1.9.0")
	gitCommitFile(t, dir, rel, current, "prod v2.0.0")
	return filepath.Join(dir, rel)
}

func TestCommand_RollbackAgainstOnDiskManifest(t *testing.T) {
	path := onDiskManifest(t)

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// Roll the "services" deployable back to v1.9.0, recoverable only from a
	// prior manifest commit.
	cmd.SetArgs([]string{
		"--config", path,
		"--env", "prod",
		"--to", "v1.9.0",
		"--deployable", "services",
		"--actor", "oncall",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	ds := file.State["prod"].Deploys["services"]
	if ds == nil || ds.SHA != "prodold1112223" {
		t.Errorf("services not rolled back on disk: %+v", ds)
	}
	if ds.DeployedBy != "oncall" {
		t.Errorf("deployed_by = %q, want oncall", ds.DeployedBy)
	}
	// Env pointer must be untouched by a deployable-scoped rollback.
	if file.State["prod"].SHA != "prodnew7654321" {
		t.Errorf("env pointer mutated: %q", file.State["prod"].SHA)
	}
}

func TestCommand_DryRunDoesNotMutate(t *testing.T) {
	path := onDiskManifest(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", path,
		"--env", "prod",
		"--to", "v1.9.0",
		"--deployable", "services",
		"--dry-run",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Error("--dry-run mutated the manifest")
	}
}

func TestCommand_UnknownTargetReturnsError(t *testing.T) {
	path := onDiskManifest(t)

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"--config", path,
		"--env", "prod",
		"--to", "v0.0.0-nope",
	})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for unknown target")
	}
}
