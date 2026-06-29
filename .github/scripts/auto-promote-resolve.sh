#!/usr/bin/env bash
# Decide whether a completed Fleet E2E run promotes a final vX.Y.Z release, and
# if so, which version. This is the decision core of the auto-promote workflow,
# extracted so it can be exercised in isolation with mock inputs.
#
# Gates, in order:
#   1. conclusion gate  - only a success fleet conclusion proceeds.
#   2. full_run gate    - a selective run (repos=subset) never promotes.
#   3. rc-only gate     - only a vX.Y.Z-rc.N version promotes (not -dryrun.N).
#   4. suffix strip      - the final version is the rc with -rc.N removed.
#
# Inputs (environment):
#   CONCLUSION        - workflow_run.conclusion (default: success).
#   WR_HEAD_BRANCH    - workflow_run.head_branch (fallback rc source).
#   WR_HEAD_SHA       - workflow_run.head_sha (fallback rc source).
#   GITHUB_REPOSITORY - owner/repo, for the head_sha -> tag fallback.
#   GH_TOKEN          - token for the head_sha -> tag fallback (gh api).
#   GITHUB_OUTPUT     - file the resolved outputs are appended to.
#
# Inputs (working directory, both optional):
#   full-run.txt           - "true" for a full (all repos) fleet run.
#   version-under-test.txt - the exact version the fleet pinned every suite to.
#
# Outputs (appended to $GITHUB_OUTPUT):
#   promote      - "true" only when all gates pass, else "false".
#   rc_version   - the resolved vX.Y.Z-rc.N (only when promote=true).
#   base_version - the final vX.Y.Z to publish (only when promote=true).

set -euo pipefail

CONCLUSION="${CONCLUSION:-success}"
WR_HEAD_BRANCH="${WR_HEAD_BRANCH:-}"
WR_HEAD_SHA="${WR_HEAD_SHA:-}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:-}"

# Gate 1: only a green fleet promotes. The calling job already guards on this at
# the job level; re-asserting it here keeps the decision self-contained and lets
# a non-success conclusion short-circuit to a clean no-op.
if [ "$CONCLUSION" != "success" ]; then
  echo "::notice::Fleet conclusion was '$CONCLUSION', not success; nothing to promote."
  echo "promote=false" >> "$GITHUB_OUTPUT"
  exit 0
fi

# Gate 2: read the full_run marker (true only for repos=all/default). Selective
# fleet runs (any subset) must never promote, even if they pass, because only
# full validation is a safe release signal. This gate prevents accidental
# promotion from a maintainer's debug run (e.g. repos=4env).
FULL_RUN="false"
if [ -f full-run.txt ]; then
  FULL_RUN=$(tr -d '[:space:]' < full-run.txt)
  echo "::notice::Read full-run marker: '$FULL_RUN'"
else
  echo "::notice::No full-run marker; assuming pre-selector artifact (full run)."
  FULL_RUN="true"
fi

if [ "$FULL_RUN" != "true" ]; then
  echo "::notice::Fleet run was selective (repos=subset), not a full validation. Skipping promotion."
  echo "promote=false" >> "$GITHUB_OUTPUT"
  exit 0
fi

# Primary: the version-under-test artifact carries the exact resolved version
# the fleet pinned every suite to. Authoritative when present.
RC=""
if [ -f version-under-test.txt ]; then
  RC=$(tr -d '[:space:]' < version-under-test.txt)
  echo "::notice::Read version-under-test artifact: '${RC:-<empty>}'"
fi

# Fallback: no artifact (a tag-push-triggered fleet predating this handoff).
# Resolve the rc tag the fleet validated the prior way.
if [ -z "$RC" ]; then
  echo "::notice::No version-under-test artifact; falling back to head_branch / head_sha."
  if printf '%s' "$WR_HEAD_BRANCH" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'; then
    RC="$WR_HEAD_BRANCH"
  elif [ -n "$WR_HEAD_SHA" ]; then
    # Pick the highest rc tag on the head_sha so selection is deterministic
    # regardless of API ordering.
    RC=$(gh api "repos/${GITHUB_REPOSITORY}/tags" \
      --jq ".[] | select(.commit.sha == \"$WR_HEAD_SHA\") | .name" \
      | grep -- '-rc\.' | sort -V -r | head -n 1 || true)
  else
    RC=""
  fi
fi

# Gate 3: only an rc tag of shape vX.Y.Z-rc.N promotes. Anything else (a non-rc
# fleet dispatch, a -dryrun.N tag, an empty resolve) is a clean no-op.
if ! printf '%s' "$RC" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'; then
  echo "::notice::Fleet run was not for a vX.Y.Z-rc.N tag (got '${RC:-<empty>}'); nothing to promote."
  echo "promote=false" >> "$GITHUB_OUTPUT"
  exit 0
fi

# Gate 4: strip the -rc.N suffix to get the final release version.
BASE="${RC%-rc.*}"

{
  echo "promote=true"
  echo "rc_version=$RC"
  echo "base_version=$BASE"
} >> "$GITHUB_OUTPUT"
echo "::notice::Green full fleet for $RC -> promoting final $BASE"
