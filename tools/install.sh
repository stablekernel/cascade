#!/usr/bin/env bash
# Install git-safe-rebase as a git subcommand
#
# After installation, you can run:
#   git safe-rebase [options]
#
# Usage:
#   ./install.sh           # Install to ~/.local/bin (default)
#   ./install.sh /usr/local/bin  # Install to specific directory
#   ./install.sh --uninstall     # Remove installation

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_INSTALL_DIR="$HOME/.local/bin"

# Colors
if [[ -t 1 ]]; then
  GREEN='\033[0;32m'
  YELLOW='\033[0;33m'
  RED='\033[0;31m'
  RESET='\033[0m'
else
  GREEN=''
  YELLOW=''
  RED=''
  RESET=''
fi

log_info() { echo -e "${GREEN}[INFO]${RESET} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${RESET} $1"; }
log_error() { echo -e "${RED}[ERROR]${RESET} $1" >&2; }

show_help() {
  cat << 'EOF'
Install git-safe-rebase as a git subcommand

Usage:
  ./install.sh                    Install to ~/.local/bin (default)
  ./install.sh <directory>        Install to specific directory
  ./install.sh --uninstall        Remove installation
  ./install.sh --help             Show this help

After installation, you can run:
  git safe-rebase [options]

The tool will be available as a git subcommand because git looks for
executables named 'git-*' in your PATH.

EOF
}

uninstall() {
  local search_paths=(
    "$HOME/.local/bin"
    "/usr/local/bin"
    "$HOME/bin"
  )

  local found=false
  for dir in "${search_paths[@]}"; do
    local target="$dir/git-safe-rebase"
    if [[ -f "$target" ]]; then
      log_info "Removing $target"
      rm -f "$target"
      found=true
    fi
  done

  if [[ "$found" == "true" ]]; then
    log_info "Uninstall complete"
  else
    log_warn "git-safe-rebase not found in common locations"
  fi
  exit 0
}

# Parse arguments
INSTALL_DIR="$DEFAULT_INSTALL_DIR"

if [[ $# -gt 0 ]]; then
  case "$1" in
    --help|-h)
      show_help
      exit 0
      ;;
    --uninstall)
      uninstall
      ;;
    *)
      INSTALL_DIR="$1"
      ;;
  esac
fi

# Check source exists
SOURCE="$SCRIPT_DIR/git-safe-rebase"
if [[ ! -f "$SOURCE" ]]; then
  log_error "Source not found: $SOURCE"
  exit 1
fi

# Create install directory if needed
if [[ ! -d "$INSTALL_DIR" ]]; then
  log_info "Creating directory: $INSTALL_DIR"
  mkdir -p "$INSTALL_DIR"
fi

# Copy script
TARGET="$INSTALL_DIR/git-safe-rebase"
log_info "Installing to: $TARGET"
cp "$SOURCE" "$TARGET"
chmod +x "$TARGET"

# Check if directory is in PATH
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
  log_warn "$INSTALL_DIR is not in your PATH"
  echo ""
  echo "Add it to your shell configuration:"
  echo ""
  echo "  For bash (~/.bashrc):"
  echo "    export PATH=\"\$PATH:$INSTALL_DIR\""
  echo ""
  echo "  For zsh (~/.zshrc):"
  echo "    export PATH=\"\$PATH:$INSTALL_DIR\""
  echo ""
  echo "Then reload your shell or run:"
  echo "  source ~/.bashrc  # or ~/.zshrc"
  echo ""
fi

log_info "Installation complete!"
echo ""
echo "Usage:"
echo "  git safe-rebase --help"
echo ""
echo "Examples:"
echo "  git safe-rebase           # Rebase onto auto-detected trunk"
echo "  git safe-rebase -s        # Auto-stash changes before rebase"
echo "  git safe-rebase -i        # Interactive rebase"
echo "  git safe-rebase -n        # Dry run"
