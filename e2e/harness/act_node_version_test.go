package harness

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// nodeVersionExecer is a containerExecer that scripts a single `node --version`
// response without Docker, so the act-image Node-floor preflight is exercisable
// in a plain unit test. It records the command it was handed so a test can
// confirm the preflight asks for the node version.
type nodeVersionExecer struct {
	code     int
	output   string
	err      error
	gotCmd   []string
	gotMulti bool
}

func (n *nodeVersionExecer) Exec(_ context.Context, cmd []string, opts ...tcexec.ProcessOption) (int, io.Reader, error) {
	n.gotCmd = cmd
	n.gotMulti = len(opts) > 0
	if n.err != nil {
		return 0, nil, n.err
	}
	return n.code, strings.NewReader(n.output), nil
}

// TestParseNodeMajor covers the version-string shapes `node --version` can
// produce, plus the malformed inputs that must fail loudly rather than read as
// major 0.
func TestParseNodeMajor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantMajor int
		wantErr   bool
	}{
		{name: "standard with v prefix", input: "v24.17.0", wantMajor: 24},
		{name: "no v prefix", input: "24.17.0", wantMajor: 24},
		{name: "older major", input: "v20.11.1", wantMajor: 20},
		{name: "major only", input: "v24", wantMajor: 24},
		{name: "surrounding whitespace", input: "  v18.0.0\n", wantMajor: 18},
		{name: "two digit major", input: "v100.0.0", wantMajor: 100},
		{name: "empty", input: "", wantErr: true},
		{name: "whitespace only", input: "  \n", wantErr: true},
		{name: "v only", input: "v", wantErr: true},
		{name: "non numeric", input: "vabc.1.2", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseNodeMajor(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantMajor, got)
		})
	}
}

// TestAssertNodeMajorAtLeast_MeetsFloor proves the preflight passes when the
// image's Node major is at or above the required floor, and that it asks the
// container for the node version over a multiplexed (header-stripped) stream.
func TestAssertNodeMajorAtLeast_MeetsFloor(t *testing.T) {
	t.Parallel()

	exec := &nodeVersionExecer{code: 0, output: "v24.17.0\n"}
	err := assertNodeMajorAtLeast(context.Background(), exec, 24)
	require.NoError(t, err)
	assert.Equal(t, []string{"node", "--version"}, exec.gotCmd)
	assert.True(t, exec.gotMulti, "version check must request a multiplexed reader so Docker frame headers are stripped")
}

// TestAssertNodeMajorAtLeast_BelowFloor proves a stale image whose Node major is
// below the floor fails with a message that names the deficient version and
// tells the reader to repin, instead of letting a Node 24 action crash later.
func TestAssertNodeMajorAtLeast_BelowFloor(t *testing.T) {
	t.Parallel()

	exec := &nodeVersionExecer{code: 0, output: "v20.11.1\n"}
	err := assertNodeMajorAtLeast(context.Background(), exec, 24)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Node 20")
	assert.Contains(t, err.Error(), "Repin")
	assert.Contains(t, err.Error(), actRunnerImage)
}

// TestAssertNodeMajorAtLeast_NonZeroExit surfaces a node binary that exists but
// exits non-zero (e.g. a broken install) rather than treating it as a pass.
func TestAssertNodeMajorAtLeast_NonZeroExit(t *testing.T) {
	t.Parallel()

	exec := &nodeVersionExecer{code: 127, output: "node: command not found"}
	err := assertNodeMajorAtLeast(context.Background(), exec, 24)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited 127")
}

// TestAssertNodeMajorAtLeast_ExecError surfaces a Docker transport failure on
// the version check as an error rather than a silent pass.
func TestAssertNodeMajorAtLeast_ExecError(t *testing.T) {
	t.Parallel()

	exec := &nodeVersionExecer{err: errors.New("docker daemon gone")}
	err := assertNodeMajorAtLeast(context.Background(), exec, 24)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exec failed")
}

// TestAssertNodeMajorAtLeast_GarbledOutput proves an unparseable version string
// fails the preflight (with the image named) instead of reading as major 0 and
// tripping a confusing below-floor message.
func TestAssertNodeMajorAtLeast_GarbledOutput(t *testing.T) {
	t.Parallel()

	exec := &nodeVersionExecer{code: 0, output: "not-a-version"}
	err := assertNodeMajorAtLeast(context.Background(), exec, 24)
	require.Error(t, err)
	assert.Contains(t, err.Error(), actRunnerImage)
}
