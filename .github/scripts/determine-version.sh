#!/usr/bin/env bash
# Determine next semantic version based on conventional commits
# Usage: determine-version.sh --repo <owner/repo> --base-sha <sha> --head-sha <sha> [--override <version>] --token <token>
#
# Outputs (4 lines to stdout):
#   1. current_version: Last released version
#   2. suggested_version: Calculated next version
#   3. final_version: Override if provided, else suggested
#   4. bump_type: major|minor|patch

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/log.sh"

# Parse arguments
REPO=""
BASE_SHA=""
HEAD_SHA=""
OVERRIDE=""
TOKEN=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --repo)
      REPO="$2"
      shift 2
      ;;
    --base-sha)
      BASE_SHA="$2"
      shift 2
      ;;
    --head-sha)
      HEAD_SHA="$2"
      shift 2
      ;;
    --override)
      OVERRIDE="$2"
      shift 2
      ;;
    --token)
      TOKEN="$2"
      shift 2
      ;;
    *)
      log_error "Unknown option: $1"
      exit 1
      ;;
  esac
done

# Validate required arguments
if [[ -z "$REPO" ]]; then
  log_error "Missing required argument: --repo"
  exit 1
fi

if [[ -z "$BASE_SHA" ]]; then
  log_error "Missing required argument: --base-sha"
  exit 1
fi

if [[ -z "$HEAD_SHA" ]]; then
  log_error "Missing required argument: --head-sha"
  exit 1
fi

if [[ -z "$TOKEN" ]]; then
  log_error "Missing required argument: --token"
  exit 1
fi

log_info "Determining version from $BASE_SHA to $HEAD_SHA"

# Get latest release version from GitHub
API_BASE="https://api.github.com/repos/$REPO"

LATEST_RELEASE=$(curl -s \
  -H "Accept: application/vnd.github+json" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  "$API_BASE/releases/latest" 2>/dev/null || echo "{}")

CURRENT_VERSION=$(echo "$LATEST_RELEASE" | jq -r '.tag_name // "v0.0.0"')
# Strip leading 'v' if present
CURRENT_VERSION="${CURRENT_VERSION#v}"

log_info "Current version: $CURRENT_VERSION"

# Validate semver format
if ! [[ "$CURRENT_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?(\+[a-zA-Z0-9.-]+)?$ ]]; then
  log_warn "Current version '$CURRENT_VERSION' is not valid semver, defaulting to 0.0.0"
  CURRENT_VERSION="0.0.0"
fi

# Parse current version
IFS='.' read -r MAJOR MINOR PATCH <<< "${CURRENT_VERSION%%-*}"
MAJOR=${MAJOR:-0}
MINOR=${MINOR:-0}
PATCH=${PATCH:-0}

log_debug "Parsed version: major=$MAJOR, minor=$MINOR, patch=$PATCH"

# Analyze commits to determine bump type
COMMITS=$(git log --format="%s|%b" "$BASE_SHA..$HEAD_SHA" 2>/dev/null || echo "")

HAS_BREAKING=false
HAS_FEATURE=false
HAS_FIX=false

while IFS='|' read -r subject body; do
  [[ -z "$subject" ]] && continue

  # Check for breaking changes
  if [[ "$subject" =~ ^[a-z]+(\([^)]+\))?!: ]] || [[ "$body" =~ BREAKING[[:space:]]CHANGE: ]]; then
    HAS_BREAKING=true
    log_debug "Found breaking change: $subject"
  fi

  # Check for features
  if [[ "$subject" =~ ^feat(\([^)]+\))?[!]?: ]]; then
    HAS_FEATURE=true
    log_debug "Found feature: $subject"
  fi

  # Check for fixes
  if [[ "$subject" =~ ^fix(\([^)]+\))?[!]?: ]]; then
    HAS_FIX=true
    log_debug "Found fix: $subject"
  fi
done <<< "$COMMITS"

# Determine bump type
BUMP_TYPE="patch"
NEW_MAJOR=$MAJOR
NEW_MINOR=$MINOR
NEW_PATCH=$((PATCH + 1))

if [[ "$HAS_BREAKING" == "true" ]]; then
  BUMP_TYPE="major"
  NEW_MAJOR=$((MAJOR + 1))
  NEW_MINOR=0
  NEW_PATCH=0
elif [[ "$HAS_FEATURE" == "true" ]]; then
  BUMP_TYPE="minor"
  NEW_MINOR=$((MINOR + 1))
  NEW_PATCH=0
elif [[ "$HAS_FIX" == "true" ]]; then
  BUMP_TYPE="patch"
  # Already set above
else
  # No conventional commits found, default to patch
  log_debug "No conventional commits found, defaulting to patch bump"
  BUMP_TYPE="patch"
fi

SUGGESTED_VERSION="$NEW_MAJOR.$NEW_MINOR.$NEW_PATCH"

log_info "Suggested version: $SUGGESTED_VERSION (bump: $BUMP_TYPE)"

# Determine final version
FINAL_VERSION="$SUGGESTED_VERSION"
if [[ -n "$OVERRIDE" ]]; then
  # Validate override is valid semver
  if [[ "$OVERRIDE" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?(\+[a-zA-Z0-9.-]+)?$ ]]; then
    FINAL_VERSION="${OVERRIDE#v}"
    log_info "Using override version: $FINAL_VERSION"
  else
    log_error "Invalid override version: $OVERRIDE (must be valid semver)"
    exit 1
  fi
fi

# Output results
echo "$CURRENT_VERSION"
echo "$SUGGESTED_VERSION"
echo "$FINAL_VERSION"
echo "$BUMP_TYPE"
