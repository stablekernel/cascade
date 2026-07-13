package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSimulateHotfixPreviewsCherryPickChain builds the real cascade CLI and runs
// `cascade simulate hotfix` against a three-environment manifest, asserting the
// command previews the ordered multi-environment cherry-pick chain. A hotfix
// targeting prod on the dev -> uat -> prod ladder elevates the fix bottom-up
// through uat and then prod, and the what-if renders that chain without touching
// git or the network.
//
// The what-if simulator is in-process, so unlike the act + gitea scenarios this
// exercises the shipped binary end to end (build the CLI, run it against a real
// manifest, parse stdout) with no containers, and therefore runs under -short.
func TestSimulateHotfixPreviewsCherryPickChain(t *testing.T) {
	t.Parallel()

	projectRoot, err := filepath.Abs("..")
	require.NoError(t, err, "resolve project root")

	// Build the CLI for the host so the test can run it directly.
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "cascade")
	build := exec.Command("go", "build", "-o", bin, "./cmd/cascade")
	build.Dir = projectRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cascade CLI: %v\n%s", err, out)
	}

	// A dev -> uat -> prod ladder with recorded state for uat and prod, so a
	// hotfix targeting prod has a real elevation chain (uat then prod) to preview.
	manifest := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - uat
      - prod
  state:
    uat:
      sha: uatbase0000000000000000000000000000000a
      version: v1.1.0-rc.1
      committed_at: "2026-01-02T10:00:00Z"
      committed_by: seed
    prod:
      sha: prodbase000000000000000000000000000000b
      version: v1.0.0
      committed_at: "2026-01-01T10:00:00Z"
      committed_by: seed
`
	manifestPath := filepath.Join(t.TempDir(), "manifest.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0o600))

	const fix = "fixaaa11122233344455566677788899900011122"
	run := exec.Command(bin, "simulate", "hotfix",
		"--env", "prod",
		"--fix", fix,
		"--config", manifestPath,
	)
	out, err := run.CombinedOutput()
	require.NoErrorf(t, err, "simulate hotfix failed: %s", out)
	got := string(out)

	require.Contains(t, got, "Cherry-pick chain (in order):",
		"the hotfix what-if must preview the cherry-pick chain")
	require.Contains(t, got, "1. cherry-pick uat (commit fixaaa1)")
	require.Contains(t, got, "2. cherry-pick prod (commit fixaaa1)")

	// The chain elevates bottom-up: uat is planned before the prod target.
	uatIdx := strings.Index(got, "cherry-pick uat")
	prodIdx := strings.Index(got, "cherry-pick prod")
	require.GreaterOrEqual(t, uatIdx, 0)
	require.GreaterOrEqual(t, prodIdx, 0)
	require.Less(t, uatIdx, prodIdx, "the chain must list uat before the prod target")
}
