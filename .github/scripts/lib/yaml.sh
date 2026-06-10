#!/usr/bin/env bash
# YAML manipulation helpers using yq v4+
# Usage: source lib/yaml.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/log.sh"

# Check yq is available
_require_yq() {
  if ! command -v yq &> /dev/null; then
    log_error "yq is required but not installed"
    exit 1
  fi
}

# Check file exists
_require_file() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    log_error "File not found: $file"
    return 1
  fi
}

# Get value from YAML file
# Usage: yaml_get "file.yaml" ".path.to.key"
yaml_get() {
  _require_yq
  local file="$1"
  local path="$2"
  _require_file "$file" || return 1
  yq eval "$path" "$file"
}

# Set value in YAML file (in place)
# Usage: yaml_set "file.yaml" ".path.to.key" "value"
yaml_set() {
  _require_yq
  local file="$1"
  local path="$2"
  local value="$3"
  _require_file "$file" || return 1
  yq eval -i "$path = \"$value\"" "$file"
  log_debug "Set $path = $value in $file"
}

# Set value without quotes (for numbers, booleans)
# Usage: yaml_set_raw "file.yaml" ".path.to.key" "true"
yaml_set_raw() {
  _require_yq
  local file="$1"
  local path="$2"
  local value="$3"
  _require_file "$file" || return 1
  yq eval -i "$path = $value" "$file"
  log_debug "Set $path = $value (raw) in $file"
}

# Get array values as newline-separated list
# Usage: yaml_get_array "file.yaml" ".path.to.array"
yaml_get_array() {
  _require_yq
  local file="$1"
  local path="$2"
  _require_file "$file" || return 1
  yq eval "$path[]" "$file" 2>/dev/null || true
}

# Get array as JSON
# Usage: yaml_get_json "file.yaml" ".path"
yaml_get_json() {
  _require_yq
  local file="$1"
  local path="$2"
  _require_file "$file" || return 1
  yq eval -o=json "$path" "$file"
}

# Merge overlay into base, output to stdout
# Usage: yaml_merge "base.yaml" "overlay.yaml"
yaml_merge() {
  _require_yq
  local base="$1"
  local overlay="$2"
  _require_file "$base" || return 1
  _require_file "$overlay" || return 1
  yq eval-all "select(fileIndex == 0) * select(fileIndex == 1)" "$base" "$overlay"
}

# Validate YAML syntax
# Usage: yaml_validate "file.yaml"
yaml_validate() {
  _require_yq
  local file="$1"
  _require_file "$file" || return 1
  if yq eval "." "$file" > /dev/null 2>&1; then
    log_debug "YAML valid: $file"
    return 0
  else
    log_error "Invalid YAML: $file"
    return 1
  fi
}

# Check if path exists in YAML
# Usage: yaml_has "file.yaml" ".path.to.key"
yaml_has() {
  _require_yq
  local file="$1"
  local path="$2"
  _require_file "$file" || return 1
  local result
  result=$(yq eval "$path | type" "$file")
  [[ "$result" != "!!null" ]]
}
