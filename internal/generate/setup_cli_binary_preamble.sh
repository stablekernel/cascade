#!/usr/bin/env bash
# Binary-mode preamble for the cascade setup-cli install. The generator emits
# this inline (via --cli-install=binary) instead of the setup-cli composite
# action, so a downstream repo installs the CLI with no third-party `uses:` step
# and needs no organization Actions allowlist entry for a third-party action.
#
# It resolves the requested version, detects the runner OS/arch, installs a
# checksum-verified cosign by direct binary download (not the cosign-installer
# action, so the authenticity tooling itself stays under first-party control and
# is gated the same way the release archive is), then hands off to install.sh.
# install.sh owns the mandatory sha256 gate and the keyless cosign verification
# of checksums.txt, and is shared byte-for-byte with the composite action, so the
# verify contract has exactly one source and cannot drift between the two modes.
#
# Everything the caller controls arrives through the environment, never spliced
# into this script text:
#   GH_TOKEN               token gh uses for release and cosign downloads
#   CASCADE_CLI_VERSION    requested version ("latest" or a concrete tag)
#   CASCADE_INSTALL_SCRIPT path to the install.sh the caller wrote to disk
set -euo pipefail

# cosign is pinned to an immutable release and gated against its published
# checksum, so a compromised or swapped cosign binary is rejected before it can
# run. Update both the version and the matching per-arch checksums together.
COSIGN_VERSION="v2.4.3"
COSIGN_SHA256_AMD64="caaad125acef1cb81d58dcdc454a1e429d09a750d1e9e2b3ed1aed8964454708"
COSIGN_SHA256_ARM64="bd0f9763bca54de88699c3656ade2f39c9a1c7a2916ff35601caf23a79be0629"

VERSION="${CASCADE_CLI_VERSION:-latest}"

# Resolve "latest" to the concrete release tag, matching the composite action and
# the pin-reconcile resolvers: pre-releases and drafts are excluded so a default
# caller never installs an rc or dryrun build.
if [ "$VERSION" = "latest" ]; then
  TAG="$(gh release list -R stablekernel/cascade -L 1 --exclude-pre-releases --exclude-drafts --json tagName -q '.[0].tagName')"
else
  TAG="$VERSION"
fi
if [ -z "${TAG:-}" ]; then
  echo "::error::could not resolve a cascade release tag from version '$VERSION'."
  exit 1
fi

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64 | arm64) ARCH="arm64" ;;
esac
ARCHIVE_PATTERN="cascade_*_${OS}_${ARCH}.tar.gz"

# install_cosign fetches cosign by direct download and verifies its sha256
# against the pinned value before installing it, so binary mode pulls in no
# third-party action and never runs an unverified cosign. A checksum mismatch
# aborts (set -e on the -c check). Only linux amd64/arm64 (the GitHub-hosted
# runner arches) carry a pinned checksum; any other target, or a failed
# download, skips the cosign install and lets install.sh fall back to the
# sha256-only gate with a loud warning, exactly as it does when a runner ships
# without cosign.
install_cosign() {
  local expected="" dir asset
  case "$ARCH" in
    amd64) expected="$COSIGN_SHA256_AMD64" ;;
    arm64) expected="$COSIGN_SHA256_ARM64" ;;
  esac
  if [ "$OS" != "linux" ] || [ -z "$expected" ]; then
    echo "::warning::no pinned cosign checksum for ${OS}/${ARCH}; skipping cosign install (install.sh will fall back to sha256-only verification with a warning)."
    return 0
  fi
  if command -v cosign >/dev/null 2>&1; then
    return 0
  fi
  dir="$(mktemp -d)"
  asset="cosign-linux-${ARCH}"
  echo "Installing cosign ${COSIGN_VERSION} (${asset})..."
  if ! curl -fsSL -o "$dir/cosign" "https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}/${asset}"; then
    echo "::warning::cosign download failed; skipping cosign install (install.sh will fall back to sha256-only verification with a warning)."
    rm -rf "$dir"
    return 0
  fi
  echo "${expected}  ${dir}/cosign" >"$dir/cosign.sha256"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "$dir/cosign.sha256"
  else
    shasum -a 256 -c "$dir/cosign.sha256"
  fi
  chmod +x "$dir/cosign"
  # Plain install (no sudo), matching install.sh: /usr/local/bin is writable by
  # the runner user on GitHub-hosted runners.
  install -m 0755 "$dir/cosign" /usr/local/bin/cosign
  rm -rf "$dir"
}
install_cosign

INSTALL_SCRIPT="${CASCADE_INSTALL_SCRIPT:?CASCADE_INSTALL_SCRIPT must point at the install.sh written by the caller}"
TAG="$TAG" ARCHIVE_PATTERN="$ARCHIVE_PATTERN" bash "$INSTALL_SCRIPT"
