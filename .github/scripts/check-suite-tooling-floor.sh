#!/usr/bin/env bash
# check-suite-tooling-floor.sh - fail when an example repo's scenario suite
# pins the cascade tooling below the feature floor.
#
# Each cascade-example repo runs its own scenario-suite.yaml, which bootstraps
# a cascade CLI through the setup-cli action to drive its scenarios. That
# bootstrap version is pinned by hand in the suite. Nothing keeps it moving
# forward, so a suite can sit on a release that predates a command the suite
# now invokes, producing an "unknown command" failure deep inside a live fleet
# run. This check compares every suite's pin against the floor and fails fast
# when one has drifted below it, so the drift is caught before a fleet run.
#
# The floor is the latest published stable cascade release: the newest version
# every released command is guaranteed to exist in. A suite pinned at or above
# the floor passes; only a strictly-lower pin fails. A suite that tracks a
# moving ref (for example @main) carries no semver pin and is treated as
# current, so it is never flagged.
#
# Usage:
#   check-suite-tooling-floor.sh                    # floor = latest release
#   FLOOR=v0.7.0 check-suite-tooling-floor.sh       # explicit floor override
#   REPOS="4env 3env" check-suite-tooling-floor.sh  # subset of the roster
#
# Environment:
#   FLOOR        Override the floor version (vX.Y.Z). Empty resolves to the
#                latest stable release of the cascade repo.
#   REPOS        Space-separated example-repo short names to check. Empty uses
#                the full roster below.
#   FLEET_OWNER  GitHub owner of the cascade and example repos (default
#                stablekernel).
#
# Requires: gh (authenticated), base64, sort -V.
set -euo pipefail

OWNER="${FLEET_OWNER:-stablekernel}"

# Canonical floor-check roster. This mirrors the fleet-e2e repin roster: every
# example repo whose suite installs a pinned cascade CLI belongs here. Keep it
# in sync with the roster in .github/workflows/fleet-e2e.yaml when a repo is
# added or removed.
DEFAULT_REPOS="primary artifact-a artifact-b 4env 3env 2env single-env release-only no-env callbacks rollback-dispatch"

SUITE_PATH=".github/workflows/scenario-suite.yaml"

# ver_lt A B: succeed when semver A is strictly lower than semver B.
ver_lt() {
  [ "$1" != "$2" ] && \
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n 1)" = "$1" ]
}

# resolve_floor: echo the newest non-prerelease, non-draft release tag.
resolve_floor() {
  gh release list --repo "${OWNER}/cascade" -L 50 \
    --json tagName,isPrerelease,isDraft \
    --jq '.[] | select(.isPrerelease == false and .isDraft == false) | .tagName' \
    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1
}

floor="${FLOOR:-}"
if [ -z "$floor" ]; then
  floor="$(resolve_floor || true)"
fi
if ! printf '%s' "$floor" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "::error::could not resolve a stable floor version (got '${floor:-<empty>}')"
  exit 1
fi
echo "Feature floor (latest stable cascade release): ${floor}"
echo ""

read -ra repos <<< "${REPOS:-$DEFAULT_REPOS}"

stale=""
skipped=""
for name in "${repos[@]}"; do
  slug="${OWNER}/cascade-example-${name}"
  content="$(gh api "repos/${slug}/contents/${SUITE_PATH}" --jq '.content' 2>/dev/null \
    | base64 -d 2>/dev/null || true)"
  if [ -z "$content" ]; then
    skipped="${skipped} ${name}(no-suite)"
    continue
  fi

  # Extract every semver pin from the suite's setup-cli action ref and its
  # version input. A moving ref (for example setup-cli@main) yields no match
  # and is skipped rather than flagged.
  pins="$(printf '%s' "$content" \
    | grep -oE 'setup-cli@v[0-9]+\.[0-9]+\.[0-9]+|version:[[:space:]]*v[0-9]+\.[0-9]+\.[0-9]+' \
    | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | sort -u || true)"
  if [ -z "$pins" ]; then
    echo "  ${name}: no semver tooling pin (tracks a moving ref); skipped"
    skipped="${skipped} ${name}(moving-ref)"
    continue
  fi

  while IFS= read -r pin; do
    [ -n "$pin" ] || continue
    if ver_lt "$pin" "$floor"; then
      echo "  ${name}: ${pin} is BELOW floor ${floor}"
      stale="${stale} ${name}:${pin}"
    else
      echo "  ${name}: ${pin} >= floor ${floor}"
    fi
  done <<< "$pins"
done

echo ""
if [ -n "$skipped" ]; then
  echo "Not pin-checked:${skipped}"
fi

if [ -n "$stale" ]; then
  echo "::error::example-suite tooling pins below floor ${floor}:${stale}"
  echo "Bump each listed repo's ${SUITE_PATH} setup-cli pin (both the action"
  echo "ref and the version input) to at least ${floor}, then rerun this check."
  exit 1
fi

echo "All example-repo suite tooling pins are at or above floor ${floor}."
