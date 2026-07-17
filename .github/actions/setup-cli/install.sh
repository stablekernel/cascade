#!/usr/bin/env bash
# Downloads, verifies, and installs the cascade CLI release archive. Called by
# the setup-cli composite action (action.yaml) and exercised hermetically by
# internal/setupcli against fixture releases and a stubbed gh, so keep the
# interface stable:
#
#   GH_TOKEN             token gh uses for the release downloads
#   TAG                  concrete release tag (already resolved, never "latest")
#   ARCHIVE_PATTERN      OS/arch archive glob, e.g. cascade_*_linux_amd64.tar.gz
#   CASCADE_INSTALL_DIR  install destination, defaults to /usr/local/bin
set -euo pipefail

INSTALL_DIR="${CASCADE_INSTALL_DIR:-/usr/local/bin}"

# GitHub's API and release CDN occasionally answer a download for a release
# that exists with a spurious error: a 5xx, an HTML error page, or a "release
# not found" that succeeds on the very next call. An unguarded download turns
# one such blip into a failed install, so every gh call below runs through a
# small retry budget with exponential backoff.
#
# Every failure is retried, rather than only those matching a classifier. gh's
# error text does not reliably separate a transient fault from a permanent one
# (the incident that motivated this reported a missing release for one that was
# published), and a wrong classifier fails in the more damaging direction. The
# budget is the safeguard instead: three attempts and roughly 6s of backoff
# means a genuinely missing release (a deleted tag, a typo'd version) still
# fails loudly within seconds rather than hanging, so a dangling pin stays as
# easy to spot as it was before.
GH_MAX_ATTEMPTS="${CASCADE_GH_MAX_ATTEMPTS:-3}"
GH_RETRY_BASE_SLEEP="${CASCADE_GH_RETRY_BASE_SLEEP:-2}"

gh_retry() {
  local attempt=1
  local sleep_for="$GH_RETRY_BASE_SLEEP"
  while :; do
    if gh "$@"; then
      return 0
    fi
    if [ "$attempt" -ge "$GH_MAX_ATTEMPTS" ]; then
      echo "gh $1 $2 failed after $attempt attempts."
      return 1
    fi
    echo "gh $1 $2 failed (attempt $attempt/$GH_MAX_ATTEMPTS); retrying in ${sleep_for}s..."
    if [ "$sleep_for" -gt 0 ]; then
      sleep "$sleep_for"
    fi
    attempt=$((attempt + 1))
    sleep_for=$((sleep_for * 2))
  done
}

echo "Downloading archive matching $ARCHIVE_PATTERN from release $TAG..."
workdir=$(mktemp -d)
gh_retry release download "$TAG" \
  -R stablekernel/cascade \
  -p "$ARCHIVE_PATTERN" \
  -D "$workdir"

# Integrity gate: every release publishes checksums.txt, and nothing is
# installed until the downloaded archive's sha256 matches it. A release
# without checksums.txt, an archive with no checksum entry, or a hash
# mismatch all abort the install.
gh_retry release download "$TAG" \
  -R stablekernel/cascade \
  -p "checksums.txt" \
  -D "$workdir"

cd "$workdir"
shopt -s nullglob
archives=(cascade_*.tar.gz)
if [ "${#archives[@]}" -ne 1 ]; then
  echo "::error::Expected exactly one downloaded archive for $ARCHIVE_PATTERN, found ${#archives[@]}; refusing to install."
  exit 1
fi
ARCHIVE="${archives[0]}"
if ! awk -v f="$ARCHIVE" '$2 == f { print; ok = 1 } END { exit ok ? 0 : 1 }' checksums.txt > archive.sha256; then
  echo "::error::No checksum entry for $ARCHIVE in checksums.txt of release $TAG; refusing to install."
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum -c archive.sha256
else
  shasum -a 256 -c archive.sha256
fi

# Authenticity gate: the release pipeline cosign-signs checksums.txt
# keylessly (Sigstore bundle format, cosign v3). Verify the signature
# whenever the bundle and a cosign binary are available; a failed
# verification always aborts. If either is unavailable (an old release
# without a bundle, or a runner without cosign), fall back to the checksum
# gate above with a loud warning, never silently.
# Manual equivalent: docs/release-verification.md.
# The bundle fetch retries too: a blip here would otherwise downgrade a signed
# release to the sha256-only fallback silently, which is the weaker gate. The
# cost is that a release genuinely published without a bundle now spends the
# retry budget before warning, which is seconds on a path that already warns.
if gh_retry release download "$TAG" -R stablekernel/cascade -p "checksums.txt.bundle" -D .; then
  if command -v cosign >/dev/null 2>&1; then
    # cosign v2 needs --new-bundle-format to read the Sigstore bundle
    # format that the release pipeline (cosign v3) emits; probe the flag
    # so both major versions verify correctly.
    NEW_BUNDLE_FLAG=""
    COSIGN_HELP="$(cosign verify-blob --help 2>&1 || true)"
    case "$COSIGN_HELP" in
      *--new-bundle-format*) NEW_BUNDLE_FLAG="--new-bundle-format=true" ;;
    esac
    cosign verify-blob \
      --bundle=checksums.txt.bundle \
      --certificate-identity-regexp='^https://github.com/stablekernel/cascade/' \
      --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
      ${NEW_BUNDLE_FLAG:+"$NEW_BUNDLE_FLAG"} \
      checksums.txt
    echo "cosign signature on checksums.txt verified."
  else
    echo "::warning::cosign not found on this runner; proceeding on sha256 verification only (checksums.txt signature not verified)."
  fi
else
  echo "::warning::Could not download checksums.txt.bundle for release $TAG; proceeding on sha256 verification only (checksums.txt signature not verified)."
fi

tar -xzf "$ARCHIVE"
install -m 0755 cascade "$INSTALL_DIR/cascade"
cd /
rm -rf "$workdir"
