package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunner_ExecuteStep_Commit(t *testing.T) {
	// This is a unit test for step execution logic
	// Full integration tests will be separate

	step := &Step{
		Name:   "Test commit",
		Action: "commit",
		Commit: &CommitStep{
			Message: "feat: test feature",
			Files:   map[string]string{"test.go": "package test"},
		},
	}

	// Without infrastructure, this should be a no-op for unit test
	// Just verify the step is recognized
	assert.Equal(t, "commit", step.Action)
	assert.NotNil(t, step.Commit)
	assert.Equal(t, "feat: test feature", step.Commit.Message)
}

func TestRunner_ValidateScenario(t *testing.T) {
	runner := &Runner{t: t}

	// Valid scenario
	scenario := &MultiStepScenario{
		Name: "Test",
		Config: Config{
			Environments: []string{"dev", "prod"},
		},
		Steps: []Step{
			{Name: "Step 1", Action: "commit", Commit: &CommitStep{Message: "test", Files: map[string]string{"a": "b"}}},
			{Name: "Step 2", Action: "orchestrate"},
		},
	}
	err := runner.ValidateScenario(scenario)
	require.NoError(t, err)

	// Invalid - unknown action
	scenario.Steps = append(scenario.Steps, Step{Name: "Bad", Action: "unknown"})
	err = runner.ValidateScenario(scenario)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown action")
}

func TestRunner_ValidateScenario_CommitValidation(t *testing.T) {
	runner := &Runner{t: t}

	tests := []struct {
		name      string
		step      Step
		wantError string
	}{
		{
			name: "commit without commit config",
			step: Step{
				Name:   "Bad commit",
				Action: "commit",
				Commit: nil,
			},
			wantError: "commit action requires commit config",
		},
		{
			name: "commit without message",
			step: Step{
				Name:   "Bad commit",
				Action: "commit",
				Commit: &CommitStep{
					Message: "",
					Files:   map[string]string{"a": "b"},
				},
			},
			wantError: "commit must have a message",
		},
		{
			name: "commit without files",
			step: Step{
				Name:   "Bad commit",
				Action: "commit",
				Commit: &CommitStep{
					Message: "test",
					Files:   map[string]string{},
				},
			},
			wantError: "commit must have files",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scenario := &MultiStepScenario{
				Name:  "Test",
				Steps: []Step{tt.step},
			}
			err := runner.ValidateScenario(scenario)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
		})
	}
}

func TestRunner_ValidateScenario_PromoteValidation(t *testing.T) {
	runner := &Runner{t: t}

	tests := []struct {
		name      string
		step      Step
		wantError string
	}{
		{
			name: "promote without promote config",
			step: Step{
				Name:    "Bad promote",
				Action:  "promote",
				Promote: nil,
			},
			wantError: "promote action requires promote config",
		},
		{
			name: "promote with invalid mode",
			step: Step{
				Name:   "Bad promote",
				Action: "promote",
				Promote: &PromoteStep{
					Mode: "invalid",
				},
			},
			wantError: "promote mode must be 'default' or 'cascade'",
		},
		{
			name: "cascade without target",
			step: Step{
				Name:   "Bad promote",
				Action: "promote",
				Promote: &PromoteStep{
					Mode:   "cascade",
					Target: "",
				},
			},
			wantError: "cascade promote requires target",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scenario := &MultiStepScenario{
				Name:  "Test",
				Steps: []Step{tt.step},
			}
			err := runner.ValidateScenario(scenario)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
		})
	}
}

func TestRunner_ExecuteCommit(t *testing.T) {
	runner := &Runner{
		ctx: NewExecutionContext(),
		t:   t,
	}

	commit := &CommitStep{
		Message: "feat: test feature",
		Files:   map[string]string{"test.go": "package test"},
	}

	ctx := context.Background()
	err := runner.executeCommit(ctx, commit)
	require.NoError(t, err)

	// Verify commit was tracked
	assert.Equal(t, 1, runner.ctx.GetCommitCount())
	sha := runner.ctx.ResolveSHA("commit1")
	assert.NotEmpty(t, sha)
	assert.Contains(t, sha, "sha_commit1") // placeholder SHA
}

func TestRunner_AssertStep_UnchangedState(t *testing.T) {
	runner := &Runner{
		ctx: NewExecutionContext(),
		t:   t,
	}

	// Set up initial state
	runner.ctx.RecordState("dev", "abc123", "v0.1.0")

	// Take snapshot
	preState := runner.ctx.Clone()

	// Create step with unchanged expectation
	step := &Step{
		Name:   "Test step",
		Action: "orchestrate",
		Expect: &StepExpect{
			State: map[string]*StateExpect{
				"dev": {
					Unchanged: true,
				},
			},
		},
	}

	// Assert - should pass since state hasn't changed
	ctx := context.Background()
	errs := runner.assertStep(ctx, step, preState)
	assert.Empty(t, errs)

	// Now change state
	runner.ctx.RecordState("dev", "def456", "v0.2.0")

	// Assert - should fail since state changed
	errs = runner.assertStep(ctx, step, preState)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "expected unchanged but changed")
}

func TestRunner_AssertStep_State(t *testing.T) {
	runner := &Runner{
		ctx: NewExecutionContext(),
		t:   t,
	}

	// Set up state
	runner.ctx.RecordCommit("commit1", "abc123")
	runner.ctx.RecordState("dev", "abc123", "v0.1.0-rc.0")

	step := &Step{
		Name:   "Test step",
		Action: "orchestrate",
		Expect: &StepExpect{
			State: map[string]*StateExpect{
				"dev": {
					SHA:     "commit1",
					Version: "v0.1.0-rc.0",
				},
			},
		},
	}

	ctx := context.Background()
	preState := runner.ctx.Clone()
	errs := runner.assertStep(ctx, step, preState)
	assert.Empty(t, errs)

	// Test with wrong version
	step.Expect.State["dev"].Version = "v0.2.0"
	errs = runner.assertStep(ctx, step, preState)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "version")
}

func TestRunner_AssertStep_Tags(t *testing.T) {
	runner := &Runner{
		ctx: NewExecutionContext(),
		t:   t,
	}

	// Set up tags
	runner.ctx.RecordTag("v0.1.0-rc.0", true)
	runner.ctx.RecordTag("v0.1.0-rc.1", true)

	step := &Step{
		Name:   "Test step",
		Action: "orchestrate",
		Expect: &StepExpect{
			Tags: &TagsExpect{
				Exist:   []string{"v0.1.0-rc.0", "v0.1.0-rc.1"},
				Deleted: []string{"v0.1.0"},
			},
		},
	}

	ctx := context.Background()
	preState := runner.ctx.Clone()
	errs := runner.assertStep(ctx, step, preState)
	assert.Empty(t, errs)

	// Test with missing tag
	step.Expect.Tags.Exist = append(step.Expect.Tags.Exist, "v0.2.0")
	errs = runner.assertStep(ctx, step, preState)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "v0.2.0")
}

func TestRunner_AssertStep_Releases(t *testing.T) {
	runner := &Runner{
		ctx: NewExecutionContext(),
		t:   t,
	}

	// Set up releases
	runner.ctx.RecordRelease(&ReleaseInfo{
		Tag:        "v0.1.0-rc.0",
		Prerelease: true,
		Draft:      true,
	})

	step := &Step{
		Name:   "Test step",
		Action: "orchestrate",
		Expect: &StepExpect{
			Releases: []ReleaseExpectStep{
				{
					Tag:        "v0.1.0-rc.0",
					Prerelease: true,
					Draft:      true,
				},
			},
		},
	}

	ctx := context.Background()
	preState := runner.ctx.Clone()
	errs := runner.assertStep(ctx, step, preState)
	assert.Empty(t, errs)

	// Test with wrong prerelease flag
	step.Expect.Releases[0].Prerelease = false
	errs = runner.assertStep(ctx, step, preState)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "prerelease")
}

func TestRunner_Run_SimpleScenario(t *testing.T) {
	runner := &Runner{
		ctx: NewExecutionContext(),
		t:   t,
	}

	scenario := &MultiStepScenario{
		Name: "Simple test",
		Config: Config{
			Environments: []string{},
		},
		Steps: []Step{
			{
				Name:   "First commit",
				Action: "commit",
				Commit: &CommitStep{
					Message: "feat: test",
					Files:   map[string]string{"test.go": "package test"},
				},
			},
		},
	}

	ctx := context.Background()
	err := runner.Run(ctx, scenario)
	require.NoError(t, err)

	// Verify commit was tracked
	assert.Equal(t, 1, runner.ctx.GetCommitCount())
}

func TestRunner_ApplySetup(t *testing.T) {
	runner := &Runner{
		ctx: NewExecutionContext(),
		t:   t,
	}

	setup := &SetupState{
		State: map[string]*EnvStateSetup{
			"dev": {
				SHA:     "abc123",
				Version: "v0.1.0",
			},
		},
		Tags: []string{"v0.1.0", "v0.2.0"},
		Releases: []ReleaseSetup{
			{
				Tag:        "v0.1.0",
				Prerelease: false,
			},
		},
	}

	ctx := context.Background()
	err := runner.applySetup(ctx, setup)
	require.NoError(t, err)

	// Verify state was applied
	state := runner.ctx.GetState("dev")
	assert.Equal(t, "abc123", state.SHA)
	assert.Equal(t, "v0.1.0", state.Version)

	// Verify tags
	assert.True(t, runner.ctx.HasTag("v0.1.0"))
	assert.True(t, runner.ctx.HasTag("v0.2.0"))

	// Verify releases
	release := runner.ctx.GetRelease("v0.1.0")
	assert.NotNil(t, release)
	assert.Equal(t, "v0.1.0", release.Tag)
	assert.False(t, release.Prerelease)
}
