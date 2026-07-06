#!/usr/bin/env bash
#
# release-idempotency.sh: decide whether the Release job can skip publishing
# because a fully published GitHub release already exists for the tag.
#
# release.yaml is triggered redundantly on purpose. A candidate tag is both
# pushed (a real push event) and dispatched explicitly by orchestrate, promote,
# and auto-promote, because API-created and CI-skipped tags do not reliably emit
# the push or release events (#86). The explicit dispatch guarantees the build
# runs; the cost is that a pushed candidate tag can trigger Release twice at the
# same instant. Without a guard the second run reaches GoReleaser and dies on
# `422 already_exists`, leaving a red Release run and a stray draft. This script
# lets a duplicate run detect the winner's published release and exit cleanly
# instead.
#
# A release counts as already published only when it is NOT a draft AND carries
# the checksums.txt manifest that GoReleaser uploads once it has finished writing
# the archive set. Keying on that named asset (rather than any asset, or on the
# tag alone) avoids skipping while the winning run is still mid-upload: a bare or
# asset-less release makes this print "false" so the job proceeds, and the 422
# tolerance belt in release.yaml absorbs the rare case where two runs publish at
# once.
#
# Usage:
#   release-idempotency.sh <tag>   # prints "true" (skip) or "false" (build)
#
# The release JSON is read from `gh release view <tag> --json isDraft,assets`.
# For hermetic testing, define CASCADE_RELEASE_JSON to inject that JSON; an empty
# value means the release is absent. Exit status is 0 on a well-formed run (2 on
# a usage error); the decision is the printed word.

set -euo pipefail

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "usage: $(basename "$0") <tag>" >&2
  exit 2
fi

tag="$1"

# A defined CASCADE_RELEASE_JSON (even empty) overrides gh, so a test can inject
# an absent release. Only a wholly unset variable falls through to `gh`.
if [ -n "${CASCADE_RELEASE_JSON+set}" ]; then
  data="$CASCADE_RELEASE_JSON"
else
  # A missing release makes `gh release view` exit non-zero; that is the
  # not-yet-published case, so swallow the error and treat the release as absent.
  if ! data="$(gh release view "$tag" --json isDraft,assets 2>/dev/null)"; then
    data=""
  fi
fi

# Absent release means nothing has published this tag yet, so the job must build.
if [ -z "$data" ]; then
  echo "false"
  exit 0
fi

# Published only when the release is live (not a draft) and the checksum manifest
# is attached, which GoReleaser writes after the archive set is complete.
printf '%s' "$data" | jq -r '
  if (.isDraft == false)
     and ((.assets // []) | any(.name == "checksums.txt"))
  then "true" else "false" end
'
