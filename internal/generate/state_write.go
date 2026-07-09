package generate

import (
	"fmt"
	"strings"
)

// stateWriteParams describes a single manifest state-write step. Both the
// orchestrate finalize ("Update Manifest") and release finalize ("Update Latest
// Release State") steps apply yq edits to the manifest and then need to persist
// that change to the trunk branch. The persistence path differs by environment.
type stateWriteParams struct {
	// applyFn is the name of the already-defined shell function that re-applies
	// the yq edits to $MANIFEST_FILE (for example "apply_state_edits"). It is
	// invoked once per attempt so a rebase onto the latest tip can re-layer the
	// edits without conflicting on the same yaml line.
	applyFn string
	// commitMessage is the commit message body for the state commit. It may
	// contain shell variable references (for example "$ENVIRONMENT") that are
	// already exported in the step env, and may span multiple lines.
	commitMessage string
	// noChangeLabel is printed when the manifest has no pending changes.
	noChangeLabel string
	// successLabel is printed after a successful write.
	//
	// The token used for the REST API write is not part of this struct: it is
	// supplied to the gh CLI via the step's GH_TOKEN env (see getStateTokenRef).
	// A token able to bypass branch protection lets the write land on a protected
	// trunk and produces a verified, signed commit.
	successLabel string
	// authorName and authorEmail are the identity stamped as both the author and
	// the committer of the Contents API state commit. Without them the API
	// attributes the commit to the token owner; callers populate them from the
	// manifest git config (GetGitUserName/GetGitUserEmail) so the commit is the
	// automation bot. Empty fields fall back to the github-actions[bot] default.
	authorName  string
	authorEmail string
}

// defaultStateAuthorName and defaultStateAuthorEmail are the identity stamped on
// a Contents API state commit when a caller supplies no override. They match the
// git-config identity the act/gitea git-commit path uses, so both write paths
// attribute the commit to the automation bot rather than the token owner.
const (
	defaultStateAuthorName  = "github-actions[bot]"
	defaultStateAuthorEmail = "github-actions[bot]@users.noreply.github.com"
)

// writeStateCommitPush emits the dual-path state-write logic into an already-open
// "run: |" block at the given indent. The caller must have already set the
// MANIFEST_FILE and BRANCH shell variables and defined the apply function named
// by params.applyFn.
//
// On real GitHub the write goes through the Contents REST API: API-created
// commits are signed by GitHub (shown as Verified) and, when made with a
// bypass-capable token, update the trunk even when a required status check
// protects the branch. In the act/gitea e2e environment (detected exactly as
// the dispatch steps do, by GITHUB_SERVER_URL != https://github.com) there is no
// GitHub API, so the existing fetch/reset/reapply/commit/push retry loop runs
// unchanged. gitea enforces neither branch protection nor commit signatures, so
// that path keeps working as before.
func writeStateCommitPush(sb *strings.Builder, indent string, params stateWriteParams) {
	// w writes a single indented line. When no format args are supplied the
	// string is written verbatim so literal '%' (for example in
	// "$((RANDOM % 5 + 2))") is never interpreted as a format verb.
	w := func(format string, args ...any) {
		if len(args) == 0 {
			sb.WriteString(indent + format + "\n")
			return
		}
		fmt.Fprintf(sb, indent+format+"\n", args...)
	}

	// Resolve the commit identity so an absent override never leaves the API write
	// to default to the token owner. Callers pass the manifest git config identity;
	// an empty field falls back to the automation bot.
	authorName := params.authorName
	if authorName == "" {
		authorName = defaultStateAuthorName
	}
	authorEmail := params.authorEmail
	if authorEmail == "" {
		authorEmail = defaultStateAuthorEmail
	}

	w("if [[ \"$GITHUB_SERVER_URL\" != \"https://github.com\" ]]; then")
	// gitea/act path: keep the existing git fetch/reset/reapply/commit/push loop.
	w("  # act/gitea e2e: no GitHub API, and the trunk is neither protected nor")
	w("  # signature-checked, so push the state commit directly with retries.")
	w("  for attempt in 1 2 3 4 5 6 7 8 9 10; do")
	w("    echo \"cascade-state-write: attempt=$attempt/10\"")
	w("    git fetch origin \"$BRANCH\"")
	w("    git reset --hard \"origin/$BRANCH\"")
	w("    %s", params.applyFn)
	w("    if git diff --quiet \"$MANIFEST_FILE\"; then")
	w("      echo \"%s\"", params.noChangeLabel)
	w("      exit 0")
	w("    fi")
	w("    git add \"$MANIFEST_FILE\"")
	writeShellCommit(sb, indent+"    ", params.commitMessage)
	w("    if git push origin \"HEAD:$BRANCH\"; then")
	w("      echo \"%s on attempt $attempt\"", params.successLabel)
	w("      echo \"cascade-state-write: ok attempt=$attempt\"")
	w("      exit 0")
	w("    fi")
	w("    echo \"Push attempt $attempt rejected (likely concurrent run); retrying...\" >&2")
	writeStateBackoffSleep(sb, indent+"    ")
	w("  done")
	w("  echo \"cascade-state-write: exhausted attempts=10\" >&2")
	w("  echo \"::error::Failed to push state after 10 attempts\" >&2")
	w("  exit 1")
	w("fi")
	w("")
	// Real GitHub path: write through the Contents REST API so the commit is
	// signed by GitHub and can bypass branch protection with a capable token.
	w("# Real GitHub: write state through the Contents REST API. API commits are")
	w("# signed by GitHub (Verified) and, with a bypass-capable token, update the")
	w("# trunk even when a required status check protects it.")
	w("for attempt in 1 2 3 4 5 6 7 8 9 10; do")
	w("  echo \"cascade-state-write: attempt=$attempt/10\"")
	w("  git fetch origin \"$BRANCH\"")
	w("  git reset --hard \"origin/$BRANCH\"")
	w("  %s", params.applyFn)
	w("  if git diff --quiet \"$MANIFEST_FILE\"; then")
	w("    echo \"%s\"", params.noChangeLabel)
	w("    exit 0")
	w("  fi")
	w("  CONTENT_B64=$(base64 -w0 \"$MANIFEST_FILE\" 2>/dev/null || base64 \"$MANIFEST_FILE\" | tr -d '\\n')")
	w("  CURRENT_SHA=$(gh api \"repos/${{ github.repository }}/contents/$MANIFEST_FILE?ref=$BRANCH\" --jq '.sha' 2>/dev/null || true)")
	// Build the API arguments. -f message handles a multi-line message safely.
	w("  API_ARGS=(\"repos/${{ github.repository }}/contents/$MANIFEST_FILE\" -X PUT")
	w("    -f \"message=%s\"", shellSingleLineMessage(params.commitMessage))
	w("    -f \"content=$CONTENT_B64\"")
	w("    -f \"branch=$BRANCH\"")
	// Stamp author and committer so the API attributes the commit to the bot
	// identity rather than the token owner GitHub would otherwise use.
	w("    -f \"author[name]=%s\"", authorName)
	w("    -f \"author[email]=%s\"", authorEmail)
	w("    -f \"committer[name]=%s\"", authorName)
	w("    -f \"committer[email]=%s\")", authorEmail)
	w("  if [[ -n \"$CURRENT_SHA\" ]]; then")
	w("    API_ARGS+=(-f \"sha=$CURRENT_SHA\")")
	w("  fi")
	w("  if gh api \"${API_ARGS[@]}\" >/dev/null; then")
	w("    echo \"%s via API on attempt $attempt\"", params.successLabel)
	w("    echo \"cascade-state-write: ok attempt=$attempt\"")
	w("    exit 0")
	w("  fi")
	w("  echo \"State write attempt $attempt failed (likely concurrent run); retrying...\" >&2")
	writeStateBackoffSleep(sb, indent+"  ")
	w("done")
	w("echo \"cascade-state-write: exhausted attempts=10\" >&2")
	w("echo \"::error::Failed to write state via API after 10 attempts\" >&2")
	w("exit 1")
}

// writeStateBackoffSleep emits an exponential-with-jitter backoff sleep for the
// state-write retry loop at the given indent. The wait doubles per attempt
// (1s, 2s, 4s, 8s) capped at eight seconds, plus a random zero-or-one-second
// jitter, so concurrent writers racing one trunk branch de-synchronize rather
// than colliding again in lockstep. It mirrors the exponential jittered backoff
// the git and Contents-API Go write paths use. The loop variable is "$attempt".
func writeStateBackoffSleep(sb *strings.Builder, indent string) {
	// These lines contain literal '%' (RANDOM % 2); write them verbatim so the
	// caller's fmt-based line writer never treats '%' as a format verb.
	sb.WriteString(indent + "backoff=$(( 1 << (attempt - 1) ))\n")
	sb.WriteString(indent + "if [ \"$backoff\" -gt 8 ]; then backoff=8; fi\n")
	sb.WriteString(indent + "sleep $(( backoff + RANDOM % 2 ))\n")
}

// writeShellCommit emits a `git commit -m` line for a possibly multi-line
// message at the given indent. Multi-line messages preserve the existing
// behavior of embedding a trailer line under the subject.
func writeShellCommit(sb *strings.Builder, indent, message string) {
	lines := strings.Split(message, "\n")
	if len(lines) == 1 {
		fmt.Fprintf(sb, "%sgit commit -m \"%s\"\n", indent, lines[0])
		return
	}
	fmt.Fprintf(sb, "%sgit commit -m \"%s\n", indent, lines[0])
	for i := 1; i < len(lines); i++ {
		if i == len(lines)-1 {
			fmt.Fprintf(sb, "%s%s\"\n", indent, lines[i])
		} else {
			fmt.Fprintf(sb, "%s%s\n", indent, lines[i])
		}
	}
}

// shellSingleLineMessage collapses a possibly multi-line commit message into a
// single line for the REST API -f message argument, joining lines with a space.
// The Contents API takes the full message as one field value; a single line
// keeps the emitted shell readable while preserving the subject and any trailer.
func shellSingleLineMessage(message string) string {
	parts := strings.Split(message, "\n")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}
