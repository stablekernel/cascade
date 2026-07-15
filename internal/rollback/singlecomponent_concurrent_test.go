package rollback

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// flatEnvLeaf digs out ci.state.<env> from a parsed single-component manifest,
// failing the test when any level is missing.
func flatEnvLeaf(t *testing.T, m map[string]any, env string) map[string]any {
	t.Helper()
	ci, ok := m["ci"].(map[string]any)
	require.True(t, ok, "ci block present")
	state, ok := ci["state"].(map[string]any)
	require.True(t, ok, "ci.state present")
	leaf, ok := state[env].(map[string]any)
	require.True(t, ok, "env %s present", env)
	return leaf
}

// TestSingleComponentFinalize_SiblingEnvSurvivesThrough409Retry drives the
// single-component (component == "") rollback finalize write path end to end:
// Rollbacker.CommitAndPush on real GitHub must go through the shared
// statewrite.CommitWithRetry optimistic-lock loop, exactly like its
// component-scoped and promote/hotfix siblings, rather than a one-shot
// read-sha-then-PUT.
//
// The scenario: prod is rolled back while a concurrent dev finalize wins the
// race and commits dev's advanced state between this writer's read and its PUT.
// The losing writer must observe the 409, re-fetch the winner's bytes, re-apply
// only its own prod leaf, and land a manifest carrying BOTH writes. A one-shot
// PUT either clobbers the winner's dev state (if it read a fresh SHA with stale
// content) or aborts the whole finalize (if its stale SHA is rejected); neither
// is acceptable under the concurrent-finalize waves this path serves.
func TestSingleComponentFinalize_SiblingEnvSurvivesThrough409Retry(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "owner/repo")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REF", "refs/heads/main")

	const baseManifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
  state:
    dev:
      sha: devbase00001
      version: v1.5.0-rc.2
    prod:
      sha: prodcur00001
      version: v1.4.0
      previous:
        - sha: prodprev0001
          version: v1.3.0
`

	// The winner's committed trunk: a concurrent dev finalize advanced dev while
	// this rollback was in flight.
	const winnerTrunk = `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
  state:
    dev:
      sha: devwinner001
      version: v1.5.0-rc.3
      committed_by: dev-bot
    prod:
      sha: prodcur00001
      version: v1.4.0
      previous:
        - sha: prodprev0001
          version: v1.3.0
`

	dir := t.TempDir()
	gitInit(t, dir)
	gitCommitFile(t, dir, "manifest.yaml", baseManifest, "seed manifest")
	t.Chdir(dir)

	client := &conflictOnceClient{
		trunk:      []byte(baseManifest),
		sha:        "sha-initial",
		injectYAML: winnerTrunk,
	}

	rb, err := New(Options{ConfigPath: filepath.Join(dir, "manifest.yaml"), Actor: "prod-bot"})
	require.NoError(t, err)
	rb.contentsClient = client

	plan, err := rb.Plan("prod", "", "")
	require.NoError(t, err)
	require.NoError(t, rb.Apply(plan))

	require.NoError(t, rb.CommitAndPush())
	require.Equal(t, 2, client.puts, "expected exactly one 409 retry through the shared optimistic-lock loop")

	final := parseManifestTree(t, client.trunk)

	// The rollback's own prod leaf landed the rolled-back version.
	prod := flatEnvLeaf(t, final, "prod")
	require.Equal(t, "prodprev0001", prod["sha"])
	require.Equal(t, "v1.3.0", prod["version"])
	require.Equal(t, "prod-bot", prod["committed_by"])

	// The winner's concurrently committed dev state survived the retry.
	dev := flatEnvLeaf(t, final, "dev")
	require.Equal(t, "devwinner001", dev["sha"], "the concurrent dev finalize's committed state must survive")
	require.Equal(t, "v1.5.0-rc.3", dev["version"])
}

// TestSingleComponentCommitAndPush_NoChangesIsNoOp keeps the clean-tree fast
// path: an unchanged committed manifest observes no diff and returns nil
// without attempting any write.
func TestSingleComponentCommitAndPush_NoChangesIsNoOp(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	gitCommitFile(t, dir, "manifest.yaml", manifestAt("prodsha9999999", "v1.9.0"), "seed")
	t.Chdir(dir)

	rb := &Rollbacker{configPath: "manifest.yaml", appliedEnv: "prod"}
	require.NoError(t, rb.CommitAndPush())
}
