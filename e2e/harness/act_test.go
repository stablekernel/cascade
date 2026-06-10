package harness

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActRunner_Start(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	runner, err := NewActRunner(ctx, "", "", "", nil)
	require.NoError(t, err)
	defer func() { _ = runner.Terminate(ctx) }()

	assert.NotNil(t, runner.container)
}

func TestActRunner_RunWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	runner, err := NewActRunner(ctx, "", "", "", nil)
	require.NoError(t, err)
	defer func() { _ = runner.Terminate(ctx) }()

	// Simple test workflow
	workflowContent := `
name: Simple Test
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: Echo
        run: echo "Hello from act"
`

	result, err := runner.RunWorkflow(ctx, RunOpts{
		WorkflowContent: workflowContent,
		Event:           "push",
	})
	require.NoError(t, err)
	assert.Equal(t, "success", result.Conclusion)
	assert.Contains(t, result.Logs, "Hello from act")
}

func TestNormalizeWorkflowResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		jobs           map[string]*JobResultExtended
		workflowPath   string
		exitCode       int
		wantConclusion string
		wantErr        bool
	}{
		{
			name:           "successful run with jobs stays success",
			jobs:           map[string]*JobResultExtended{"build": {Name: "build", Conclusion: "success"}},
			workflowPath:   ".github/workflows/orchestrate.yaml",
			exitCode:       0,
			wantConclusion: "success",
			wantErr:        false,
		},
		{
			name:           "targeted workflow with zero jobs is a failure",
			jobs:           map[string]*JobResultExtended{},
			workflowPath:   ".github/workflows/orchestrate.yaml",
			exitCode:       0,
			wantConclusion: "failure",
			wantErr:        true,
		},
		{
			name:           "non-zero exit is a failure",
			jobs:           map[string]*JobResultExtended{"build": {Name: "build"}},
			workflowPath:   ".github/workflows/orchestrate.yaml",
			exitCode:       1,
			wantConclusion: "failure",
			wantErr:        true,
		},
		{
			name:           "no targeted workflow path tolerates zero jobs",
			jobs:           map[string]*JobResultExtended{},
			workflowPath:   "",
			exitCode:       0,
			wantConclusion: "success",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := &ExtendedWorkflowResult{Conclusion: "success", Jobs: tt.jobs}
			normalizeWorkflowResult(result, tt.workflowPath, tt.exitCode)
			assert.Equal(t, tt.wantConclusion, result.Conclusion)
			if tt.wantErr {
				assert.NotEmpty(t, result.Error)
			} else {
				assert.Empty(t, result.Error)
			}
		})
	}
}
