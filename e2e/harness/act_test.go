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

func TestEventFileArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventPath string
		want      []string
	}{
		{
			name:      "no event path yields no flag",
			eventPath: "",
			want:      nil,
		},
		{
			name:      "event path yields -e flag",
			eventPath: eventFilePath,
			want:      []string{"-e", eventFilePath},
		},
		{
			name:      "custom event path is passed through",
			eventPath: "/tmp/other-event.json",
			want:      []string{"-e", "/tmp/other-event.json"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, eventFileArgs(tt.eventPath))
		})
	}
}

// TestBuildActArgs_EventFlag verifies the act command picks up `-e <file>` when
// an event payload was written, and omits it otherwise, without requiring a
// real act run or container.
func TestBuildActArgs_EventFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		opts         RunOpts
		eventPath    string
		wantContains []string
		wantOmits    []string
	}{
		{
			name:         "event payload adds -e flag",
			opts:         RunOpts{},
			eventPath:    eventFilePath,
			wantContains: []string{"-e " + eventFilePath},
		},
		{
			name:      "no event payload omits -e flag",
			opts:      RunOpts{},
			eventPath: "",
			wantOmits: []string{"-e"},
		},
		{
			name:         "event flag coexists with env and inputs",
			opts:         RunOpts{Env: map[string]string{"FOO": "bar"}, Inputs: map[string]string{"k": "v"}},
			eventPath:    eventFilePath,
			wantContains: []string{"-e " + eventFilePath, "--env FOO=bar", "--input k=v"},
		},
	}

	a := &ActRunner{}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args := a.buildActArgs(tt.opts, tt.eventPath)
			for _, want := range tt.wantContains {
				assert.Contains(t, args, want)
			}
			for _, omit := range tt.wantOmits {
				assert.NotContains(t, args, omit)
			}
		})
	}
}

// TestDispatchInputsEventJSON verifies the synthesized workflow_dispatch payload
// embeds the run's inputs under a top-level "inputs" key (the shape act reads to
// seed github.event.inputs) and is empty when there are no inputs.
func TestDispatchInputsEventJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		inputs map[string]string
		want   string
	}{
		{
			name:   "no inputs yields empty payload",
			inputs: nil,
			want:   "",
		},
		{
			name:   "empty map yields empty payload",
			inputs: map[string]string{},
			want:   "",
		},
		{
			name: "hotfix dry-run inputs are embedded under inputs key (keys sorted)",
			inputs: map[string]string{
				"commit":     "abc123",
				"target_env": "test",
				"dry_run":    "true",
			},
			want: `{"inputs":{"commit":"abc123","dry_run":"true","target_env":"test"}}`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, dispatchInputsEventJSON(tt.inputs))
		})
	}
}

// TestResolveEventJSON verifies an explicit EventJSON always wins, a
// workflow_dispatch with inputs synthesizes the inputs payload, and all other
// runs resolve to no event file.
func TestResolveEventJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts RunOpts
		want string
	}{
		{
			name: "explicit event json wins over synthesized payload",
			opts: RunOpts{
				Event:     "workflow_dispatch",
				EventJSON: `{"action":"closed"}`,
				Inputs:    map[string]string{"dry_run": "true"},
			},
			want: `{"action":"closed"}`,
		},
		{
			name: "workflow_dispatch with inputs synthesizes inputs payload",
			opts: RunOpts{
				Event:  "workflow_dispatch",
				Inputs: map[string]string{"dry_run": "true", "target_env": "test"},
			},
			want: `{"inputs":{"dry_run":"true","target_env":"test"}}`,
		},
		{
			name: "workflow_dispatch without inputs yields no event file",
			opts: RunOpts{Event: "workflow_dispatch"},
			want: "",
		},
		{
			name: "non-dispatch event without explicit json yields no event file",
			opts: RunOpts{
				Event:  "push",
				Inputs: map[string]string{"dry_run": "true"},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, resolveEventJSON(tt.opts))
		})
	}
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
