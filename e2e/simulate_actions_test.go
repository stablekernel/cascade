package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSimulateActionsPreviewPlans builds the real cascade CLI and runs
// `cascade simulate promote|rollback|release` against a three-environment
// manifest, asserting each what-if renders its state diff and ordered effect
// sequence and leaves the manifest byte-identical. Together with the hotfix
// chain test this gives every simulate subcommand a binary-level scenario.
//
// The what-if simulator is in-process, so unlike the act + gitea scenarios this
// exercises the shipped binary end to end (build the CLI, run it against a real
// manifest, parse stdout) with no containers, and therefore runs under -short.
func TestSimulateActionsPreviewPlans(t *testing.T) {
	t.Parallel()

	projectRoot, err := filepath.Abs("..")
	require.NoError(t, err, "resolve project root")

	// Build the CLI for the host so the subtests can run it directly.
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "cascade")
	build := exec.Command("go", "build", "-o", bin, "./cmd/cascade")
	build.Dir = projectRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cascade CLI: %v\n%s", err, out)
	}

	// A dev -> uat -> prod ladder: dev carries a newer rc than uat (so a
	// promotion has real work), and prod carries a deploy-history ring snapshot
	// (so a rollback has a real prior target).
	manifest := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - uat
      - prod
  state:
    dev:
      sha: devbase00000000000000000000000000000000c
      version: v1.2.0-rc.3
      committed_at: "2026-01-03T10:00:00Z"
      committed_by: seed
    uat:
      sha: uatbase0000000000000000000000000000000a
      version: v1.1.0-rc.1
      committed_at: "2026-01-02T10:00:00Z"
      committed_by: seed
    prod:
      sha: prodnew00000000000000000000000000000000d
      version: v1.0.1
      committed_at: "2026-01-04T10:00:00Z"
      committed_by: seed
      previous:
        - sha: prodold00000000000000000000000000000000e
          version: v1.0.0
          committed_at: "2026-01-01T10:00:00Z"
          committed_by: seed
`
	manifestPath := filepath.Join(t.TempDir(), "manifest.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0o600))

	// runSimulate runs one subcommand against the shared manifest and asserts
	// the record-only engine left the manifest bytes untouched.
	runSimulate := func(t *testing.T, args ...string) string {
		t.Helper()
		run := exec.Command(bin, append([]string{"simulate"}, args...)...)
		out, err := run.CombinedOutput()
		require.NoErrorf(t, err, "simulate %s failed: %s", strings.Join(args, " "), out)

		after, err := os.ReadFile(manifestPath)
		require.NoError(t, err)
		require.Equal(t, manifest, string(after),
			"a simulation must never mutate the on-disk manifest")
		return string(out)
	}

	t.Run("promote", func(t *testing.T) {
		got := runSimulate(t, "promote", "--config", manifestPath)

		require.Contains(t, got, "Simulating: promote (mode=default)")

		// State diff: uat advances to dev's rc, and the release row appears.
		require.Contains(t, got, "v1.1.0-rc.1 -> v1.2.0-rc.3",
			"uat must advance to the rc dev currently holds")
		require.Contains(t, got, "uatbase -> devbase")
		require.Contains(t, got, "(none) -> v1.1.0",
			"promoting past uat must surface the release the crossing publishes")

		// Effects: deploy before state write before release publish.
		require.Contains(t, got, "deploy uat from dev")
		require.Contains(t, got, "release publish v1.1.0 (rc v1.1.0-rc.1, sha uatbase)")
		deployIdx := strings.Index(got, "deploy uat from dev")
		writeIdx := strings.Index(got, "write state uat")
		publishIdx := strings.Index(got, "release publish v1.1.0")
		require.GreaterOrEqual(t, deployIdx, 0)
		require.GreaterOrEqual(t, writeIdx, 0)
		require.Greater(t, writeIdx, deployIdx, "state is written only after the deploy effect")
		require.Greater(t, publishIdx, writeIdx, "the release publishes only after state is written")
	})

	t.Run("rollback", func(t *testing.T) {
		got := runSimulate(t, "rollback", "--env", "prod", "--config", manifestPath)

		require.Contains(t, got, "Simulating: rollback (env=prod, to=previous)")

		// The target resolves from the deploy-history ring, not trunk state.
		require.Contains(t, got, "v1.0.1 -> v1.0.0", "prod must revert to the ring snapshot")
		require.Contains(t, got, "prodnew -> prodold")
		require.Contains(t, got, "revert prod (to sha prodold, version v1.0.0 (from previous-ring))")
		require.Contains(t, got, "write state prod (sha prodold, version v1.0.0)")
		require.Contains(t, got, "no -> yes",
			"a rollback must mark the environment diverged off trunk")
	})

	t.Run("rollback without a prior target fails", func(t *testing.T) {
		run := exec.Command(bin, "simulate", "rollback", "--env", "uat", "--config", manifestPath)
		out, err := run.CombinedOutput()
		require.Error(t, err, "uat has no deploy history, so the rollback what-if must fail")
		require.Contains(t, string(out), "no prior version to roll back to")
	})

	t.Run("rollback of the first environment is refused", func(t *testing.T) {
		run := exec.Command(bin, "simulate", "rollback", "--env", "dev", "--config", manifestPath)
		out, err := run.CombinedOutput()
		require.Error(t, err, "the first environment tracks trunk and is never rolled back")
		require.Contains(t, string(out), "Revert it with a merge to the trunk branch")
	})

	t.Run("release", func(t *testing.T) {
		got := runSimulate(t, "release", "--config", manifestPath)

		require.Contains(t, got, "Simulating: release (prerelease/publish crossing)")
		require.Contains(t, got, "(none) -> v1.1.0",
			"the crossing must strip uat's rc suffix into the published version")
		require.Contains(t, got, "release publish v1.1.0 (rc v1.1.0-rc.1, sha uatbase)")
	})
}
