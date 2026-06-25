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
	"fmt"
	"strings"
	"time"
)

// maxAttempts bounds the read-modify-write retry loop. Five attempts comfortably
// absorbs the handful of envs that can finalize in parallel without masking a
// genuinely stuck write.
const maxAttempts = 5

// retryBackoff is the base delay between optimistic-lock retries. Attempt N
// waits N*retryBackoff so concurrent writers stagger rather than re-collide.
const retryBackoff = 500 * time.Millisecond

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

// orDefault returns the identity with any empty field filled from the bot
// default, so a commit is always attributed to a concrete identity and behavior
// is never worse than before this attribution was threaded through.
func (id Identity) orDefault() Identity {
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
// GetContent returns the current file bytes and its blob SHA at ref. A file that
// does not yet exist returns an empty SHA and a nil error so the first write
// creates it. PutContent writes content at ref using sha for the optimistic
// lock (empty sha creates the file); it returns an error satisfying IsConflict
// when the blob SHA no longer matches.
type ContentsClient interface {
	GetContent(repo, path, ref string) (content []byte, sha string, err error)
	PutContent(repo, path, ref, sha, message string, content []byte, author Identity) error
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
// recognizes both the typed ConflictError and the raw gh-CLI 409 body, which
// carries "does not match" and a "409" status, so a client that forwards the
// gh error verbatim still triggers a retry.
func IsConflict(err error) bool {
	if err == nil {
		return false
	}
	var ce *ConflictError
	if asConflict(err, &ce) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "does not match") && strings.Contains(msg, "409")
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
	author := opts.Author.orDefault()

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		current, sha, err := opts.Client.GetContent(opts.Repo, opts.Path, opts.Ref)
		if err != nil {
			return fmt.Errorf("reading current manifest for state write: %w", err)
		}

		next, err := opts.Mutate(current)
		if err != nil {
			return fmt.Errorf("applying state mutation: %w", err)
		}

		err = opts.Client.PutContent(opts.Repo, opts.Path, opts.Ref, sha, opts.Message, next, author)
		if err == nil {
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
			sleep(time.Duration(attempt) * retryBackoff)
		}
	}

	return fmt.Errorf("state write via API still conflicting after %d attempts: %w", maxAttempts, lastErr)
}
