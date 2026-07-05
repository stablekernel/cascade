#!/usr/bin/env bash
#
# previous-stable-tag.sh: print the previous final (non-prerelease) release tag
# strictly below a given tag, or an empty string when none exists.
#
# Auto-promote tags the newest candidate's exact commit as the final vX.Y.Z, so
# a final tag and its last candidate (for example v0.8.0 and v0.8.0-rc.7) point
# at the same commit. GoReleaser, told only GORELEASER_CURRENT_TAG, auto-detects
# the previous tag as the immediately preceding semver tag, which is that same
# candidate. The GitHub compare range is then empty and the final release ships
# with an empty changelog. Feeding GoReleaser the previous *stable* tag as
# GORELEASER_PREVIOUS_TAG makes the final changelog span the whole candidate
# cycle (for example v0.7.0..v0.8.0) instead.
#
# This computes that previous stable tag. It keeps only final tags shaped
# vMAJOR.MINOR.PATCH (no -rc. / -dryrun. / other prerelease suffix), keeps those
# strictly less than the current tag by semver, and prints the greatest. Any
# prerelease suffix on the current tag is stripped first, so a prerelease current
# tag still resolves the stable release below it (the workflow only uses the
# value for final builds, but the behaviour is well defined either way).
#
# Tags come from `git tag` in the working directory, so the caller must have
# fetched tags first (release.yaml checks out with fetch-depth: 0). For hermetic
# testing, set CASCADE_TAG_LIST to a newline-separated tag list to bypass git.
#
# Usage:
#   previous-stable-tag.sh <current-tag>
#
# Exit status:
#   0  success (prints the previous stable tag, or empty when none exists)
#   2  usage / argument error

set -euo pipefail

usage() {
  echo "usage: $(basename "$0") <current-tag>" >&2
}

if [ "$#" -ne 1 ]; then
  usage
  exit 2
fi

current="$1"

if [ -z "$current" ]; then
  usage
  exit 2
fi

# Strip any prerelease suffix so the comparison target is a pure final version.
# For a final current tag this is a no-op; for vX.Y.Z-rc.N it yields vX.Y.Z, and
# the stable release below that base is what we want.
current_base="${current%%-*}"

# A defined CASCADE_TAG_LIST (even empty) overrides git, so a test can inject an
# empty tag set. Only a wholly unset variable falls through to `git tag`.
if [ -n "${CASCADE_TAG_LIST+set}" ]; then
  tags="$CASCADE_TAG_LIST"
else
  tags="$(git tag)"
fi

# Keep only final tags. A `|| true` absorbs grep's exit 1 on no match so an empty
# tag set does not trip `set -o pipefail`.
finals="$(printf '%s\n' "$tags" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true)"

# No final tags at all means there is no previous stable release to point at.
if [ -z "$finals" ]; then
  printf '\n'
  exit 0
fi

# Append the current base as a sentinel, sort every final tag plus the sentinel
# by semver (`-u` collapses the sentinel into a final current tag that is already
# present, so it never appears twice), and print the tag immediately below the
# sentinel. That is the greatest final tag strictly less than the current base.
# When nothing sorts below the sentinel (no prior stable), prev is empty and an
# empty line prints.
printf '%s\n%s\n' "$finals" "$current_base" \
  | sort -V -u \
  | awk -v cur="$current_base" '$0 == cur { print prev; exit } { prev = $0 }'
