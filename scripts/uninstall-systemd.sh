#!/usr/bin/env bash
# Remove the native systemd user-unit without deleting durable state by default.
set -euo pipefail

fail() { printf 'uninstall-systemd: %s\n' "$*" >&2; exit 1; }

purge_state=false
if [[ "${1:-}" == "--purge-state" ]]; then
  purge_state=true
  shift
fi
[[ $# -eq 0 ]] || fail "usage: uninstall-systemd.sh [--purge-state]"

STATE_DIR="${CLAUDE_HISTORY_RAG_STATE_DIR:-$HOME/.claude-history-rag}"
CONFIG_HOME="${XDG_CONFIG_HOME:-$HOME/.config}"
SERVICE_DEST="$CONFIG_HOME/systemd/user/ai-agent-history-rag.service"
CONFIG_DIR="$CONFIG_HOME/ai-agent-history-rag"
SYSTEMCTL_BIN="${SYSTEMCTL_BIN:-systemctl}"

[[ "$STATE_DIR" == "$HOME/.claude-history-rag" ]] || fail "state directory must remain the default path for purge safety"
[[ "$CONFIG_HOME" == /* && "$CONFIG_HOME" != *"/../"* && "$CONFIG_HOME" != */.. && "$CONFIG_HOME" != *"//"* ]] || fail "config home must be an absolute clean path"

"$SYSTEMCTL_BIN" --user disable --now ai-agent-history-rag || true
rm -f -- "$SERVICE_DEST"
rm -f -- "$CONFIG_DIR/history-ragd.json" "$CONFIG_DIR/history-ragd.env"
rmdir -- "$CONFIG_DIR" 2>/dev/null || true
"$SYSTEMCTL_BIN" --user daemon-reload

if "$purge_state"; then
  rm -rf -- "$STATE_DIR"
  printf 'native systemd service and durable state removed\n'
else
  printf 'native systemd service removed; durable state retained at %s\n' "$STATE_DIR"
fi
