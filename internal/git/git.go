package git

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"strings"
	"time"

	"github.com/stablekernel/cascade/internal/log"
	"github.com/stablekernel/cascade/internal/taggrammar"
)

// IsValidVersionTag reports whether tag is a well-formed cascade version tag
// under the default grammar. Tags that do not match (for example a
// vX.Y.Z-dryrun.N exercise tag, a foreign "nightly" or "latest" tag, or a typo)
// are invisible to version discovery so they can never be mistaken for the
// latest released or prereleased version.
func IsValidVersionTag(tag string) bool {
	return IsValidVersionTagSpec(taggrammar.Default(), tag)
}

// IsValidVersionTagSpec reports whether tag is a well-formed version tag under
// spec. Version discovery and git both read this from the canonical grammar, so
// the predicate can never drift from the parser the way a hand-copied regex
// once could.
func IsValidVersionTagSpec(spec taggrammar.Spec, tag string) bool {
	return spec.IsVersionTag(tag)
}

// GetChangedFiles returns the list of files changed between two commits
func GetChangedFiles(baseSHA, headSHA string) ([]string, error) {
	// Handle null SHA (new branch or first commit)
	if baseSHA == "0000000000000000000000000000000000000000" {
		return getAllFiles(headSHA)
	}

	cmd := exec.Command("git", "diff", "--name-only", baseSHA, headSHA)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	return parseLines(output), nil
}

// getAllFiles returns all files in the tree at the given SHA
func getAllFiles(sha string) ([]string, error) {
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", sha)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree: %w", err)
	}

	return parseLines(output), nil
}

// Commit represents a git commit
type Commit struct {
	Hash           string
	Subject        string
	Body           string
	Author         string
	AuthorEmail    string // Author email for deduplication
	GitHubUsername string // GitHub username (looked up via API)
}

// GetCommits returns commits between two SHAs
func GetCommits(baseSHA, headSHA string, excludePaths []string) ([]Commit, error) {
	// Build command with format: hash, subject, author, author email, body separated by special markers
	// Using ASCII record separator (0x1E) and unit separator (0x1F) which won't appear in commit messages
	format := "%H\x1f%s\x1f%an\x1f%ae\x1f%b\x1e"

	args := []string{"log", fmt.Sprintf("--format=%s", format), fmt.Sprintf("%s..%s", baseSHA, headSHA)}

	// Add path exclusions if specified
	if len(excludePaths) > 0 {
		args = append(args, "--")
		for _, p := range excludePaths {
			args = append(args, ":!"+p)
		}
	}

	cmd := exec.Command("git", args...)
	output, err := cmd.Output()
	if err != nil {
		// A non-zero exit means git failed (a bad or unknown base SHA, a
		// shallow clone missing the base commit, or "not a git repository").
		// A legitimately empty range exits 0 and returns no commits via
		// parseCommits below, so any error here is a real failure and must be
		// surfaced rather than masked as "no commits."
		return nil, fmt.Errorf("git log: %w", err)
	}

	return parseCommits(output), nil
}

// GetCommitsForPaths returns commits between two SHAs whose changes touch at
// least one of includePaths, emitting `git log ... base..head -- <p1> <p2>`. It
// is the include-path sibling of GetCommits (which appends pathspecs only as
// exclusions): a component's version derivation passes its own path so a commit
// touching only a sibling component's subtree is invisible to it. With an empty
// includePaths the argv carries no `--` separator, so the result is identical to
// GetCommits(base, head, nil) and the single-component path is unchanged.
func GetCommitsForPaths(baseSHA, headSHA string, includePaths []string) ([]Commit, error) {
	format := "%H\x1f%s\x1f%an\x1f%ae\x1f%b\x1e"

	args := []string{"log", fmt.Sprintf("--format=%s", format), fmt.Sprintf("%s..%s", baseSHA, headSHA)}

	// Add include paths as a trailing pathspec so only commits touching one of
	// them are reported.
	if len(includePaths) > 0 {
		args = append(args, "--")
		args = append(args, includePaths...)
	}

	cmd := exec.Command("git", args...)
	output, err := cmd.Output()
	if err != nil {
		// A non-zero exit is a real git failure (bad base SHA, shallow clone, or
		// "not a git repository"); an empty range exits 0. Surface it rather than
		// masking it as "no commits" (mirrors GetCommits).
		return nil, fmt.Errorf("git log: %w", err)
	}

	return parseCommits(output), nil
}

func parseCommits(data []byte) []Commit {
	var commits []Commit

	// Split by record separator
	records := bytes.Split(data, []byte{0x1e})

	for _, record := range records {
		record = bytes.TrimSpace(record)
		if len(record) == 0 {
			continue
		}

		// Split by unit separator: hash, subject, author, author email, body
		parts := bytes.Split(record, []byte{0x1f})
		if len(parts) < 4 {
			continue
		}

		commit := Commit{
			Hash:        string(bytes.TrimSpace(parts[0])),
			Subject:     string(bytes.TrimSpace(parts[1])),
			Author:      string(bytes.TrimSpace(parts[2])),
			AuthorEmail: string(bytes.TrimSpace(parts[3])),
		}
		if len(parts) > 4 {
			commit.Body = string(bytes.TrimSpace(parts[4]))
		}

		if commit.Hash != "" {
			commits = append(commits, commit)
		}
	}

	return commits
}

func parseLines(data []byte) []string {
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// GetInitialCommit returns the SHA of the first commit in the repository
func GetInitialCommit() (string, error) {
	cmd := exec.Command("git", "rev-list", "--max-parents=0", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-list: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetLatestTag returns the most recent tag matching the given prefix, sorted by
// semver. Tag lookups run against dir so the caller's repository is read even
// when the process working directory points elsewhere; an empty dir falls back
// to the process working directory. Returns empty string if no matching tags
// found.
func GetLatestTag(dir, prefix string) (string, string, error) {
	// The historical contract validates candidates under the default (permissive)
	// grammar with the caller's prefix glob, so a hyphenated or foreign-cased tag
	// still reads as today. GetLatestTagSpec carries that behavior verbatim when
	// given a permissive default spec.
	spec := taggrammar.Default()
	spec.Prefix = prefix
	return GetLatestTagSpec(dir, spec)
}

// GetLatestTagSpec is GetLatestTag under a caller-supplied grammar. The prefix
// glob widens the candidate set to every tag leading with spec.Prefix; the tag
// predicate then narrows it to a version-parseable tag under spec, so a strict
// per-component prefix (StrictPrefix) accepts only that component's tags and can
// never read a sibling component's namespace. Returns empty string if no
// matching version tag is found.
func GetLatestTagSpec(dir string, spec taggrammar.Spec) (string, string, error) {
	// Get all tags matching the prefix glob, sorted by version descending.
	// --sort=-v:refname sorts by version in descending order.
	cmd := exec.Command("git", "tag", "-l", spec.Prefix+"*", "--sort=-v:refname")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git tag: %w", err)
	}

	tags := parseLines(output)

	// The prefix glob can still match non-version tags (for example a
	// vX.Y.Z-dryrun.N exercise tag or a "vnightly" alias). Skip anything that is
	// not a version tag under spec so it can never be read as the latest one.
	for _, tag := range tags {
		if !IsValidVersionTagSpec(spec, tag) {
			continue
		}

		// First valid tag is the latest (git sorted descending by version).
		cmd = exec.Command("git", "rev-list", "-n", "1", tag)
		cmd.Dir = dir
		output, err = cmd.Output()
		if err != nil {
			return tag, "", fmt.Errorf("git rev-list for tag: %w", err)
		}
		return tag, strings.TrimSpace(string(output)), nil
	}

	return "", "", nil
}

// ListTags returns every tag in the repository. It returns an empty slice when
// the repository has no tags.
func ListTags() ([]string, error) {
	cmd := exec.Command("git", "tag", "-l")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git tag -l: %w", err)
	}
	return parseLines(output), nil
}

// pushRetryAttempts is the number of times a rejected push is retried before the
// operation is declared failed. It is sized to survive a realistic concurrent
// wave (all components of a monorepo racing to write their own leaf into one
// shared manifest file on trunk) and is aligned with the Contents-API write path
// ceiling in internal/statewrite.
const pushRetryAttempts = 10

// defaultPushBackoff is the base delay between push retries when no backoff is
// set via WithBackoff. The effective wait grows exponentially per attempt and
// carries a random jitter so concurrent writers de-synchronize rather than
// colliding again in lockstep. See backoffForAttempt.
const defaultPushBackoff = 250 * time.Millisecond

// maxPushBackoff caps the exponential growth of the per-attempt backoff so a
// late retry never sleeps for an unbounded stretch.
const maxPushBackoff = 8 * time.Second

// pushOptions holds the tunable behaviour of the rebase-retry push helpers. Its
// zero value reproduces the historical behaviour: git runs in the process working
// directory and retries wait defaultPushBackoff apart with no re-apply hook.
type pushOptions struct {
	dir     string
	backoff time.Duration
	// reapply, when set, is invoked on a rejected push instead of a textual
	// "git pull --rebase". The loop first re-fetches trunk and hard-resets the
	// local branch to the upstream tip (dropping the local commit whose bytes
	// were derived from a now-stale trunk), then calls reapply, which re-derives
	// and re-writes only the owned state leaf onto the fresh trunk bytes and
	// re-commits. This converges an owned leaf against a concurrent sibling's
	// leaf that already landed on trunk, where a textual rebase conflicts on the
	// adjacent edit. A nil reapply preserves the historical textual-rebase
	// behaviour for callers (promote, hotfix, rollback) that do not need it.
	reapply func() error
}

// Option configures the rebase-retry push helpers. Options are additive: a call
// with no options behaves exactly as the original positional API did.
type Option func(*pushOptions)

// WithDir runs the git commands with cmd.Dir set to dir instead of the process
// working directory. An empty dir (the default) leaves the process working
// directory in effect. This lets a caller drive a repository other than the one
// the process was launched in.
func WithDir(dir string) Option {
	return func(o *pushOptions) { o.dir = dir }
}

// WithBackoff sets the base delay between push retries. A zero duration (the
// default) selects defaultPushBackoff. The effective wait grows exponentially
// from this base per attempt (capped at maxPushBackoff) plus a random jitter, so
// tests pass a tiny value to keep the retry loop fast.
func WithBackoff(d time.Duration) Option {
	return func(o *pushOptions) { o.backoff = d }
}

// WithReapply supplies a re-apply callback invoked on a rejected push in place of
// a textual "git pull --rebase". On a non-fast-forward reject the loop re-fetches
// trunk, hard-resets the local branch to the upstream tip, then calls fn, which
// must re-derive and re-write only the owned state leaf onto the fresh trunk
// bytes and re-commit it. This makes an owned leaf converge against a concurrent
// sibling leaf already on trunk without the textual conflict a rebase hits on the
// adjacent edit. With no WithReapply the loop keeps the historical textual-rebase
// behaviour, so callers that do not need re-apply (promote, hotfix, rollback) are
// unaffected.
func WithReapply(fn func() error) Option {
	return func(o *pushOptions) { o.reapply = fn }
}

// backoffForAttempt returns the delay before the retry following a zero-based
// attempt index: an exponential growth of base (base * 2^attempt) capped at
// maxPushBackoff, plus a random jitter of up to base so concurrent writers racing
// the same trunk de-synchronize instead of colliding again in lockstep.
func backoffForAttempt(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = defaultPushBackoff
	}
	d := base
	for i := 0; i < attempt && d < maxPushBackoff; i++ {
		d *= 2
	}
	if d > maxPushBackoff {
		d = maxPushBackoff
	}
	return d + time.Duration(rand.Int64N(int64(base)+1))
}

// RefetchAndReset re-fetches trunk and hard-resets the working tree to the
// upstream tracking tip, so the working manifest holds the fresh trunk bytes
// (including any concurrent sibling leaf). It is exported for a state writer that
// wants to re-derive its owned leaf onto fresh trunk before its first commit, so
// that write never rests on a stale checkout base rather than relying solely on a
// push rejection to surface the staleness. It is the same operation the push
// retry loop performs on a rejected push.
func RefetchAndReset(dir string) error { return refetchAndReset(dir) }

// refetchAndReset re-fetches trunk and hard-resets the working tree to the
// upstream tracking tip, dropping any local commit whose bytes were derived from
// a stale trunk. It is the pre-step of a re-apply retry: after it returns the
// working manifest holds the fresh trunk bytes (including any concurrent sibling
// leaf) onto which the owned leaf is re-derived.
func refetchAndReset(dir string) error {
	fetch := exec.Command("git", "fetch")
	fetch.Dir = dir
	if out, err := fetch.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch failed: %s: %w", string(out), err)
	}
	reset := exec.Command("git", "reset", "--hard", "@{u}")
	reset.Dir = dir
	if out, err := reset.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset --hard @{u} failed: %s: %w", string(out), err)
	}
	return nil
}

func newPushOptions(opts []Option) pushOptions {
	var o pushOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.backoff == 0 {
		o.backoff = defaultPushBackoff
	}
	return o
}

// CommitAndPushWithRetry stages filePath, commits it with message, and pushes
// to the current branch's upstream, retrying a rejected push up to
// pushRetryAttempts times behind a pull --rebase. A "nothing to commit" state is
// treated as success (no-op). This
// is the manifest state-write path shared by promote and hotfix finalize: an
// API-created commit on real GitHub goes through a different path, so this is the
// plain-git fallback used when committing locally.
//
// Optional behaviour is supplied through Options: WithDir runs the commands in a
// specific repository, and WithBackoff tunes the retry delay. With no options the
// call behaves identically to the original positional signature.
func CommitAndPushWithRetry(filePath, message string, opts ...Option) error {
	o := newPushOptions(opts)

	cmd := exec.Command("git", "add", filePath)
	cmd.Dir = o.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(out), err)
	}

	cmd = exec.Command("git", "commit", "-m", message)
	cmd.Dir = o.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit failed: %s: %w", string(out), err)
	}

	return pushWithRebaseRetry(o)
}

// PushWithRebaseRetry pushes the current branch to its upstream, retrying a
// rejected push up to pushRetryAttempts times (for example a non-fast-forward
// caused by a concurrent state writer landing on trunk between checkout and
// push). It is the push half of CommitAndPushWithRetry, exposed for callers that
// stage and commit through their own flow and only need the shared retry
// behaviour. By default a rejected push is retried behind a "git pull --rebase",
// aborting the rebase and returning the wrapped error on a conflict rather than
// leaving the repository mid-rebase. Passing WithReapply switches the retry to
// re-derive the owned state leaf against re-fetched trunk instead, so an owned
// leaf converges against a concurrent sibling's adjacent leaf without conflict.
func PushWithRebaseRetry(opts ...Option) error {
	return pushWithRebaseRetry(newPushOptions(opts))
}

// pushWithRebaseRetry is the single rebase-retry loop shared by
// CommitAndPushWithRetry and PushWithRebaseRetry. Keeping one implementation
// means the re-apply and rebase-abort-on-conflict behaviours live in exactly one
// place. When o.reapply is set it re-derives the owned leaf against re-fetched
// trunk on each rejected push; otherwise it falls back to a textual rebase.
//
// Every attempt emits a stable "cascade-state-write:" log line carrying the
// attempt count, so a live run can be grepped to prove a concurrent wave
// converged (and, on failure, that it exhausted the ceiling rather than erroring
// for another reason).
func pushWithRebaseRetry(o pushOptions) error {
	var lastOut []byte
	for i := 0; i < pushRetryAttempts; i++ {
		log.Info("cascade-state-write: attempt=%d/%d", i+1, pushRetryAttempts)

		cmd := exec.Command("git", "push")
		cmd.Dir = o.dir
		out, err := cmd.CombinedOutput()
		if err == nil {
			log.Info("cascade-state-write: ok attempt=%d", i+1)
			return nil
		}
		lastOut = out

		// Only a stale-ref rejection can be fixed by rebasing onto the advanced
		// upstream and pushing again. Anything else (a GH013 workflow-scope
		// refusal, branch protection, auth, a missing remote) fails identically
		// on every retry, so surface the real output immediately instead of
		// hiding it behind no-op rebases and a generic exhaustion summary.
		if !retryablePushRejection(out) {
			return fmt.Errorf("git push failed: %s: %w", strings.TrimSpace(string(out)), err)
		}

		if i == pushRetryAttempts-1 {
			break
		}

		if o.reapply != nil {
			// Re-derive the owned leaf onto re-fetched trunk bytes instead of a
			// textual rebase, which conflicts when a concurrent sibling wrote an
			// adjacent leaf into the same shared manifest file.
			if err := refetchAndReset(o.dir); err != nil {
				return err
			}
			if err := o.reapply(); err != nil {
				return fmt.Errorf("state re-apply on push retry failed: %w", err)
			}
		} else {
			cmd = exec.Command("git", "pull", "--rebase")
			cmd.Dir = o.dir
			if out, err := cmd.CombinedOutput(); err != nil {
				// A failed rebase (typically a conflict) leaves the repository
				// mid-rebase. Abort it so we neither leave a conflicted state
				// behind nor loop into a guaranteed-failing push, and surface the
				// real error instead of the generic "push failed" summary.
				abort := exec.Command("git", "rebase", "--abort")
				abort.Dir = o.dir
				_, _ = abort.CombinedOutput() // best effort; nothing to abort is fine
				return fmt.Errorf("git pull --rebase failed: %s: %w", string(out), err)
			}
		}
		time.Sleep(backoffForAttempt(o.backoff, i))
	}

	log.Info("cascade-state-write: exhausted attempts=%d", pushRetryAttempts)
	return fmt.Errorf("git push failed after %d retries: %s", pushRetryAttempts, strings.TrimSpace(string(lastOut)))
}

// retryablePushRejection reports whether a failed push is the optimistic kind
// the retry loop exists for: the remote ref advanced under us (a client-side
// "! [rejected] ... (fetch first)" / non-fast-forward) or a concurrent writer
// held the ref lock. A "! [remote rejected]" (pre-receive hook, branch
// protection, GH013 scope refusal), an auth failure, or a transport error
// cannot be fixed by rebase-and-retry, so those are not retryable.
func retryablePushRejection(out []byte) bool {
	s := string(out)
	for _, marker := range []string{
		"non-fast-forward",
		"fetch first",
		"cannot lock ref",
		"! [rejected]",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// CurrentBranch returns the name of the currently checked-out branch.
// It returns an error if the HEAD is detached or the command fails.
func CurrentBranch() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD: %w", err)
	}
	branch := strings.TrimSpace(string(output))
	if branch == "HEAD" {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD: HEAD is detached")
	}
	return branch, nil
}

// CommitAndPush stages a file, commits with the given message, and pushes to origin.
func CommitAndPush(filePath, message string) error {
	// Stage the file
	cmd := exec.Command("git", "add", filePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w\n%s", err, output)
	}

	// Commit
	cmd = exec.Command("git", "commit", "-m", message)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w\n%s", err, output)
	}

	// Push
	cmd = exec.Command("git", "push")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %w\n%s", err, output)
	}

	return nil
}

// IsAncestor reports whether ancestor is an ancestor of descendant by running
// "git merge-base --is-ancestor". An exit code of 0 means true, an exit code of 1
// means false, and any other exit code or execution failure is returned as an error.
//
// Both commits must be present in the local object store. In a shallow clone the
// relevant history may be missing, so callers that rely on this must ensure full
// history is fetched (for example fetch-depth: 0).
func IsAncestor(ancestor, descendant string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 1 {
			return false, nil
		}
	}

	return false, fmt.Errorf("git merge-base --is-ancestor: %w", err)
}

// BranchExists reports whether the remote-tracking ref refs/remotes/<remote>/<name>
// exists by running "git rev-parse --verify". An exit code of 0 means the ref
// exists, a non-zero exit code means it does not, and an execution failure is
// returned as an error.
//
// This checks remote-tracking refs, so the remote must have been fetched first.
// A shallow or partial fetch that omits the branch will cause this to report false.
func BranchExists(remote, name string) (bool, error) {
	ref := fmt.Sprintf("refs/remotes/%s/%s", remote, name)
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}

	return false, fmt.Errorf("git rev-parse --verify %s: %w", ref, err)
}

// RemoteBranchSHA returns the SHA of the remote-tracking ref
// refs/remotes/<remote>/<name> by running "git rev-parse". The returned SHA is
// whitespace-trimmed. An error is returned if the ref cannot be resolved.
//
// This resolves a remote-tracking ref, so the remote must have been fetched first.
// A shallow or partial fetch that omits the branch will cause this to fail.
func RemoteBranchSHA(remote, name string) (string, error) {
	ref := fmt.Sprintf("refs/remotes/%s/%s", remote, name)
	cmd := exec.Command("git", "rev-parse", ref)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// ListRemoteBranches returns the branch names known for the given remote via the
// remote-tracking refs refs/remotes/<remote>/*. The remote prefix and the symbolic
// HEAD pointer are stripped, so "refs/remotes/origin/env/test" is returned as
// "env/test". The remote must have been fetched first; a shallow or partial fetch
// that omits branches will leave them out of the result.
func ListRemoteBranches(remote string) ([]string, error) {
	prefix := fmt.Sprintf("refs/remotes/%s/", remote)
	cmd := exec.Command("git", "for-each-ref", "--format=%(refname)", prefix)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref %s: %w", prefix, err)
	}

	var branches []string
	for _, ref := range parseLines(output) {
		name := strings.TrimPrefix(ref, prefix)
		if name == "" || name == "HEAD" {
			continue
		}
		branches = append(branches, name)
	}
	return branches, nil
}

// DeleteRemoteBranch deletes the named branch on the given remote by running
// "git push <remote> --delete <name>". Deleting a branch that does not exist on
// the remote is treated as success so the operation is idempotent: re-running a
// rejoin cleanup after a partial failure does not error on an already-deleted
// branch.
func DeleteRemoteBranch(remote, name string) error {
	cmd := exec.Command("git", "push", remote, "--delete", name)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if remoteRefAlreadyGone(out) {
		return nil
	}
	return fmt.Errorf("git push %s --delete %s: %w\n%s", remote, name, err, out)
}

// DeleteRemoteTag deletes the named tag on the given remote by running
// "git push <remote> --delete refs/tags/<name>". Deleting a tag that does not
// exist on the remote is treated as success so the operation is idempotent.
func DeleteRemoteTag(remote, name string) error {
	cmd := exec.Command("git", "push", remote, "--delete", "refs/tags/"+name)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if remoteRefAlreadyGone(out) {
		return nil
	}
	return fmt.Errorf("git push %s --delete refs/tags/%s: %w\n%s", remote, name, err, out)
}

// remoteRefAlreadyGone reports whether a failed delete-push is because the ref
// does not exist on the remote, which we treat as success. Git emits "remote ref
// does not exist" (newer) or "unable to delete ... remote ref does not exist".
func remoteRefAlreadyGone(out []byte) bool {
	return strings.Contains(string(out), "remote ref does not exist")
}

// GetLatestReleaseTag returns the most recent non-prerelease tag (no -rc suffix).
// This is used to find the base version for calculating next release versions.
// Tag lookups run against dir so the caller's repository is read even when the
// process working directory points elsewhere; an empty dir falls back to the
// process working directory.
func GetLatestReleaseTag(dir, prefix string) (string, string, error) {
	spec := taggrammar.Default()
	spec.Prefix = prefix
	return GetLatestReleaseTagSpec(dir, spec)
}

// GetLatestReleaseTagSpec is GetLatestReleaseTag under a caller-supplied grammar.
// The prefix glob widens the candidate set to every tag that leads with the
// grammar's prefix; the release predicate then narrows it to a published
// release, so a custom pre-release token is classified correctly rather than
// slipping through a hard-wired "-rc." check.
func GetLatestReleaseTagSpec(dir string, spec taggrammar.Spec) (string, string, error) {
	cmd := exec.Command("git", "tag", "-l", spec.Prefix+"*", "--sort=-v:refname")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git tag: %w", err)
	}

	tags := parseLines(output)
	if len(tags) == 0 {
		return "", "", nil
	}

	// Find the first published release: a tag that parses as a version under the
	// grammar and carries no pre-release segment. Parsing through the grammar
	// keeps non-version tags (such as a vX.Y.Z-dryrun.N exercise tag) out, and
	// the no-pre-release check keeps pre-releases (rc, beta, or any custom token)
	// from being mistaken for a release.
	for _, tag := range tags {
		parsed, ok := spec.Parse(tag)
		if !ok {
			continue
		}
		if parsed.PreRelease < 0 {
			// Get the SHA for this tag
			cmd = exec.Command("git", "rev-list", "-n", "1", tag)
			cmd.Dir = dir
			output, err = cmd.Output()
			if err != nil {
				return tag, "", fmt.Errorf("git rev-list for tag: %w", err)
			}
			return tag, strings.TrimSpace(string(output)), nil
		}
	}

	return "", "", nil
}
