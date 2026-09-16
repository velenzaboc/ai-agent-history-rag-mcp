#!/usr/bin/env bash
# Remove the native LaunchAgent without deleting durable state by default.
set -euo pipefail

fail() { printf 'uninstall-launchd: %s\n' "$*" >&2; exit 1; }

purge_state=false
if [[ "${1:-}" == "--purge-state" ]]; then
  purge_state=true
  shift
fi
[[ $# -eq 0 ]] || fail "usage: uninstall-launchd.sh [--purge-state]"

LABEL="com.ai-agent-history-rag.daemon"
READ_ONLY="${CLAUDE_HISTORY_RAG_READ_ONLY:-false}"
case "$READ_ONLY" in true) LABEL="com.ai-agent-history-rag.search" ;; false) ;; *) fail "read-only must be true or false" ;; esac
LAUNCHCTL_BIN="${LAUNCHCTL_BIN:-launchctl}"
STATE_DIR="${CLAUDE_HISTORY_RAG_STATE_DIR:-$HOME/.claude-history-rag}"
DEFAULT_STATE="$HOME/.claude-history-rag"
CONFIG_NAME="history-ragd.json"
if [[ "$READ_ONLY" == true ]]; then DEFAULT_STATE="$HOME/.claude-history-rag-native-search"; STATE_DIR="${CLAUDE_HISTORY_RAG_STATE_DIR:-$DEFAULT_STATE}"; CONFIG_NAME="history-ragd-search.json"; fi
CONFIG_DIR="$HOME/.config/ai-agent-history-rag"
PLIST_DEST="$HOME/Library/LaunchAgents/$LABEL.plist"

[[ "$STATE_DIR" == "$DEFAULT_STATE" ]] || fail "state directory must remain the default path for purge safety"
[[ "$CONFIG_DIR" == "$HOME/.config/ai-agent-history-rag" ]] || fail "config directory must remain beneath the default user config root"

"$LAUNCHCTL_BIN" bootout "gui/$UID/$LABEL" 2>/dev/null || true
rm -f -- "$PLIST_DEST" "$CONFIG_DIR/$CONFIG_NAME"
rmdir -- "$CONFIG_DIR" 2>/dev/null || true

if "$purge_state"; then
  rm -rf -- "$STATE_DIR"
  printf 'native launchd agent and durable state removed\n'
else
  printf 'native launchd agent removed; durable state retained at %s\n' "$STATE_DIR"
fi
