// Package statewrite provides the shared optimistic-lock retry used by every
// finalize verb (orchestrate, promote, rollback, hotfix) when it commits the
// manifest to the trunk branch through the GitHub Contents REST API.
//
// Concurrent finalize jobs for different environments mutate disjoint keys in
// the same manifest file. They do not conflict semantically, but they collide
// on the file blob SHA: the second writer to PUT with a now-stale SHA gets an
// HTTP 409 ("does not match <sha>") and its state is dropped. CommitWithRetry
// closes that race by expressing the write as a read-modify-write that re-reads
// the current manifest, re-applies the caller's mutation on top of whatever the
// other writer committed, and re-PUTs, retrying on 409.
package statewrite

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/stablekernel/cascade/internal/log"
)

// maxAttempts bounds the read-modify-write retry loop. It is sized to survive a
// realistic concurrent wave (every component of a monorepo racing to write its
// own leaf into one shared manifest file on trunk) rather than only the handful
// of envs that finalize in parallel, and is aligned with the git push-retry
// ceiling in internal/git so a single grep of the "cascade-state-write" marker
// covers every write path.
const maxAttempts = 10

// retryBaseBackoff is the base delay between optimistic-lock retries. The
// effective wait grows exponentially per attempt (capped at maxRetryBackoff) and
// carries a random jitter of up to the base, so concurrent writers de-synchronize
// rather than colliding again in lockstep. See backoffForAttempt.
const retryBaseBackoff = 250 * time.Millisecond

// maxRetryBackoff caps the exponential growth of the per-attempt backoff so a
// late retry never sleeps for an unbounded stretch.
const maxRetryBackoff = 8 * time.Second

// defaultBotName and defaultBotEmail are the identity stamped on a state commit
// when the manifest git config supplies no override. They match the identity the
// git-based state writers use, so a Contents API commit is attributed to the
// automation bot rather than the token owner.
const (
	defaultBotName  = "github-actions[bot]"
	defaultBotEmail = "github-actions[bot]@users.noreply.github.com"
)

// Identity is the name and email stamped as both the author and the committer of
// a Contents API state commit. State writers populate it from the manifest git
// config (GetGitUserName/GetGitUserEmail) so automated commits are attributed to
// the bot identity rather than the token owner that GitHub would otherwise use.
type Identity struct {
	// Name is the commit author/committer name. Empty falls back to the bot default.
	Name string
	// Email is the commit author/committer email. Empty falls back to the bot default.
	Email string
}

// OrDefault returns the identity with any empty field filled from the bot
// default, so a commit is always attributed to a concrete identity and behavior
// is never worse than before this attribution was threaded through. It is
// exported so other state writers that build their own Contents API request can
// resolve the same identity without duplicating the bot defaults.
func (id Identity) OrDefault() Identity {
	if id.Name == "" {
		id.Name = defaultBotName
	}
	if id.Email == "" {
		id.Email = defaultBotEmail
	}
	return id
}

// ContentsClient is the minimal GitHub Contents API surface the retry loop
// needs. The production implementation shells out to the gh CLI; tests inject a
// fake that returns a 409 on the first PUT and succeeds on the second.
//
// GetContent returns the current file bytes and its blob SHA at ref. A real
// HTTP 404 returns an error satisfying IsNotFound; a state write only ever
// targets an already committed manifest, so absence is a hard failure, not a
// create-on-first-write signal. PutContent writes content at ref guarded by
// sha as the optimistic-lock token (a non-empty sha is required) and returns
// an error satisfying IsConflict when the blob SHA no longer matches.
type ContentsClient interface {
	GetContent(repo, path, ref string) (content []byte, sha string, err error)
	PutContent(repo, path, ref, sha, message string, content []byte, author Identity) error
}

// NotFoundError reports that the Contents API GET for the manifest returned a
// real HTTP 404. GitHub deliberately answers 404 both for a file that does not
// exist and for a repository the token cannot read (it never confirms a
// private repo's existence with a 403), so the two causes are
// indistinguishable at the API. Either way the state write cannot proceed and
// retrying cannot help, so CommitWithRetry fails fast on it instead of
// spending the backoff budget.
type NotFoundError struct {
	// Repo, Path, and Ref locate the lookup that 404ed, so the operator can
	// spot a wrong path or repo slug at a glance.
	Repo, Path, Ref string
	// Detail carries the gh CLI's diagnostic text (stderr plus the API's JSON
	// error body).
	Detail string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("manifest not found at %s/%s@%s (file absent or token lacks repo access): %s",
		e.Repo, e.Path, e.Ref, e.Detail)
}

// IsNotFound reports whether err is (or wraps) a Contents API 404.
func IsNotFound(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
}

// ConflictError reports an optimistic-lock (HTTP 409) failure from the Contents
// API, the signal CommitWithRetry retries on. Clients wrap the API's 409 in this
// type so the retry loop does not have to string-match raw gh output.
type ConflictError struct {
	// Err is the underlying transport or CLI error, surfaced when the retry
	// bound is exhausted.
	Err error
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("contents API conflict (409): %v", e.Err)
}

// Unwrap exposes the wrapped error to errors.Is/As.
func (e *ConflictError) Unwrap() error { return e.Err }

// IsConflict reports whether err is (or wraps) a Contents API 409 conflict. It
// recognizes the typed ConflictError and both raw gh-CLI 409 bodies, so a client
// that forwards the gh error verbatim still triggers a retry: the blob If-Match
// mismatch carries "does not match", and the branch-ref compare-and-swap failure
// two racing finalizes produce reads "... is at X but expected Y ..." with no
// "does not match". Either lock marker alongside a "409" or "Conflict" status is
// a conflict; this mirrors classifyPutError exactly.
func IsConflict(err error) bool {
	if err == nil {
		return false
	}
	var ce *ConflictError
	if asConflict(err, &ce) {
		return true
	}
	msg := err.Error()
	return (strings.Contains(msg, "does not match") || strings.Contains(msg, "is at")) &&
		(strings.Contains(msg, "409") || strings.Contains(msg, "Conflict"))
}

// asConflict is a tiny errors.As wrapper kept local so the package has no hard
// dependency surface beyond the standard library at its call sites.
func asConflict(err error, target **ConflictError) bool {
	for err != nil {
		if ce, ok := err.(*ConflictError); ok { //nolint:errorlint // walked manually below
			*target = ce
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Mutate applies a caller's state change to the current manifest bytes and
// returns the new bytes to write. It MUST be re-appliable: CommitWithRetry calls
// it again after re-fetching the manifest on a 409, so it must derive the new
// bytes purely from the current bytes it is handed (for example, parse them,
// set only this env's ci.state.<env> keys, and re-marshal) rather than from a
// stale in-memory snapshot. Re-fetching picks up the other writer's committed
// keys, and re-applying preserves both.
type Mutate func(current []byte) ([]byte, error)

// Options carries the inputs CommitWithRetry needs. Required identity fields are
// explicit; Sleep is optional and defaults to time.Sleep.
type Options struct {
	// Client performs the Contents API get/put. Required.
	Client ContentsClient
	// Repo is the "owner/name" repository slug. Required.
	Repo string
	// Path is the repo-relative manifest path. Required.
	Path string
	// Ref is the branch the write targets (the trunk branch). Required.
	Ref string
	// Message is the commit message for the write. Required.
	Message string
	// Mutate derives the bytes to write from the current manifest bytes. It is
	// re-applied on every retry. Required.
	Mutate Mutate
	// Author is the identity stamped as both author and committer of the state
	// commit. Callers populate it from the manifest git config so the commit is
	// attributed to the bot identity. An empty field falls back to the
	// github-actions[bot] default.
	Author Identity
	// Sleep is called between retries. Defaults to time.Sleep; tests inject a
	// no-op so no real time passes.
	Sleep func(time.Duration)
}

// CommitWithRetry performs an optimistic-locked read-modify-write of the
// manifest at opts.Ref. It fetches the current manifest and blob SHA, applies
// opts.Mutate to the current bytes, and PUTs with that SHA. On a 409 conflict it
// re-fetches, re-applies the mutation on top of the now-current manifest, and
// re-PUTs, up to maxAttempts with a staggered backoff. It returns the last
// conflict (or any non-409 error) when the bound is exhausted, so a genuinely
// stuck write still surfaces.
//
// Because Mutate is re-applied against the freshly fetched manifest, two
// finalize jobs that each set only their own env's ci.state.<env> keys merge:
// the loser re-reads the winner's committed keys and re-applies its own on top,
// so the final manifest carries both.
func CommitWithRetry(opts Options) error {
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	author := opts.Author.OrDefault()

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// A stable, greppable marker per attempt lets a live concurrency proof
		// confirm every lane converged: the same token the git push-retry path
		// emits, so one grep covers all write paths.
		log.Info("cascade-state-write: attempt=%d/%d", attempt, maxAttempts)

		current, sha, err := opts.Client.GetContent(opts.Repo, opts.Path, opts.Ref)
		if err != nil {
			if IsNotFound(err) {
				// A 404 is not transient: the manifest is genuinely absent (deleted,
				// or the path/repo is wrong) or the token cannot see a private repo
				// (GitHub answers 404 there, never 403). Retrying would burn the full
				// backoff budget and end in a generic empty-manifest error with the
				// real cause discarded, so surface it immediately.
				return fmt.Errorf("state write via API failed: %w", err)
			}
			// A re-fetch failure (a transient non-2xx or transport blip from the
			// Contents-API GET) is retryable exactly like a 409: retry within the
			// bounded loop rather than aborting the whole write on a blip. Crucially,
			// a failed fetch must never be parsed as a manifest.
			lastErr = fmt.Errorf("reading current manifest for state write: %w", err)
			log.Info("cascade-state-write: refetch-error attempt=%d/%d", attempt, maxAttempts)
			if attempt < maxAttempts {
				sleep(backoffForAttempt(retryBaseBackoff, attempt-1))
			}
			continue
		}
		if len(strings.TrimSpace(string(current))) == 0 {
			// An empty body from a successful-looking GET is a transient read, not a
			// real manifest: the state-write path only ever targets an already
			// committed manifest, and Mutate derives its bytes by parsing the current
			// manifest, so it cannot produce anything valid from nothing. Parsing the
			// empty content would surface a spurious "missing 'ci' key" and, worse,
			// risk PUTting bytes derived from garbage. Treat it as a retryable
			// re-fetch, never as content.
			lastErr = fmt.Errorf("re-fetched current manifest for state write was empty")
			log.Info("cascade-state-write: refetch-empty attempt=%d/%d", attempt, maxAttempts)
			if attempt < maxAttempts {
				sleep(backoffForAttempt(retryBaseBackoff, attempt-1))
			}
			continue
		}

		next, err := opts.Mutate(current)
		if err != nil {
			return fmt.Errorf("applying state mutation: %w", err)
		}

		err = opts.Client.PutContent(opts.Repo, opts.Path, opts.Ref, sha, opts.Message, next, author)
		if err == nil {
			log.Info("cascade-state-write: ok attempt=%d", attempt)
			return nil
		}
		if !IsConflict(err) {
			return fmt.Errorf("state write via API failed: %w", err)
		}

		// Optimistic-lock conflict: another writer committed between our read
		// and our PUT. Re-fetch, re-apply, and retry so both writers' state
		// merges rather than one being dropped.
		lastErr = err
		if attempt < maxAttempts {
			sleep(backoffForAttempt(retryBaseBackoff, attempt-1))
		}
	}

	log.Info("cascade-state-write: exhausted attempts=%d", maxAttempts)
	if IsConflict(lastErr) {
		return fmt.Errorf("state write via API still conflicting after %d attempts: %w", maxAttempts, lastErr)
	}
	// Exhaustion driven by a persistently empty or errored re-fetch: surface a
	// distinct, non-corrupting error rather than a spurious parse failure.
	return fmt.Errorf("state write via API could not obtain a valid current manifest after %d attempts: %w", maxAttempts, lastErr)
}

// backoffForAttempt returns the delay before the retry following a zero-based
// attempt index: an exponential growth of base (base * 2^attempt) capped at
// maxRetryBackoff, plus a random jitter of up to base so concurrent writers
// racing the same trunk de-synchronize instead of colliding again in lockstep.
// It matches the git push-retry backoff shape so both live write paths behave
// identically under a wave.
func backoffForAttempt(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = retryBaseBackoff
	}
	d := base
	for i := 0; i < attempt && d < maxRetryBackoff; i++ {
		d *= 2
	}
	if d > maxRetryBackoff {
		d = maxRetryBackoff
	}
	return d + time.Duration(rand.Int64N(int64(base)+1))
}
