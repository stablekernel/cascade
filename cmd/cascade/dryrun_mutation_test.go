package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run the built binary end to end and prove that --dry-run gates
// the state-mutation paths. The temp dir is deliberately not a git repo: if
// the dry-run guard is bypassed, the command rewrites the manifest on disk
// and then fails attempting a real commit and push, so the assertions below
// catch both the mutation and the non-zero exit.

// TestOrchestrateFinalize_DryRunDoesNotMutateState proves that
// `orchestrate finalize --dry-run` writes no state: the manifest stays
// byte-identical, the command exits 0, and the dry-run preview is printed.
func TestOrchestrateFinalize_DryRunDoesNotMutateState(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, ".github", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatalf("creating .github dir: %v", err)
	}

	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.0
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}

	stdout, stderr, err := runCLIIn(tmpDir,
		"orchestrate", "finalize",
		"--config", manifestPath,
		"--environment", "dev",
		"--version", "v1.2.3",
		"--deploy-results", "app:success",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run finalize must not fail: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("re-reading manifest: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("dry-run finalize mutated the manifest on disk:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "Would update state") {
		t.Errorf("expected dry-run preview output, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestExternalUpdate_DryRunDoesNotMutateState proves that
// `external update --dry-run` writes no state: the manifest stays
// byte-identical, the command exits 0, and the dry-run preview is printed.
func TestExternalUpdate_DryRunDoesNotMutateState(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
    external:
      - repo: org/cdk-infra
        deploys:
          - name: cdk
            workflow: .github/workflows/deploy-cdk.yaml
  state:
    dev:
      sha: abc123
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}

	stdout, stderr, err := runCLIIn(tmpDir,
		"external", "update",
		"--config", manifestPath,
		"--source-repo", "org/cdk-infra",
		"--deploy-name", "cdk",
		"--environment", "dev",
		"--sha", "deadbeef",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run external update must not fail: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("re-reading manifest: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("dry-run external update mutated the manifest on disk:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "Would commit and push") {
		t.Errorf("expected dry-run preview output, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}
