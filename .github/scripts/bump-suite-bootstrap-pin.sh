#!/usr/bin/env bash
#
# bump-suite-bootstrap-pin.sh: rewrite a cascade-example repo's scenario suite
# so its committed bootstrap pin points at a target cascade release.
#
# Every cascade-example repo runs its own .github/workflows/scenario-suite.yaml,
# which bootstraps a cascade CLI through the setup-cli action. That bootstrap is
# pinned by hand to a fixed release tag in two places:
#
#   - the action ref:    uses: stablekernel/cascade/.github/actions/setup-cli@vX.Y.Z
#   - the version input:  version: vX.Y.Z
#
# When a new cascade release publishes, both pins want to move to it so the
# suites bootstrap the current CLI instead of drifting onto an older tag. This
# rewrites both, in place, to the target version. It is idempotent: a suite
# already on the target, or one that tracks a moving ref (for example
# setup-cli@main) and carries no semver pin, is left byte-for-byte unchanged.
# Callers detect "did anything change" from git status, so the script itself
# only reports argument or IO failure through its exit code.
#
# The two rewritten shapes mirror exactly what check-suite-tooling-floor.sh reads
# back, so a bump here clears the floor check for the same repo.
#
# Usage:
#   bump-suite-bootstrap-pin.sh <suite-file> <target-version>
#
# Arguments:
#   suite-file      Path to the scenario-suite.yaml to rewrite.
#   target-version  Release tag to pin (vX.Y.Z, no prerelease suffix).
#
# Exit status:
#   0  rewrite applied, or the file already matched (no-op)
#   1  suite file missing
#   2  usage / argument error

set -euo pipefail

usage() {
  echo "usage: $(basename "$0") <suite-file> <target-version>" >&2
}

if [ "$#" -ne 2 ]; then
  usage
  exit 2
fi

suite="$1"
target="$2"

# The target must be a final release tag: vMAJOR.MINOR.PATCH with no rc/dryrun
# prerelease suffix. Refusing a prerelease here is the last line of defence
# behind the caller's trigger gate, so a committed pin can never land on a
# candidate tag even if a caller passes one by mistake.
if ! printf '%s' "$target" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "::error::target version '${target}' is not a final release tag (vX.Y.Z)" >&2
  exit 2
fi

if [ ! -f "$suite" ]; then
  echo "::error::suite file not found: ${suite}" >&2
  exit 1
fi

# Rewrite both pins:
#   1. the setup-cli action ref: ...setup-cli@vX.Y.Z
#   2. the version input:        version: vX.Y.Z
# Only bare semver pins are matched, so a moving ref (@main) or an unrelated
# non-semver value is never touched. A `version:` line carrying a vX.Y.Z value
# is the suite's setup-cli input; this mirrors the floor check's own extraction,
# which treats a `version: vX.Y.Z` line as the tooling pin.
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

# Rewrite both pins only within a setup-cli step block (scoped by YAML indentation).
# The address range /setup-cli@.../,/^      - name:/ matches from a setup-cli
# line to the next step marker (a list item at the same indentation), ensuring
# version: rewrites only the setup-cli input's version field, not any unrelated
# semver value elsewhere in the file.
sed -E \
  '/setup-cli@v[0-9]+\.[0-9]+\.[0-9]+/,/^      - name:/{
    s#(setup-cli@)v[0-9]+\.[0-9]+\.[0-9]+#\1'"${target}"'#g
    s#(version:[[:space:]]*)v[0-9]+\.[0-9]+\.[0-9]+#\1'"${target}"'#g
  }' \
  "$suite" > "$tmp"

# Overwrite only when content actually changed, preserving the original file's
# bytes, mode, and mtime when it already pins the target.
if cmp -s "$suite" "$tmp"; then
  echo "${suite} already pins ${target}; no change"
else
  cat "$tmp" > "$suite"
  echo "bumped bootstrap pin in ${suite} to ${target}"
fi
