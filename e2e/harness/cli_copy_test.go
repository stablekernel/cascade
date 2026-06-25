package harness

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCopier records CopyToContainer calls and fails the first failUntil of
// them, letting tests drive copyCLIToContainer's retry path without Docker.
type fakeCopier struct {
	failUntil int   // number of leading attempts that return err
	err       error // error returned while failing
	calls     int   // total CopyToContainer invocations
	lastBytes []byte
	lastPath  string
}

func (f *fakeCopier) CopyToContainer(_ context.Context, content []byte, path string, _ int64) error {
	f.calls++
	f.lastBytes = content
	f.lastPath = path
	if f.calls <= f.failUntil {
		return f.err
	}
	return nil
}

func TestCopyCLIToContainer_SucceedsFirstTry(t *testing.T) {
	t.Parallel()

	fc := &fakeCopier{}
	content := []byte("cascade-binary-bytes")

	err := copyCLIToContainer(context.Background(), fc, content, "/usr/local/bin/cascade")

	require.NoError(t, err)
	assert.Equal(t, 1, fc.calls, "a clean copy must not retry")
	assert.Equal(t, content, fc.lastBytes)
	assert.Equal(t, "/usr/local/bin/cascade", fc.lastPath)
}

func TestCopyCLIToContainer_RetriesTransientThenSucceeds(t *testing.T) {
	// Zero the backoff so the retry path runs fast.
	orig := cliCopyBackoff
	cliCopyBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { cliCopyBackoff = orig })

	fc := &fakeCopier{failUntil: cliCopyMaxAttempts - 1, err: errors.New("docker daemon connection reset")}

	err := copyCLIToContainer(context.Background(), fc, []byte("x"), "/dst")

	require.NoError(t, err, "a copy that succeeds within the budget must not surface an error")
	assert.Equal(t, cliCopyMaxAttempts, fc.calls)
}

func TestCopyCLIToContainer_FailsClosedAfterBudget(t *testing.T) {
	orig := cliCopyBackoff
	cliCopyBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { cliCopyBackoff = orig })

	sentinel := errors.New("archive/tar: write too long")
	fc := &fakeCopier{failUntil: cliCopyMaxAttempts + 5, err: sentinel}

	err := copyCLIToContainer(context.Background(), fc, []byte("x"), "/dst")

	require.Error(t, err, "an exhausted retry budget must surface an error, not swallow it")
	assert.ErrorIs(t, err, sentinel, "the underlying copy error must be wrapped, not lost")
	assert.Equal(t, cliCopyMaxAttempts, fc.calls, "retries are bounded by cliCopyMaxAttempts")
}

func TestCopyCLIToContainer_HonorsContextCancellation(t *testing.T) {
	// Keep a real backoff so the cancelled context is observed during the wait.
	fc := &fakeCopier{failUntil: cliCopyMaxAttempts, err: errors.New("blip")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the first retry's backoff

	err := copyCLIToContainer(ctx, fc, []byte("x"), "/dst")

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, fc.calls, "cancellation must stop further attempts")
}

// TestStableSliceTarSizeMatches proves the invariant the #336 fix relies on:
// when the tar header size and the payload both come from one immutable byte
// slice, archive/tar writes exactly Size bytes with no "write too long" or
// short-write mismatch. This is the deterministic property CopyToContainer
// gives us that CopyFileToContainer (stat-then-stream) does not.
func TestStableSliceTarSizeMatches(t *testing.T) {
	t.Parallel()

	content := bytes.Repeat([]byte("cascade"), 4096) // arbitrary stable payload

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "usr/local/bin/cascade",
		Mode: 0o755,
		Size: int64(len(content)), // declared size derives from the SAME slice
	}))
	n, err := tw.Write(content)
	require.NoError(t, err, "writing exactly Size bytes must not error")
	require.NoError(t, tw.Close(), "closing after a size-matched write must not error")
	assert.Equal(t, len(content), n)

	// Round-trip: read the archive back and confirm the bytes are intact.
	tr := tar.NewReader(&buf)
	hdr, err := tr.Next()
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), hdr.Size)
	got, err := io.ReadAll(tr)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestActStartupBudgets(t *testing.T) {
	t.Parallel()

	// The readiness probe must carry a cold-pull-aware budget that is still a
	// hard cap, and poll briskly. These guard against a silent regression to the
	// 60s testcontainers default that let the cold-image start flake (#338).
	assert.GreaterOrEqual(t, actStartupTimeout, 3*time.Minute,
		"startup budget must tolerate a one-time cold image pull")
	assert.Greater(t, actStartupPollInterval, time.Duration(0),
		"poll interval must be positive")
	assert.Less(t, actStartupPollInterval, actStartupTimeout,
		"poll interval must be far smaller than the overall budget")
}
