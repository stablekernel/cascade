#!/usr/bin/env bash
# GitHub Actions helper functions
# Usage: source lib/gha.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/log.sh"

# Get action input value
# Usage: gha_input "name" ["default"]
gha_input() {
  local name="$1"
  local default="${2:-}"
  local var_name="INPUT_$(echo "$name" | tr '[:lower:]' '[:upper:]' | tr '-' '_')"
  local value="${!var_name:-$default}"
  echo "$value"
}

# Set action output
# Usage: gha_output "name" "value"
gha_output() {
  local name="$1"
  local value="$2"
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    echo "${name}=${value}" >> "$GITHUB_OUTPUT"
    log_debug "Set output: $name=$value"
  else
    log_debug "Would set output: $name=$value"
  fi
}

# Set multiline action output
# Usage: gha_output_multiline "name" "value"
gha_output_multiline() {
  local name="$1"
  local value="$2"
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
      echo "${name}<<EOF"
      echo "$value"
      echo "EOF"
    } >> "$GITHUB_OUTPUT"
    log_debug "Set multiline output: $name"
  else
    log_debug "Would set multiline output: $name"
  fi
}

# Append to job summary
# Usage: gha_summary "markdown content"
gha_summary() {
  local content="$1"
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    echo "$content" >> "$GITHUB_STEP_SUMMARY"
  else
    log_debug "Would add to summary: $content"
  fi
}

# Fail the action with error message
# Usage: gha_fail "error message"
gha_fail() {
  local msg="$1"
  log_error "$msg"
  exit 1
}

# Mask a value in logs
# Usage: gha_mask "sensitive value"
gha_mask() {
  local value="$1"
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::add-mask::$value"
  fi
}

# Set environment variable for subsequent steps
# Usage: gha_set_env "name" "value"
gha_set_env() {
  local name="$1"
  local value="$2"
  if [[ -n "${GITHUB_ENV:-}" ]]; then
    echo "${name}=${value}" >> "$GITHUB_ENV"
    log_debug "Set env: $name=$value"
  else
    log_debug "Would set env: $name=$value"
  fi
}
