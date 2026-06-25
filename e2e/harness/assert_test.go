package harness

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssertStateMatches(t *testing.T) {
	t.Run("state matches expectations", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordCommit("commit1", "abc123")
		ctx.RecordState("dev", "abc123", "v0.1.0-rc.0")

		expected := map[string]*StateExpect{
			"dev": {
				SHA:     "commit1",
				Version: "v0.1.0-rc.0",
			},
		}

		// Should pass - state matches
		mt := &mockT{}
		AssertStateMatches(mt, ctx, expected)
		assert.False(t, mt.failed, "assertion should pass")
	})

	t.Run("version mismatch", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordState("dev", "abc123", "v0.1.0-rc.0")

		expected := map[string]*StateExpect{
			"dev": {
				Version: "v0.2.0-rc.0",
			},
		}

		// Should fail - version mismatch
		mt := &mockT{}
		AssertStateMatches(mt, ctx, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "version")
	})

	t.Run("wiped state", func(t *testing.T) {
		ctx := NewExecutionContext()
		// State should be empty for "prerelease" env

		expected := map[string]*StateExpect{
			"prerelease": {
				Wiped: true,
			},
		}

		// Should pass - state is wiped
		mt := &mockT{}
		AssertStateMatches(mt, ctx, expected)
		assert.False(t, mt.failed, "assertion should pass")
	})

	t.Run("state not wiped when expected", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordState("dev", "abc123", "v0.1.0-rc.0")

		expected := map[string]*StateExpect{
			"dev": {
				Wiped: true,
			},
		}

		// Should fail - state exists but should be wiped
		mt := &mockT{}
		AssertStateMatches(mt, ctx, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "wiped")
	})

	t.Run("deploy state matches", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordCommit("commit1", "abc123")
		ctx.RecordState("dev", "abc123", "v0.1.0-rc.0")
		ctx.RecordDeployState("dev", "cdk", "abc123")

		expected := map[string]*StateExpect{
			"dev": {
				Deploys: map[string]*DeployExpect{
					"cdk": {
						SHA: "commit1",
					},
				},
			},
		}

		// Should pass - deploy state matches
		mt := &mockT{}
		AssertStateMatches(mt, ctx, expected)
		assert.False(t, mt.failed, "assertion should pass")
	})
}

func TestAssertJobsRan(t *testing.T) {
	t.Run("jobs match expectations", func(t *testing.T) {
		runResult := &WorkflowRunResult{
			Jobs: map[string]string{
				"build-app":    "success",
				"build-worker": "skipped",
				"deploy-cdk":   "success",
			},
		}

		expected := map[string]string{
			"build-app":    "success",
			"build-worker": "skipped",
		}

		// Should pass
		mt := &mockT{}
		AssertJobsRan(mt, runResult, expected)
		assert.False(t, mt.failed, "assertion should pass")
	})

	t.Run("job conclusion mismatch", func(t *testing.T) {
		runResult := &WorkflowRunResult{
			Jobs: map[string]string{
				"build-app": "success",
			},
		}

		expected := map[string]string{
			"build-app": "failure",
		}

		// Should fail - wrong conclusion
		mt := &mockT{}
		AssertJobsRan(mt, runResult, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "build-app")
	})

	t.Run("job not found", func(t *testing.T) {
		runResult := &WorkflowRunResult{
			Jobs: map[string]string{
				"build-app": "success",
			},
		}

		expected := map[string]string{
			"build-worker": "success",
		}

		// Should fail - job not found
		mt := &mockT{}
		AssertJobsRan(mt, runResult, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "not found")
	})
}

func TestAssertTagsExist(t *testing.T) {
	t.Run("tags exist as expected", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordTag("v0.1.0-rc.0", true)
		ctx.RecordTag("v0.1.0-rc.1", true)
		ctx.RecordTag("v0.1.0", false) // deleted

		expected := &TagsExpect{
			Exist:   []string{"v0.1.0-rc.0", "v0.1.0-rc.1"},
			Deleted: []string{"v0.1.0"},
		}

		// Should pass
		mt := &mockT{}
		AssertTagsExist(mt, ctx, expected)
		assert.False(t, mt.failed, "assertion should pass")
	})

	t.Run("tag should exist but doesn't", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordTag("v0.1.0-rc.0", true)

		expected := &TagsExpect{
			Exist: []string{"v0.1.0"},
		}

		// Should fail - tag should exist but doesn't
		mt := &mockT{}
		AssertTagsExist(mt, ctx, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "v0.1.0")
	})

	t.Run("tag should be deleted but exists", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordTag("v0.1.0-rc.0", true)

		expected := &TagsExpect{
			Deleted: []string{"v0.1.0-rc.0"},
		}

		// Should fail - tag should be deleted but exists
		mt := &mockT{}
		AssertTagsExist(mt, ctx, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "deleted")
	})
}

func TestAssertReleases(t *testing.T) {
	t.Run("releases match expectations", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordRelease(&ReleaseInfo{
			Tag:        "v0.1.0",
			Prerelease: false,
			Draft:      false,
			Latest:     true,
		})

		expected := []ReleaseExpectStep{
			{
				Tag:        "v0.1.0",
				Prerelease: false,
				Latest:     true,
			},
		}

		// Should pass
		mt := &mockT{}
		AssertReleases(mt, ctx, expected)
		assert.False(t, mt.failed, "assertion should pass")
	})

	t.Run("prerelease flag mismatch", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordRelease(&ReleaseInfo{
			Tag:        "v0.1.0-rc.0",
			Prerelease: true,
			Draft:      true,
		})

		expected := []ReleaseExpectStep{
			{
				Tag:        "v0.1.0-rc.0",
				Prerelease: false, // Expecting false but actual is true
			},
		}

		// Should fail
		mt := &mockT{}
		AssertReleases(mt, ctx, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "prerelease")
	})

	t.Run("draft flag mismatch", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordRelease(&ReleaseInfo{
			Tag:        "v0.1.0-rc.0",
			Prerelease: true,
			Draft:      false,
		})

		expected := []ReleaseExpectStep{
			{
				Tag:        "v0.1.0-rc.0",
				Prerelease: true, // Match the actual value
				Draft:      true, // Expecting true but actual is false
			},
		}

		// Should fail
		mt := &mockT{}
		AssertReleases(mt, ctx, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "draft")
	})

	t.Run("release should be deleted", func(t *testing.T) {
		ctx := NewExecutionContext()
		// No release recorded

		expected := []ReleaseExpectStep{
			{
				Tag:     "v0.1.0-rc.0",
				Deleted: true,
			},
		}

		// Should pass - release is deleted
		mt := &mockT{}
		AssertReleases(mt, ctx, expected)
		assert.False(t, mt.failed, "assertion should pass")
	})

	t.Run("release exists but should be deleted", func(t *testing.T) {
		ctx := NewExecutionContext()
		ctx.RecordRelease(&ReleaseInfo{
			Tag: "v0.1.0-rc.0",
		})

		expected := []ReleaseExpectStep{
			{
				Tag:     "v0.1.0-rc.0",
				Deleted: true,
			},
		}

		// Should fail
		mt := &mockT{}
		AssertReleases(mt, ctx, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "deleted")
	})

	t.Run("release not found", func(t *testing.T) {
		ctx := NewExecutionContext()
		// No release recorded

		expected := []ReleaseExpectStep{
			{
				Tag: "v0.1.0",
			},
		}

		// Should fail
		mt := &mockT{}
		AssertReleases(mt, ctx, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "not found")
	})
}

func TestAssertPreflight(t *testing.T) {
	t.Run("preflight matches expectations", func(t *testing.T) {
		runResult := &WorkflowRunResult{
			Preflight: &PreflightResult{
				HasBreaking: true,
				CanProceed:  false,
				SourceEnv:   "dev",
				TargetEnv:   "prod",
			},
		}

		expected := &PreflightExpect{
			HasBreaking: true,
			CanProceed:  false,
			SourceEnv:   "dev",
			TargetEnv:   "prod",
		}

		// Should pass
		mt := &mockT{}
		AssertPreflight(mt, runResult, expected)
		assert.False(t, mt.failed, "assertion should pass")
	})

	t.Run("has_breaking mismatch", func(t *testing.T) {
		runResult := &WorkflowRunResult{
			Preflight: &PreflightResult{
				HasBreaking: false,
			},
		}

		expected := &PreflightExpect{
			HasBreaking: true,
		}

		// Should fail
		mt := &mockT{}
		AssertPreflight(mt, runResult, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "has_breaking")
	})

	t.Run("can_proceed mismatch", func(t *testing.T) {
		runResult := &WorkflowRunResult{
			Preflight: &PreflightResult{
				CanProceed: true,
			},
		}

		expected := &PreflightExpect{
			CanProceed: false,
		}

		// Should fail
		mt := &mockT{}
		AssertPreflight(mt, runResult, expected)
		assert.True(t, mt.failed, "assertion should fail")
		assert.Contains(t, mt.errors[0], "can_proceed")
	})
}

// mockT is a mock testing.T for testing assertion functions
type mockT struct {
	failed bool
	errors []string
}

func (m *mockT) Errorf(format string, args ...interface{}) {
	m.failed = true
	m.errors = append(m.errors, fmt.Sprintf(format, args...))
}

func (m *mockT) Helper() {}

var _ testingT = (*testing.T)(nil)
var _ testingT = (*mockT)(nil)

func TestAssertRollbackSource(t *testing.T) {
	t.Run("marker present passes", func(t *testing.T) {
		logs := "some output\nrollback resolved from previous-ring\nmore output\n"
		mt := &mockT{}
		err := assertRollbackSource(mt, logs, "previous-ring")
		assert.NoError(t, err)
		assert.False(t, mt.failed)
	})

	t.Run("marker absent fails", func(t *testing.T) {
		logs := "some output\nrollback resolved from state\nmore output\n"
		mt := &mockT{}
		err := assertRollbackSource(mt, logs, "previous-ring")
		assert.Error(t, err)
		assert.True(t, mt.failed)
		assert.Contains(t, mt.errors[0], "previous-ring")
	})

	t.Run("state source marker", func(t *testing.T) {
		logs := "pre\nrollback resolved from state\npost\n"
		mt := &mockT{}
		err := assertRollbackSource(mt, logs, "state")
		assert.NoError(t, err)
		assert.False(t, mt.failed)
	})

	t.Run("git-history source marker", func(t *testing.T) {
		logs := "pre\nrollback resolved from git-history\npost\n"
		mt := &mockT{}
		err := assertRollbackSource(mt, logs, "git-history")
		assert.NoError(t, err)
		assert.False(t, mt.failed)
	})

	t.Run("wrong source in logs fails", func(t *testing.T) {
		logs := "rollback resolved from git-history\n"
		mt := &mockT{}
		err := assertRollbackSource(mt, logs, "state")
		assert.Error(t, err)
		assert.True(t, mt.failed)
		assert.Contains(t, mt.errors[0], "state")
	})
}
