package statewrite

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stablekernel/cascade/internal/log"
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

	// getErrs is consumed one entry per GetContent call: a non-nil entry forces
	// that GET to return an error (a transient re-fetch failure). Entries past the
	// slice default to a successful read of the stored content.
	getErrs []error
	// getContents is consumed one entry per GetContent call: a non-nil entry
	// overrides the bytes that GET returns (an empty string models a transient
	// empty body). A nil entry, or an index past the slice, reads the stored
	// content. It is a pointer slice so an empty override is distinguishable from
	// "use the stored content".
	getContents []*string

	puts    int        // number of PutContent calls
	gets    int        // number of GetContent calls
	putSeen []string   // content bytes presented to each PutContent
	shaSeen []string   // sha presented to each PutContent
	idSeen  []Identity // author/committer identity presented to each PutContent
}

func (f *fakeContents) GetContent(_, _, _ string) ([]byte, string, error) {
	i := f.gets
	f.gets++
	if i < len(f.getErrs) && f.getErrs[i] != nil {
		return nil, "", f.getErrs[i]
	}
	if i < len(f.getContents) && f.getContents[i] != nil {
		return []byte(*f.getContents[i]), f.sha, nil
	}
	return []byte(f.content), f.sha, nil
}

// strptr is a tiny helper so a test can script an exact GET body (including the
// empty string that models a transient empty re-fetch).
func strptr(s string) *string { return &s }

func (f *fakeContents) PutContent(_, _, _, sha, _ string, content []byte, author Identity) error {
	f.puts++
	f.putSeen = append(f.putSeen, string(content))
	f.shaSeen = append(f.shaSeen, sha)
	f.idSeen = append(f.idSeen, author)
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

// branchRefCAS409Body mirrors the second 409 shape GitHub returns: a branch-ref
// compare-and-swap failure whose body reads "... is at X but expected Y ..."
// and carries NO "does not match" substring. Two component finalizes racing to
// update one trunk branch produce this shape, so the classifier must recognize
// it as a conflict from the "is at" marker alongside the 409/Conflict status.
func branchRefCAS409Body() string {
	return `PUT https://api.github.com/repos/x/y/contents/state.json: 409 Conflict [] {"message":"Update is at abc123 but expected def456","documentation_url":"https://docs.github.com"}`
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

func TestCommitWithRetry_RetryCeilingIsTen(t *testing.T) {
	// A realistic monorepo wave races every component to write its own leaf into
	// one manifest on the trunk branch, so the read-modify-write must tolerate far
	// more than the handful of parallel envs the original bound assumed. Pin the
	// concrete ceiling so a silent regression back to a low bound is caught.
	require.Equal(t, 10, maxAttempts, "state-write retry ceiling must be raised to ten")

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
	assert.Equal(t, 10, fake.puts, "writer must make ten attempts before exhausting")
	assert.Equal(t, 9, slept, "writer must back off between attempts but not after the last")
}

func TestCommitWithRetry_EmitsConvergenceMarker(t *testing.T) {
	// The retry loop must emit a stable, greppable marker per attempt plus an "ok"
	// marker on success, so a live concurrency proof can grep every lane and assert
	// convergence with zero exhaustion. Capture the log output to observe them.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetColors(false)
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetColors(true) })

	fake := &fakeContents{
		content: "ci.state.test: A\n",
		sha:     "sha-0",
		putErrs: []error{rawConflict()}, // first PUT 409s, second succeeds
	}

	var slept int
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
	out := buf.String()
	assert.Contains(t, out, "cascade-state-write: attempt=1/10", "first attempt marker must be emitted")
	assert.Contains(t, out, "cascade-state-write: attempt=2/10", "the retry attempt marker must be emitted")
	assert.Contains(t, out, "cascade-state-write: ok attempt=2", "a successful write must emit the ok marker with its attempt count")
}

func TestCommitWithRetry_EmitsExhaustionMarker(t *testing.T) {
	// When every attempt conflicts the loop must emit a stable exhaustion marker so
	// a live proof asserts convergence by the absence of this token across lanes.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetColors(false)
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetColors(true) })

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
	assert.Contains(t, buf.String(), "cascade-state-write: exhausted attempts=10",
		"an exhausted retry loop must emit the exhaustion marker")
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

func TestCommitWithRetry_StampsBotAuthorAndCommitter(t *testing.T) {
	// State commits must be attributed to the bot identity (both author and
	// committer) so GitHub does not stamp them with the token owner. With no
	// override the identity defaults to github-actions[bot]; a manifest git
	// override flows through verbatim.
	tests := []struct {
		name string
		give Identity
		want Identity
	}{
		{
			name: "defaults to github-actions[bot] when unset",
			give: Identity{},
			want: Identity{
				Name:  "github-actions[bot]",
				Email: "github-actions[bot]@users.noreply.github.com",
			},
		},
		{
			name: "honors a manifest git override",
			give: Identity{Name: "release-bot", Email: "release-bot@example.com"},
			want: Identity{Name: "release-bot", Email: "release-bot@example.com"},
		},
		{
			name: "fills only the missing field from the default",
			give: Identity{Name: "release-bot"},
			want: Identity{Name: "release-bot", Email: "github-actions[bot]@users.noreply.github.com"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeContents{content: "ci.state.test: A\n", sha: "sha-0"}

			err := CommitWithRetry(Options{
				Client:  fake,
				Repo:    "owner/name",
				Path:    ".github/manifest.yaml",
				Ref:     "main",
				Message: "chore: update state [skip ci]",
				Mutate:  appendLine("ci.state.staging: B"),
				Author:  tc.give,
				Sleep:   noSleep(new(int)),
			})

			require.NoError(t, err)
			require.Len(t, fake.idSeen, 1, "exactly one PUT must be issued")
			assert.Equal(t, tc.want, fake.idSeen[0], "the PUT must carry the resolved bot identity")
		})
	}
}

func TestCommitWithRetry_IdentityPersistsAcrossRetries(t *testing.T) {
	// A 409 retry re-fetches and re-PUTs; the resolved identity must accompany
	// every attempt, not just the first.
	fake := &fakeContents{
		content: "ci.state.test: A\n",
		sha:     "sha-0",
		putErrs: []error{rawConflict()},
	}

	err := CommitWithRetry(Options{
		Client:  fake,
		Repo:    "owner/name",
		Path:    ".github/manifest.yaml",
		Ref:     "main",
		Message: "chore: update state [skip ci]",
		Mutate:  appendLine("ci.state.staging: B"),
		Author:  Identity{Name: "release-bot", Email: "release-bot@example.com"},
		Sleep:   noSleep(new(int)),
	})

	require.NoError(t, err)
	require.Len(t, fake.idSeen, 2, "the 409 must drive a second PUT")
	for i, got := range fake.idSeen {
		assert.Equal(t, Identity{Name: "release-bot", Email: "release-bot@example.com"}, got,
			"attempt %d must carry the configured identity", i+1)
	}
}

func TestIsConflict_BranchRefCAS409(t *testing.T) {
	body := branchRefCAS409Body()
	// Typed path: a client that wraps the branch-ref 409 in ConflictError.
	assert.True(t, IsConflict(&ConflictError{Err: errors.New(body)}),
		"a typed conflict wrapping the branch-ref 409 must be recognized")
	// String path: a client forwarding the raw gh body verbatim, which carries
	// no "does not match" substring, must still be recognized as a conflict.
	assert.True(t, IsConflict(errors.New(body)),
		"the raw branch-ref 409 body must be recognized without a \"does not match\" substring")
	// Negative: a genuine non-409 error must not classify as a conflict.
	assert.False(t, IsConflict(errors.New("HTTP 500 Internal Server Error")),
		"a 500 is not a conflict")
	assert.False(t, IsConflict(errors.New("the runner is at the front of the queue")),
		"an \"is at\" message without a 409/Conflict marker is not a conflict")
}

func TestCommitWithRetry_RetriesOnBranchRefCAS409(t *testing.T) {
	// The first PUT loses a branch-ref compare-and-swap race: GitHub returns a
	// 409 whose body reads "... is at X but expected Y ..." with NO "does not
	// match" substring. The forwarded raw error must still be recognized as a
	// conflict so the writer re-fetches, re-applies, and succeeds on the retry
	// rather than hard-failing after a single attempt.
	fake := &fakeContents{
		content: "ci.state.test: A\n",
		sha:     "sha-0",
		putErrs: []error{errors.New(branchRefCAS409Body())}, // raw forwarded error, not typed
	}

	var slept int
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
	assert.GreaterOrEqual(t, fake.puts, 2, "the branch-ref 409 must drive at least a second PUT")
	assert.Equal(t, 2, fake.gets, "writer must re-fetch the manifest after the branch-ref 409")
	assert.Equal(t, 1, slept, "writer must back off once between the two attempts")
	// Merge semantics: both writers' state survives the re-fetch and re-apply.
	assert.Contains(t, fake.content, "ci.state.test: A", "the other writer's state must survive")
	assert.Contains(t, fake.content, "ci.state.staging: B", "this caller's state must be written")
}

// parseGuardMutate mimics the real state mutations (WriteScopedState /
// serializeState), which parse the current bytes and reject an empty manifest
// with the config parser's "missing required 'ci' key" error. It is the exact
// failure that a spurious empty re-fetch surfaced on real GitHub, so a test that
// injects an empty re-fetch and asserts this error never escapes proves the
// guard.
func parseGuardMutate(line string) Mutate {
	return func(current []byte) ([]byte, error) {
		if len(bytes.TrimSpace(current)) == 0 {
			return nil, fmt.Errorf("parsing current manifest: manifest file missing required 'ci' key at top level")
		}
		return appendLine(line)(current)
	}
}

func TestCommitWithRetry_RetriesOnEmptyRefetchThenSucceeds(t *testing.T) {
	// The first GET returns an empty body (a transient Contents-API read that the
	// gh client turns into empty content with a nil error), the second returns the
	// real manifest. An empty re-fetch must NOT be parsed as a manifest: doing so
	// emits a spurious "missing 'ci' key". The writer must retry the fetch and
	// ultimately succeed against the real bytes.
	fake := &fakeContents{
		content:     "ci.state.test: A\n",
		sha:         "sha-0",
		getContents: []*string{strptr("")}, // first GET is empty, then the stored content
	}

	var slept int
	err := CommitWithRetry(Options{
		Client:  fake,
		Repo:    "owner/name",
		Path:    ".github/manifest.yaml",
		Ref:     "main",
		Message: "chore: record state on staging",
		Mutate:  parseGuardMutate("ci.state.staging: B"),
		Sleep:   noSleep(&slept),
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, fake.gets, 2, "an empty re-fetch must be retried, not parsed")
	assert.Equal(t, 1, fake.puts, "exactly one PUT lands once a valid manifest is fetched")
	assert.Contains(t, fake.content, "ci.state.test: A", "the real manifest must survive")
	assert.Contains(t, fake.content, "ci.state.staging: B", "this caller's state must be written")
}

func TestCommitWithRetry_RetriesOnErroredRefetchThenSucceeds(t *testing.T) {
	// The first GET fails with a transient error; the second succeeds. A transient
	// re-fetch error is retryable exactly like a 409: the writer must retry within
	// the bounded loop rather than aborting the whole write on a blip.
	fake := &fakeContents{
		content: "ci.state.test: A\n",
		sha:     "sha-0",
		getErrs: []error{errors.New("HTTP 502: Bad Gateway")}, // first GET errors, then succeeds
	}

	var slept int
	err := CommitWithRetry(Options{
		Client:  fake,
		Repo:    "owner/name",
		Path:    ".github/manifest.yaml",
		Ref:     "main",
		Message: "chore: record state on staging",
		Mutate:  parseGuardMutate("ci.state.staging: B"),
		Sleep:   noSleep(&slept),
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, fake.gets, 2, "a transient GET error must be retried")
	assert.Equal(t, 1, fake.puts, "exactly one PUT lands once the GET recovers")
	assert.Contains(t, fake.content, "ci.state.staging: B", "this caller's state must be written")
}

func TestCommitWithRetry_EmptyRefetchExhaustsWithNonParseError(t *testing.T) {
	// A re-fetch that stays empty for every attempt must exhaust the bound with a
	// clear, non-corrupting error: it must NEVER surface the spurious "missing 'ci'
	// key" parse error, and must NEVER PUT garbage derived from nothing.
	empties := make([]*string, maxAttempts)
	for i := range empties {
		empties[i] = strptr("")
	}
	fake := &fakeContents{content: "ci.state.test: A\n", sha: "sha-0", getContents: empties}

	var slept int
	err := CommitWithRetry(Options{
		Client:  fake,
		Repo:    "owner/name",
		Path:    ".github/manifest.yaml",
		Ref:     "main",
		Message: "chore: record state",
		Mutate:  parseGuardMutate("ci.state.staging: B"),
		Sleep:   noSleep(&slept),
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "missing required 'ci' key",
		"an empty re-fetch must never surface the spurious parse error")
	assert.Equal(t, 0, fake.puts, "the writer must never PUT bytes derived from an empty re-fetch")
}

func TestCommitWithRetry_NotFoundFailsFastWithCause(t *testing.T) {
	// A token without access to a private repo gets HTTP 404 from GitHub (not
	// 403), the same status as a genuinely absent file. Neither is transient:
	// retrying burns the whole backoff budget and ends in a generic
	// empty-manifest error with the real cause discarded. The write must fail
	// on the first attempt and surface the 404 detail.
	nf := &NotFoundError{
		Repo: "owner/private", Path: "manifest.yaml", Ref: "main",
		Detail: "gh: Not Found (HTTP 404)",
	}
	client := &fakeContents{getErrs: []error{nf, nf, nf, nf, nf, nf, nf, nf, nf, nf}}
	sleeps := 0

	err := CommitWithRetry(Options{
		Client:  client,
		Repo:    "owner/private",
		Path:    "manifest.yaml",
		Ref:     "main",
		Message: "ci: update state",
		Mutate:  appendLine("env: b"),
		Sleep:   noSleep(&sleeps),
	})

	require.Error(t, err)
	assert.Equal(t, 1, client.gets, "a 404 is not transient and must not be re-fetched")
	assert.Equal(t, 0, client.puts, "nothing may be written without a current manifest")
	assert.Equal(t, 0, sleeps, "a 404 must not sit through backoff")
	assert.True(t, IsNotFound(err), "the typed cause must survive wrapping")
	assert.Contains(t, err.Error(), "token lacks repo access")
	assert.Contains(t, err.Error(), "HTTP 404")
	assert.NotContains(t, err.Error(), "was empty", "the misleading empty-manifest exhaustion error must not appear")
}

func TestCommitWithRetry_InvalidNonEmptyManifestStillErrors(t *testing.T) {
	// Guard against masking real invalidity: a genuinely invalid but NON-empty
	// committed manifest must still error immediately (as today), not be retried
	// away. The empty-guard must only catch an empty/absent re-fetch.
	fake := &fakeContents{content: "this is not a manifest\n", sha: "sha-0"}

	var slept int
	err := CommitWithRetry(Options{
		Client: fake,
		Repo:   "owner/name",
		Path:   ".github/manifest.yaml",
		Ref:    "main",
		Message: "chore: record state",
		Mutate: func(current []byte) ([]byte, error) {
			// A non-empty but invalid manifest: the parser rejects it. This models
			// WriteScopedState refusing a manifest whose ci key is absent.
			return nil, fmt.Errorf("parsing current manifest: manifest file missing required 'ci' key at top level")
		},
		Sleep: noSleep(&slept),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required 'ci' key",
		"a real invalid manifest must still surface the parse error, not be masked")
	assert.Equal(t, 1, fake.gets, "a Mutate error on valid-length bytes must not be retried")
	assert.Equal(t, 0, fake.puts, "no PUT on an invalid manifest")
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
