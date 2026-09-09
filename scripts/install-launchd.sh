#!/usr/bin/env bash
# Install the native History RAG daemon as a hardened macOS LaunchAgent.
set -euo pipefail

fail() { printf 'install-launchd: %s\n' "$*" >&2; exit 1; }

require_absolute_clean() {
  local value="$1" label="$2"
  [[ "$value" == /* && "$value" != *"/../"* && "$value" != */.. && "$value" != *"//"* ]] || fail "$label must be an absolute clean path"
}

require_safe_value() {
  local name="$1" value="${!1:-}"
  [[ -n "$value" ]] || fail "$name must be set"
  [[ "$value" =~ ^[A-Za-z0-9._~+/:=@-]+$ ]] || fail "$name contains unsupported plist characters"
}

require_exact() {
  local name="$1" expected="$2"
  require_safe_value "$name"
  [[ "${!name}" == "$expected" ]] || fail "$name must equal $expected"
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd -P)"
LABEL="com.ai-agent-history-rag.daemon"
LAUNCHCTL_BIN="${LAUNCHCTL_BIN:-launchctl}"
STATE_DIR="${CLAUDE_HISTORY_RAG_STATE_DIR:-$HOME/.claude-history-rag}"
CONFIG_DIR="$HOME/.config/ai-agent-history-rag"
CONFIG_PATH="$CONFIG_DIR/history-ragd.json"
PLIST_DIR="$HOME/Library/LaunchAgents"
PLIST_DEST="$PLIST_DIR/$LABEL.plist"
HISTORY_RAGD_BIN="${HISTORY_RAGD_BIN:-$PROJECT_DIR/bin/history-ragd}"

require_absolute_clean "$PROJECT_DIR" "project directory"
require_absolute_clean "$STATE_DIR" "state directory"
require_absolute_clean "$CONFIG_DIR" "config directory"
require_absolute_clean "$PLIST_DIR" "LaunchAgents directory"
require_absolute_clean "$HISTORY_RAGD_BIN" "history-ragd binary"
[[ -x "$HISTORY_RAGD_BIN" && ! -L "$HISTORY_RAGD_BIN" ]] || fail "history-ragd binary must be an executable non-link file"
case "$HISTORY_RAGD_BIN" in "$PROJECT_DIR"/*) ;; *) fail "history-ragd binary must be beneath the project directory" ;; esac

# The production selector is deliberately closed: this launch agent has no
# local database, emulator, credential-file override, public bind, or auth-off arm.
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
install -d -m 700 "$STATE_DIR" "$CONFIG_DIR" "$PLIST_DIR"
for directory in "$STATE_DIR" "$CONFIG_DIR" "$PLIST_DIR"; do
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

cat > "$PLIST_DEST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$HISTORY_RAGD_BIN</string>
    <string>supervise</string>
    <string>--config</string>
    <string>$CONFIG_PATH</string>
  </array>
  <key>WorkingDirectory</key><string>$PROJECT_DIR</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key><string>$HOME</string>
    <key>PATH</key><string>/usr/bin:/bin:/usr/sbin:/sbin</string>
    <key>CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT</key><string>$CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT</string>
    <key>CLAUDE_HISTORY_RAG_STORAGE_BACKEND</key><string>$CLAUDE_HISTORY_RAG_STORAGE_BACKEND</string>
    <key>CLAUDE_HISTORY_RAG_SPANNER_PROJECT</key><string>$CLAUDE_HISTORY_RAG_SPANNER_PROJECT</string>
    <key>CLAUDE_HISTORY_RAG_SPANNER_INSTANCE</key><string>$CLAUDE_HISTORY_RAG_SPANNER_INSTANCE</string>
    <key>CLAUDE_HISTORY_RAG_SPANNER_DATABASE</key><string>$CLAUDE_HISTORY_RAG_SPANNER_DATABASE</string>
    <key>CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE</key><string>$CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE</string>
    <key>CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID</key><string>$CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID</string>
    <key>CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER</key><string>$CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER</string>
    <key>CLAUDE_HISTORY_RAG_EMBEDDING_MODEL</key><string>$CLAUDE_HISTORY_RAG_EMBEDDING_MODEL</string>
    <key>CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION</key><string>$CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION</string>
    <key>CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST</key><string>$CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST</string>
    <key>CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT</key><string>$CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT</string>
    <key>CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE</key><string>$CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE</string>
    <key>CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE</key><string>$CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE</string>
    <key>CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY</key><string>$CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY</string>
    <key>CLAUDE_HISTORY_RAG_SERVER_PSK</key><string>$CLAUDE_HISTORY_RAG_SERVER_PSK</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>ProcessType</key><string>Background</string>
  <key>AbandonProcessGroup</key><false/>
  <key>StandardOutPath</key><string>$STATE_DIR/launchd-stdout.log</string>
  <key>StandardErrorPath</key><string>$STATE_DIR/launchd-stderr.log</string>
</dict>
</plist>
EOF
chmod 600 "$PLIST_DEST"

"$LAUNCHCTL_BIN" bootout "gui/$UID/$LABEL" 2>/dev/null || true
"$LAUNCHCTL_BIN" bootstrap "gui/$UID" "$PLIST_DEST"

printf 'native launchd agent installed: %s\n' "$PLIST_DEST"
printf 'liveness: curl --fail http://127.0.0.1:4680/live\n'
printf 'readiness (authenticated): GET /health returns 503 until Spanner and the watcher are ready\n'
