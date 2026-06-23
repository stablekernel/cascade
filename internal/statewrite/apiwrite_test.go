package statewrite

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeContents is a stub ContentsClient that models the manifest as a single
// string and lets a test script which PUTs return a 409. It records each PUT so
// tests can assert the writer re-fetched and re-applied.
type fakeContents struct {
	content string // current manifest on the "branch"
	sha     string // current blob SHA

	// putErrs is consumed one entry per PutContent call: a non-nil entry forces
	// that PUT to fail without mutating state; nil applies the write. Extra PUTs
	// past the slice default to applying successfully.
	putErrs []error

	puts    int      // number of PutContent calls
	gets    int      // number of GetContent calls
	putSeen []string // content bytes presented to each PutContent
	shaSeen []string // sha presented to each PutContent
}

func (f *fakeContents) GetContent(_, _, _ string) ([]byte, string, error) {
	f.gets++
	return []byte(f.content), f.sha, nil
}

func (f *fakeContents) PutContent(_, _, _, sha, _ string, content []byte) error {
	f.puts++
	f.putSeen = append(f.putSeen, string(content))
	f.shaSeen = append(f.shaSeen, sha)
	if f.puts-1 < len(f.putErrs) {
		if err := f.putErrs[f.puts-1]; err != nil {
			return err
		}
	}
	// Apply the write: advance the stored content and bump the blob SHA so a
	// re-fetch sees the new state under a new optimistic-lock token.
	f.content = string(content)
	f.sha = fmt.Sprintf("%s-next", sha)
	return nil
}

// appendLine is a re-appliable mutation: it adds a line for one env, idempotently
// (it never duplicates a line it already added), so re-applying on top of another
// writer's committed line preserves both. This mirrors a finalize that sets only
// its own ci.state.<env> keys.
func appendLine(line string) Mutate {
	return func(current []byte) ([]byte, error) {
		body := string(current)
		if strings.Contains(body, line) {
			return current, nil
		}
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		return []byte(body + line + "\n"), nil
	}
}

// rawConflict mirrors the raw gh-CLI 409 body so IsConflict's string path is
// exercised, wrapped in the typed ConflictError clients are expected to return.
func rawConflict() error {
	return &ConflictError{Err: errors.New(`{"message":".github/manifest.yaml does not match abc123","status":"409"}`)}
}

// noSleep is an injected sleep that records that it was called but lets no real
// time pass, keeping the test fast and deterministic.
func noSleep(calls *int) func(time.Duration) {
	return func(time.Duration) { *calls++ }
}

func TestCommitWithRetry_RetriesOn409AndMergesConcurrentWrite(t *testing.T) {
	// Env A has already committed its state line; the branch carries it. Our
	// caller (env B) sets its own line. The first PUT loses the optimistic-lock
	// race (409); the re-fetch must pick up env A's line and the re-apply must
	// preserve it while adding env B's, so the final manifest carries BOTH.
	fake := &fakeContents{
		content: "ci.state.test: A\n",
		sha:     "sha-0",
		putErrs: []error{rawConflict()}, // first PUT 409s, second succeeds
	}

	var slept int
	start := time.Now()
	err := CommitWithRetry(Options{
		Client:  fake,
		Repo:    "owner/name",
		Path:    ".github/manifest.yaml",
		Ref:     "main",
		Message: "chore: record state on staging",
		Mutate:  appendLine("ci.state.staging: B"),
		Sleep:   noSleep(&slept),
	})

	require.NoError(t, err)
	// Re-fetched after the 409: two GETs, two PUTs.
	assert.Equal(t, 2, fake.gets, "writer must re-fetch the manifest after a 409")
	assert.Equal(t, 2, fake.puts, "writer must re-PUT after a 409")
	assert.Equal(t, 1, slept, "writer must back off once between the two attempts")
	// Merge semantics: the final committed manifest carries BOTH env A's and
	// env B's state, proving the mutation was re-applied on top of the winner.
	assert.Contains(t, fake.content, "ci.state.test: A", "the other writer's state must survive")
	assert.Contains(t, fake.content, "ci.state.staging: B", "this caller's state must be written")
	// No real time elapsed: the injected sleep is a no-op.
	assert.Less(t, time.Since(start), time.Second, "injected sleep must let no real time pass")
}

func TestCommitWithRetry_ErrorsAfterBoundedAttempts(t *testing.T) {
	// Every PUT 409s: the writer must exhaust its bound and surface the conflict
	// rather than spinning forever or swallowing the error.
	errs := make([]error, maxAttempts)
	for i := range errs {
		errs[i] = rawConflict()
	}
	fake := &fakeContents{content: "ci.state.test: A\n", sha: "sha-0", putErrs: errs}

	var slept int
	err := CommitWithRetry(Options{
		Client:  fake,
		Repo:    "owner/name",
		Path:    ".github/manifest.yaml",
		Ref:     "main",
		Message: "chore: record state",
		Mutate:  appendLine("ci.state.staging: B"),
		Sleep:   noSleep(&slept),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "still conflicting")
	assert.True(t, IsConflict(err), "the surfaced error must remain recognizable as a 409")
	assert.Equal(t, maxAttempts, fake.puts, "writer must try exactly the bounded number of times")
	assert.Equal(t, maxAttempts-1, slept, "writer must back off between attempts but not after the last")
}

func TestCommitWithRetry_NonConflictErrorIsNotRetried(t *testing.T) {
	// A non-409 error (e.g. auth) must surface immediately without retrying.
	fake := &fakeContents{
		content: "ci.state.test: A\n",
		sha:     "sha-0",
		putErrs: []error{errors.New("HTTP 401: bad credentials")},
	}

	var slept int
	err := CommitWithRetry(Options{
		Client:  fake,
		Repo:    "owner/name",
		Path:    ".github/manifest.yaml",
		Ref:     "main",
		Message: "chore: record state",
		Mutate:  appendLine("ci.state.staging: B"),
		Sleep:   noSleep(&slept),
	})

	require.Error(t, err)
	assert.False(t, IsConflict(err))
	assert.Equal(t, 1, fake.puts, "a non-409 error must not be retried")
	assert.Equal(t, 0, slept, "a non-409 error must not back off")
}

func TestIsConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"typed conflict", &ConflictError{Err: errors.New("boom")}, true},
		{"raw gh 409 body", errors.New(`{"message":"manifest.yaml does not match abc","status":"409"}`), true},
		{"wrapped typed conflict", fmt.Errorf("committing state: %w", &ConflictError{Err: errors.New("boom")}), true},
		{"unrelated error", errors.New("HTTP 500"), false},
		{"does-not-match without 409", errors.New("ref does not match"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsConflict(tc.err))
		})
	}
}
