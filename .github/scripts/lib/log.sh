#!/usr/bin/env bash
# Shared logging library for CI/CD scripts
# Usage: source lib/log.sh
# Control: LOG_LEVEL=info|debug (default: info)

# Source guard - prevent double sourcing
[[ -n "${_LOG_SH_SOURCED:-}" ]] && return 0
_LOG_SH_SOURCED=1

set -euo pipefail

# Colors (only when TTY and not in GHA)
if [[ -t 1 ]] && [[ -z "${GITHUB_ACTIONS:-}" ]]; then
  readonly COLOR_RESET="\033[0m"
  readonly COLOR_RED="\033[0;31m"
  readonly COLOR_GREEN="\033[0;32m"
  readonly COLOR_YELLOW="\033[0;33m"
  readonly COLOR_BLUE="\033[0;34m"
  readonly COLOR_GRAY="\033[0;90m"
else
  readonly COLOR_RESET=""
  readonly COLOR_RED=""
  readonly COLOR_GREEN=""
  readonly COLOR_YELLOW=""
  readonly COLOR_BLUE=""
  readonly COLOR_GRAY=""
fi

# Log level (info or debug)
: "${LOG_LEVEL:=info}"

_timestamp() {
  date -u +"%Y-%m-%dT%H:%M:%SZ"
}

log_info() {
  local msg="$1"
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::notice::$msg" >&2
  else
    echo -e "${COLOR_BLUE}[INFO]${COLOR_RESET} $(_timestamp) $msg" >&2
  fi
}

log_debug() {
  [[ "$LOG_LEVEL" != "debug" ]] && return 0
  local msg="$1"
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::debug::$msg" >&2
  else
    echo -e "${COLOR_GRAY}[DEBUG]${COLOR_RESET} $(_timestamp) $msg" >&2
  fi
}

log_warn() {
  local msg="$1"
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::warning::$msg" >&2
  else
    echo -e "${COLOR_YELLOW}[WARN]${COLOR_RESET} $(_timestamp) $msg" >&2
  fi
}

log_error() {
  local msg="$1"
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::error::$msg" >&2
  else
    echo -e "${COLOR_RED}[ERROR]${COLOR_RESET} $(_timestamp) $msg" >&2
  fi
}

log_success() {
  local msg="$1"
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::notice::✓ $msg" >&2
  else
    echo -e "${COLOR_GREEN}[SUCCESS]${COLOR_RESET} $(_timestamp) $msg" >&2
  fi
}

# Group output in GitHub Actions
log_group_start() {
  local title="$1"
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::group::$title" >&2
  else
    log_info "=== $title ==="
  fi
}

log_group_end() {
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::endgroup::" >&2
  fi
}
