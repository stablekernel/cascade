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
