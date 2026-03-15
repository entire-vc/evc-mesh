#!/bin/bash
# Install pre-push hooks for Daedalus repos
# Usage: ./scripts/install-hooks.sh [repo-path] [go|bash]
#
# Examples:
#   ./scripts/install-hooks.sh                           # install in current repo (Go)
#   ./scripts/install-hooks.sh /tmp/evc-mesh-mcp-repo go # install in MCP repo
#   ./scripts/install-hooks.sh /path/to/skill bash       # install in OpenClaw skill repo

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_PATH="${1:-.}"
HOOK_TYPE="${2:-go}"

if [ ! -d "$REPO_PATH/.git" ]; then
    echo "ERROR: $REPO_PATH is not a git repository"
    exit 1
fi

HOOKS_DIR="$REPO_PATH/.git/hooks"

if [ "$HOOK_TYPE" = "bash" ]; then
    cp "$SCRIPT_DIR/hooks/pre-push-bash" "$HOOKS_DIR/pre-push"
else
    cp "$SCRIPT_DIR/hooks/pre-push" "$HOOKS_DIR/pre-push"
fi

chmod +x "$HOOKS_DIR/pre-push"
echo "Installed pre-push hook ($HOOK_TYPE) in $REPO_PATH"
