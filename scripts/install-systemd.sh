#!/usr/bin/env bash
# Install the native History RAG daemon as a hardened systemd user service.
set -euo pipefail

fail() { printf 'install-systemd: %s\n' "$*" >&2; exit 1; }

require_absolute_clean() {
  local value="$1" label="$2"
  [[ "$value" == /* && "$value" != *"/../"* && "$value" != */.. && "$value" != *"//"* ]] || fail "$label must be an absolute clean path"
}

require_safe_value() {
  local name="$1" value="${!1:-}"
  [[ -n "$value" ]] || fail "$name must be set"
  [[ "$value" =~ ^[A-Za-z0-9._~+/:=@-]+$ ]] || fail "$name contains unsupported environment-file characters"
}

require_exact() {
  local name="$1" expected="$2"
  require_safe_value "$name"
  [[ "${!name}" == "$expected" ]] || fail "$name must equal $expected"
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd -P)"
SERVICE_NAME="ai-agent-history-rag.service"
SYSTEMCTL_BIN="${SYSTEMCTL_BIN:-systemctl}"
STATE_DIR="${CLAUDE_HISTORY_RAG_STATE_DIR:-$HOME/.claude-history-rag}"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/ai-agent-history-rag"
CONFIG_PATH="$CONFIG_DIR/history-ragd.json"
ENV_PATH="$CONFIG_DIR/history-ragd.env"
SERVICE_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
SERVICE_DEST="$SERVICE_DIR/$SERVICE_NAME"
HISTORY_RAGD_BIN="${HISTORY_RAGD_BIN:-$PROJECT_DIR/bin/history-ragd}"

require_absolute_clean "$PROJECT_DIR" "project directory"
require_absolute_clean "$STATE_DIR" "state directory"
require_absolute_clean "$CONFIG_DIR" "config directory"
require_absolute_clean "$HISTORY_RAGD_BIN" "history-ragd binary"
[[ -x "$HISTORY_RAGD_BIN" && ! -L "$HISTORY_RAGD_BIN" ]] || fail "history-ragd binary must be an executable non-link file"
case "$HISTORY_RAGD_BIN" in "$PROJECT_DIR"/*) ;; *) fail "history-ragd binary must be beneath the project directory" ;; esac

# The production selector is intentionally closed. The service never offers a
# local database, emulator, unauthenticated listener, or credential-file arm.
require_exact CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT production
require_exact CLAUDE_HISTORY_RAG_STORAGE_BACKEND spanner
require_exact CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE spanner
require_exact CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID ConversationEmbeddingModel
require_exact CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER vertex
require_exact CLAUDE_HISTORY_RAG_EMBEDDING_MODEL gemini-embedding-001
require_exact CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION 3072
require_exact CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST 127.0.0.1
require_exact CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT 4680
require_exact CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE application_default
require_exact CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE impersonated_service_account
for required in \
  CLAUDE_HISTORY_RAG_SPANNER_PROJECT \
  CLAUDE_HISTORY_RAG_SPANNER_INSTANCE \
  CLAUDE_HISTORY_RAG_SPANNER_DATABASE \
  CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY \
  CLAUDE_HISTORY_RAG_SERVER_PSK; do
  require_safe_value "$required"
done

# The validated impersonated ADC carrier is discovered at its well-known path.
# Passing a credential-file override would be refused by the native selector.
for forbidden in GOOGLE_APPLICATION_CREDENTIALS CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE CLOUDSDK_CONFIG SPANNER_EMULATOR_HOST; do
  [[ -z "${!forbidden:-}" ]] || fail "$forbidden must be unset"
done

CLAUDE_ROOT="${CLAUDE_HISTORY_RAG_PROJECTS_PATH:-$HOME/.claude/projects}"
CODEX_ROOT="${CLAUDE_HISTORY_RAG_CODEX_SESSIONS_PATH:-$HOME/.codex/sessions}"
GEMINI_ROOT="${CLAUDE_HISTORY_RAG_GEMINI_SESSIONS_PATH:-$HOME/.gemini/tmp}"
ANTIGRAVITY_ROOT="${CLAUDE_HISTORY_RAG_ANTIGRAVITY_SESSIONS_PATH:-$HOME/.gemini/antigravity}"
CHATGPT_ROOT="${CLAUDE_HISTORY_RAG_CHATGPT_EXPORTS_PATH:-$STATE_DIR/imports/chatgpt}"
CLAUDE_APP_ROOT="${CLAUDE_HISTORY_RAG_CLAUDE_APP_EXPORTS_PATH:-$STATE_DIR/imports/claude-app}"
WATCH_ROOTS=("$CLAUDE_ROOT" "$CODEX_ROOT" "$GEMINI_ROOT" "$ANTIGRAVITY_ROOT" "$CHATGPT_ROOT" "$CLAUDE_APP_ROOT")
for root in "${WATCH_ROOTS[@]}"; do
  require_absolute_clean "$root" "watch root"
done

umask 077
install -d -m 700 "$STATE_DIR" "$CONFIG_DIR" "$SERVICE_DIR"
for directory in "$STATE_DIR" "$CONFIG_DIR" "$SERVICE_DIR"; do
  [[ -d "$directory" && ! -L "$directory" ]] || fail "managed directory must be a non-link directory"
done
for root in "${WATCH_ROOTS[@]}"; do
  install -d -m 700 "$root"
  [[ -d "$root" && ! -L "$root" ]] || fail "watch root must be a non-link directory"
done

cat > "$CONFIG_PATH" <<EOF
{
  "state_dir": "$STATE_DIR",
  "listen": "127.0.0.1:4680",
  "container_mode": false,
  "watch_roots": ["$CLAUDE_ROOT", "$CODEX_ROOT", "$GEMINI_ROOT", "$ANTIGRAVITY_ROOT", "$CHATGPT_ROOT", "$CLAUDE_APP_ROOT"],
  "pid_file": "$STATE_DIR/daemon.pid",
  "auth_state_file": "$STATE_DIR/auth.json",
  "auth_enabled": true,
  "checkout_root": "$PROJECT_DIR",
  "executable": "$HISTORY_RAGD_BIN"
}
EOF
chmod 600 "$CONFIG_PATH"

{
  for name in \
    CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT \
    CLAUDE_HISTORY_RAG_STORAGE_BACKEND \
    CLAUDE_HISTORY_RAG_SPANNER_PROJECT \
    CLAUDE_HISTORY_RAG_SPANNER_INSTANCE \
    CLAUDE_HISTORY_RAG_SPANNER_DATABASE \
    CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE \
    CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID \
    CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER \
    CLAUDE_HISTORY_RAG_EMBEDDING_MODEL \
    CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION \
    CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST \
    CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT \
    CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE \
    CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE \
    CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY \
    CLAUDE_HISTORY_RAG_SERVER_PSK; do
    printf '%s=%s\n' "$name" "${!name}"
  done
} > "$ENV_PATH"
chmod 600 "$ENV_PATH"

cat > "$SERVICE_DEST" <<EOF
[Unit]
Description=AI Agent History RAG native daemon
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$PROJECT_DIR
EnvironmentFile=$ENV_PATH
ExecStart=$HISTORY_RAGD_BIN supervise --config $CONFIG_PATH
Restart=on-failure
RestartSec=5
TimeoutStopSec=20
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=$STATE_DIR
CapabilityBoundingSet=
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6

[Install]
WantedBy=default.target
EOF
chmod 600 "$SERVICE_DEST"

"$SYSTEMCTL_BIN" --user daemon-reload
"$SYSTEMCTL_BIN" --user enable --now ai-agent-history-rag

printf 'native systemd service installed: %s\n' "$SERVICE_DEST"
printf 'liveness: curl --fail http://127.0.0.1:4680/live\n'
printf 'readiness (authenticated): GET /health returns 503 until Spanner and the watcher are ready\n'
