package harness

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// frameDockerStream builds a single Docker hijacked-attach frame: an 8-byte
// header (1-byte stream type, 3 zero padding bytes, big-endian uint32 length)
// followed by the payload. This is the exact framing a TTY-less container exec
// produces on CI Linux, and the framing a raw io.Copy would leave interspersed
// in the captured logs.
func frameDockerStream(t *testing.T, stream stdcopy.StdType, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	header := make([]byte, dockerStreamHeaderLen)
	header[0] = byte(stream)
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	buf.Write(header)
	buf.WriteString(payload)
	return buf.Bytes()
}

// TestReadDemuxedStream_MultiplexedFramesAreCleaned is the load-bearing proof
// for the CI-only act-output corruption: it feeds a hand-constructed MULTIPLEXED
// stream (stdout + stderr frames, each carrying Docker's 8-byte header) through
// readDemuxedStream and asserts the result is clean text with the streams merged
// and NO header bytes left behind. Before the demux fix the capture used a raw
// io.Copy, so these header bytes would remain interspersed and corrupt the JSON
// the act parser consumes.
func TestReadDemuxedStream_MultiplexedFramesAreCleaned(t *testing.T) {
	t.Parallel()

	stdoutPayload := "hello world\n"
	stderrPayload := `{"level":"info","msg":"Using docker host..."}` + "\n"

	var raw bytes.Buffer
	raw.Write(frameDockerStream(t, stdcopy.Stdout, stdoutPayload))
	raw.Write(frameDockerStream(t, stdcopy.Stderr, stderrPayload))

	got, err := readDemuxedStream(bytes.NewReader(raw.Bytes()))
	require.NoError(t, err)

	// stdout and stderr are merged into a single clean log buffer.
	assert.Contains(t, got, "hello world")
	assert.Contains(t, got, `{"level":"info","msg":"Using docker host..."}`)

	// No Docker stream-header bytes survive. \x02\x00 is the stderr-frame
	// header signature seen leading the corrupted CI logs; \x01\x00 is stdout.
	assert.NotContains(t, got, "\x02\x00")
	assert.NotContains(t, got, "\x01\x00")
	assert.False(t, strings.ContainsRune(got, '\x00'), "no NUL padding bytes should remain")
}

// TestReadDemuxedStream_RawStreamPassesThrough verifies a raw (TTY) attach with
// no Docker framing is returned unchanged, so the demux path never mangles an
// already-clean stream (the Docker Desktop / TTY case).
func TestReadDemuxedStream_RawStreamPassesThrough(t *testing.T) {
	t.Parallel()

	raw := `{"level":"info","msg":"already clean"}` + "\nplain line\n"
	got, err := readDemuxedStream(strings.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, raw, got)
}

// TestReadDemuxedStream_NilReader confirms a nil reader yields empty output
// rather than panicking (the Exec call can return a nil reader).
func TestReadDemuxedStream_NilReader(t *testing.T) {
	t.Parallel()

	got, err := readDemuxedStream(nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestLooksMultiplexed table-checks the frame-header detector that gates demux:
// a valid stdout/stderr frame is multiplexed; raw JSON, short input, bad
// padding, an unknown stream type, and an overrunning length are not.
func TestLooksMultiplexed(t *testing.T) {
	t.Parallel()

	validFrame := frameDockerStream(t, stdcopy.Stdout, "hello world\n")
	badPadding := append([]byte{1, 1, 0, 0, 0, 0, 0, 3}, []byte("abc")...)
	unknownType := append([]byte{9, 0, 0, 0, 0, 0, 0, 3}, []byte("abc")...)
	overrun := []byte{1, 0, 0, 0, 0, 0, 0, 255}

	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "valid stdout frame", data: validFrame, want: true},
		{name: "raw json is not framed", data: []byte(`{"level":"info"}`), want: false},
		{name: "shorter than header", data: []byte{1, 0, 0}, want: false},
		{name: "non-zero padding", data: badPadding, want: false},
		{name: "unknown stream type", data: unknownType, want: false},
		{name: "length overruns buffer", data: overrun, want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, looksMultiplexed(tt.data))
		})
	}
}

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
