package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExecutionContext_TrackCommits(t *testing.T) {
	ctx := NewExecutionContext()

	// Track commits by reference
	ctx.RecordCommit("commit1", "abc123")
	ctx.RecordCommit("commit2", "def456")

	assert.Equal(t, "abc123", ctx.ResolveSHA("commit1"))
	assert.Equal(t, "def456", ctx.ResolveSHA("commit2"))
	assert.Equal(t, "", ctx.ResolveSHA("commit3")) // Unknown
}

func TestExecutionContext_TrackState(t *testing.T) {
	ctx := NewExecutionContext()

	// Record state after orchestrate
	ctx.RecordState("dev", "abc123", "v0.1.0-rc.0")

	state := ctx.GetState("dev")
	assert.Equal(t, "abc123", state.SHA)
	assert.Equal(t, "v0.1.0-rc.0", state.Version)
}

func TestExecutionContext_ClearState(t *testing.T) {
	// ClearState removes all env state so a sync from manifest can rebuild
	// from a clean slate. Without this, deletions in the manifest (e.g.
	// finalize wiping state[prerelease] on publish) leave stale entries
	// in ctx, breaking wiped: true assertions.
	ctx := NewExecutionContext()
	ctx.RecordState("dev", "abc", "v1.0.0-rc.0")
	ctx.RecordDeployState("dev", "app", "abc")
	ctx.RecordState("prerelease", "def", "v2.0.0-rc.0")

	// sanity: state populated
	assert.Equal(t, "abc", ctx.GetState("dev").SHA)
	assert.Equal(t, "def", ctx.GetState("prerelease").SHA)

	ctx.ClearState()

	assert.Equal(t, "", ctx.GetState("dev").SHA, "dev state should be cleared")
	assert.Equal(t, "", ctx.GetState("dev").Version, "dev version should be cleared")
	assert.Equal(t, "", ctx.GetState("prerelease").SHA, "prerelease state should be cleared")

	// Recording state after clear should work normally
	ctx.RecordState("dev", "new-sha", "v1.0.0")
	assert.Equal(t, "new-sha", ctx.GetState("dev").SHA)
}

func TestExecutionContext_GetCommitCount(t *testing.T) {
	ctx := NewExecutionContext()

	assert.Equal(t, 0, ctx.GetCommitCount())

	ctx.RecordCommit("commit1", "abc123")
	assert.Equal(t, 1, ctx.GetCommitCount())

	ctx.RecordCommit("commit2", "def456")
	assert.Equal(t, 2, ctx.GetCommitCount())
}

func TestExecutionContext_RecordDeployState(t *testing.T) {
	ctx := NewExecutionContext()

	// Record deploy state
	ctx.RecordDeployState("dev", "cdk", "abc123")

	state := ctx.GetState("dev")
	assert.NotNil(t, state.Deploys)
	assert.NotNil(t, state.Deploys["cdk"])
	assert.Equal(t, "abc123", state.Deploys["cdk"].SHA)
}

func TestExecutionContext_TrackTags(t *testing.T) {
	ctx := NewExecutionContext()

	// Tag should not exist initially
	assert.False(t, ctx.HasTag("v0.1.0"))

	// Record tag as existing
	ctx.RecordTag("v0.1.0", true)
	assert.True(t, ctx.HasTag("v0.1.0"))

	// Mark tag as deleted
	ctx.RecordTag("v0.1.0", false)
	assert.False(t, ctx.HasTag("v0.1.0"))
}

func TestExecutionContext_TrackReleases(t *testing.T) {
	ctx := NewExecutionContext()

	// Release should not exist initially
	release := ctx.GetRelease("v0.1.0")
	assert.Nil(t, release)

	// Record release
	ctx.RecordRelease(&ReleaseInfo{
		Tag:        "v0.1.0",
		SHA:        "abc123",
		Prerelease: true,
		Draft:      true,
		Latest:     false,
	})

	// Retrieve release
	release = ctx.GetRelease("v0.1.0")
	assert.NotNil(t, release)
	assert.Equal(t, "v0.1.0", release.Tag)
	assert.Equal(t, "abc123", release.SHA)
	assert.True(t, release.Prerelease)
	assert.True(t, release.Draft)
	assert.False(t, release.Latest)
}

func TestExecutionContext_WipeState(t *testing.T) {
	ctx := NewExecutionContext()

	// Record state
	ctx.RecordState("dev", "abc123", "v0.1.0-rc.0")
	state := ctx.GetState("dev")
	assert.Equal(t, "abc123", state.SHA)
	assert.Equal(t, "v0.1.0-rc.0", state.Version)

	// Wipe state
	ctx.WipeState("dev")
	state = ctx.GetState("dev")
	assert.Equal(t, "", state.SHA)
	assert.Equal(t, "", state.Version)
}

func TestExecutionContext_Clone(t *testing.T) {
	ctx := NewExecutionContext()

	// Setup original context
	ctx.RecordCommit("commit1", "abc123")
	ctx.RecordState("dev", "abc123", "v0.1.0-rc.0")
	ctx.RecordDeployState("dev", "cdk", "abc123")
	ctx.RecordTag("v0.1.0-rc.0", true)
	ctx.RecordRelease(&ReleaseInfo{
		Tag:        "v0.1.0-rc.0",
		SHA:        "abc123",
		Prerelease: true,
		Draft:      true,
	})

	// Clone
	clone := ctx.Clone()

	// Verify clone has same data
	assert.Equal(t, "abc123", clone.ResolveSHA("commit1"))
	assert.Equal(t, "abc123", clone.GetState("dev").SHA)
	assert.Equal(t, "v0.1.0-rc.0", clone.GetState("dev").Version)
	assert.Equal(t, "abc123", clone.GetState("dev").Deploys["cdk"].SHA)
	assert.True(t, clone.HasTag("v0.1.0-rc.0"))
	assert.NotNil(t, clone.GetRelease("v0.1.0-rc.0"))

	// Modify original, clone should not be affected
	ctx.RecordCommit("commit2", "def456")
	ctx.RecordState("dev", "def456", "v0.1.0-rc.1")

	assert.Equal(t, "", clone.ResolveSHA("commit2"))     // Not in clone
	assert.Equal(t, "abc123", clone.GetState("dev").SHA) // Still has old value
	assert.Equal(t, "v0.1.0-rc.0", clone.GetState("dev").Version)
}
