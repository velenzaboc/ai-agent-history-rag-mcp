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

path_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

path_uid() {
  if stat -f '%u' "$1" >/dev/null 2>&1; then
    stat -f '%u' "$1"
  else
    stat -c '%u' "$1"
  fi
}

derive_workstation_identity() {
  local candidates=() cred_file="" email="" base="" project="${CLAUDE_HISTORY_RAG_SPANNER_PROJECT:-jeeves-486102}"
  shopt -s nullglob
  candidates=("$HOME/.config/no13-backup"/kopia-*.json)
  shopt -u nullglob
  if [[ "${#candidates[@]}" -eq 1 ]]; then
    cred_file="${candidates[0]}"
    chmod 600 "$cred_file" 2>/dev/null || true
    email="$(jq -r '(.client_email // (.service_account_impersonation_url // "" | capture("serviceAccounts/(?<email>[^:]+)").email)) // empty' "$cred_file" 2>/dev/null || true)"
    if [[ -n "$email" ]]; then
      printf '%s' "$email"
      return 0
    fi
    base="$(basename "$cred_file" .json)"
    if [[ "$base" =~ ^kopia-[a-z0-9-]+$ ]]; then
      printf '%s@%s.iam.gserviceaccount.com' "$base" "$project"
      return 0
    fi
  fi
  printf ''
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
DEFAULT_PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd -P)"
MAPPED_PROJECT_DIR="$HOME/Meridian/repos/ai-agent-history-rag-mcp"
if [[ -d "$MAPPED_PROJECT_DIR" && ! -L "$MAPPED_PROJECT_DIR" ]] && [[ "$(cd "$MAPPED_PROJECT_DIR" && pwd -P)" == "$DEFAULT_PROJECT_DIR" ]]; then
  DEFAULT_PROJECT_DIR="$MAPPED_PROJECT_DIR"
fi
PROJECT_DIR="${CLAUDE_HISTORY_RAG_CHECKOUT_ROOT:-$DEFAULT_PROJECT_DIR}"
require_absolute_clean "$PROJECT_DIR" "project directory"
[[ -d "$PROJECT_DIR" && ! -L "$PROJECT_DIR" ]] || fail "project directory must be an existing non-link directory"
if [[ "$PROJECT_DIR" != "$MAPPED_PROJECT_DIR" ]]; then
  PROJECT_DIR="$(cd "$PROJECT_DIR" && pwd -P)"
fi

LABEL="com.ai-agent-history-rag.daemon"
READ_ONLY="${CLAUDE_HISTORY_RAG_READ_ONLY:-false}"
case "$READ_ONLY" in true) LABEL="com.ai-agent-history-rag.search" ;; false) ;; *) fail "read-only must be true or false" ;; esac
RESET_FORWARD_CUTOFF="${CLAUDE_HISTORY_RAG_RESET_FORWARD_CUTOFF:-false}"
case "$RESET_FORWARD_CUTOFF" in true|false) ;; *) fail "reset forward cutoff must be true or false" ;; esac
LAUNCHCTL_BIN="${LAUNCHCTL_BIN:-launchctl}"
STATE_DIR="${CLAUDE_HISTORY_RAG_STATE_DIR:-$HOME/.claude-history-rag}"
if [[ "$READ_ONLY" == true ]]; then STATE_DIR="${CLAUDE_HISTORY_RAG_STATE_DIR:-$HOME/.claude-history-rag-native-search}"; fi
CONFIG_DIR="$HOME/.config/ai-agent-history-rag"
CONFIG_PATH="$CONFIG_DIR/history-ragd.json"
if [[ "$READ_ONLY" == true ]]; then CONFIG_PATH="$CONFIG_DIR/history-ragd-search.json"; fi
PLIST_DIR="$HOME/Library/LaunchAgents"
PLIST_DEST="$PLIST_DIR/$LABEL.plist"
HISTORY_RAGD_BIN="${HISTORY_RAGD_BIN:-$PROJECT_DIR/bin/history-ragd}"

require_absolute_clean "$STATE_DIR" "state directory"
require_absolute_clean "$CONFIG_DIR" "config directory"
require_absolute_clean "$PLIST_DIR" "LaunchAgents directory"
require_absolute_clean "$HISTORY_RAGD_BIN" "history-ragd binary"
[[ -x "$HISTORY_RAGD_BIN" && ! -L "$HISTORY_RAGD_BIN" ]] || fail "history-ragd binary must be an executable non-link file"
if [[ "$PROJECT_DIR" != "$MAPPED_PROJECT_DIR" || "$HISTORY_RAGD_BIN" != "$PROJECT_DIR"/* ]]; then
  BIN_PARENT="$(cd "$(dirname "$HISTORY_RAGD_BIN")" && pwd -P)"
  HISTORY_RAGD_BIN="$BIN_PARENT/$(basename "$HISTORY_RAGD_BIN")"
fi
case "$HISTORY_RAGD_BIN" in "$PROJECT_DIR"/*) ;; *) fail "history-ragd binary must be beneath the project directory" ;; esac

SERVICE_HOME="$HOME"
if [[ -n "${CLAUDE_HISTORY_RAG_CREDENTIAL_HOME:-}" ]]; then
  require_safe_value CLAUDE_HISTORY_RAG_CREDENTIAL_HOME
  require_absolute_clean "$CLAUDE_HISTORY_RAG_CREDENTIAL_HOME" "credential home"
  [[ -d "$CLAUDE_HISTORY_RAG_CREDENTIAL_HOME" && ! -L "$CLAUDE_HISTORY_RAG_CREDENTIAL_HOME" ]] || fail "credential home must be an existing non-link directory"
  SERVICE_HOME="$(cd "$CLAUDE_HISTORY_RAG_CREDENTIAL_HOME" && pwd -P)"
  [[ "$(path_uid "$SERVICE_HOME")" == "$(id -u)" ]] || fail "credential home must be owned by the current user"
  [[ "$(path_mode "$SERVICE_HOME")" == "700" ]] || fail "credential home must have mode 0700"
  CREDENTIAL_ADC="$SERVICE_HOME/.config/gcloud/application_default_credentials.json"
  [[ -f "$CREDENTIAL_ADC" && ! -L "$CREDENTIAL_ADC" ]] || fail "credential home ADC must be a regular non-link file"
  [[ "$(path_uid "$CREDENTIAL_ADC")" == "$(id -u)" ]] || fail "credential home ADC must be owned by the current user"
  case "$(path_mode "$CREDENTIAL_ADC")" in
    600|400) ;;
    *) fail "credential home ADC must have mode 0600 or 0400" ;;
  esac
fi

# The production selector is deliberately closed: this launch agent has no
# local database, emulator, credential-file override, public bind, or auth-off arm.
PORT=4680
if [[ "$READ_ONLY" == true ]]; then PORT=4681; fi
CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT="${CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT:-production}"
CLAUDE_HISTORY_RAG_STORAGE_BACKEND="${CLAUDE_HISTORY_RAG_STORAGE_BACKEND:-spanner}"
CLAUDE_HISTORY_RAG_SPANNER_PROJECT="${CLAUDE_HISTORY_RAG_SPANNER_PROJECT:-jeeves-486102}"
CLAUDE_HISTORY_RAG_SPANNER_INSTANCE="${CLAUDE_HISTORY_RAG_SPANNER_INSTANCE:-jeeves-rg-spanner-prod-4d0e4c43}"
CLAUDE_HISTORY_RAG_SPANNER_DATABASE="${CLAUDE_HISTORY_RAG_SPANNER_DATABASE:-ai-agent-history-rag}"
CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE="${CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE:-spanner}"
CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID="${CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID:-ConversationEmbeddingModel}"
CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER="${CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER:-vertex}"
CLAUDE_HISTORY_RAG_EMBEDDING_MODEL="${CLAUDE_HISTORY_RAG_EMBEDDING_MODEL:-gemini-embedding-001}"
CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION="${CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION:-3072}"
CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST="${CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST:-127.0.0.1}"
CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT="${CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT:-$PORT}"
CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE="${CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE:-application_default}"
CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE="${CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE:-device_service_account}"
if [[ -z "${CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY:-}" ]]; then
  CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY="$(derive_workstation_identity)"
fi

require_exact CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT production
require_exact CLAUDE_HISTORY_RAG_STORAGE_BACKEND spanner
require_exact CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE spanner
require_exact CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID ConversationEmbeddingModel
require_exact CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER vertex
require_exact CLAUDE_HISTORY_RAG_EMBEDDING_MODEL gemini-embedding-001
require_exact CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION 3072
require_exact CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST 127.0.0.1
require_exact CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT "$PORT"
require_exact CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE application_default
case "${CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE:-}" in impersonated_service_account|device_service_account) ;; *) fail "unsupported credential profile" ;; esac
for required in \
  CLAUDE_HISTORY_RAG_SPANNER_PROJECT \
  CLAUDE_HISTORY_RAG_SPANNER_INSTANCE \
  CLAUDE_HISTORY_RAG_SPANNER_DATABASE \
  CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY \
  CLAUDE_HISTORY_RAG_SERVER_PSK; do
  require_safe_value "$required"
done
if [[ -n "${CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY:-}" ]]; then
  require_safe_value CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY
fi
for forbidden in GOOGLE_APPLICATION_CREDENTIALS CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE CLOUDSDK_CONFIG SPANNER_EMULATOR_HOST; do
  [[ -z "${!forbidden:-}" ]] || fail "$forbidden must be unset"
done

umask 077
install -d -m 700 "$STATE_DIR" "$CONFIG_DIR" "$PLIST_DIR"
for directory in "$STATE_DIR" "$CONFIG_DIR" "$PLIST_DIR"; do
  [[ -d "$directory" && ! -L "$directory" ]] || fail "managed directory must be a non-link directory"
done

CHATGPT_ROOT="${CLAUDE_HISTORY_RAG_CHATGPT_EXPORTS_PATH:-$STATE_DIR/imports/chatgpt}"
CLAUDE_APP_ROOT="${CLAUDE_HISTORY_RAG_CLAUDE_APP_EXPORTS_PATH:-$STATE_DIR/imports/claude-app}"
CLAUDE_ROOT="${CLAUDE_HISTORY_RAG_PROJECTS_PATH:-$HOME/.claude/projects}"
CODEX_ARCHIVED_ROOT="${CLAUDE_HISTORY_RAG_CODEX_ARCHIVED_SESSIONS_PATH:-$HOME/.codex/archived_sessions}"
CODEX_ROOT="${CLAUDE_HISTORY_RAG_CODEX_SESSIONS_PATH:-$HOME/.codex/sessions}"
ANTIGRAVITY_ROOT="${CLAUDE_HISTORY_RAG_ANTIGRAVITY_SESSIONS_PATH:-$HOME/.gemini/antigravity}"
GEMINI_ROOT="${CLAUDE_HISTORY_RAG_GEMINI_SESSIONS_PATH:-$HOME/.gemini/tmp}"
CLAUDE_PROFILE_ROOT="$HOME/.velenza/provider-profiles/claude"
CODEX_PROFILE_ROOT="$HOME/.velenza/provider-profiles/codex"

DEFAULT_WATCH_ROOTS=(
  "$CHATGPT_ROOT"
  "$CLAUDE_APP_ROOT"
  "$CLAUDE_ROOT"
  "$CODEX_ARCHIVED_ROOT"
  "$CODEX_ROOT"
  "$ANTIGRAVITY_ROOT"
  "$GEMINI_ROOT"
  "$CLAUDE_PROFILE_ROOT"
  "$CODEX_PROFILE_ROOT"
)

WATCH_ROOTS=()
if [[ "$READ_ONLY" == false ]]; then
  for root in "${DEFAULT_WATCH_ROOTS[@]}"; do
    require_absolute_clean "$root" "watch root"
    mkdir -p "$root"
    if [[ -L "$root" && -d "$root" ]]; then
      root="$(cd "$root" && pwd -P)"
    fi
    chmod 700 "$root"
    [[ -d "$root" && ! -L "$root" ]] || fail "watch root must be a non-link directory"
    WATCH_ROOTS+=("$root")
  done
  if [[ -n "${CLAUDE_HISTORY_RAG_EXTRA_WATCH_ROOTS:-}" ]]; then
    IFS=':' read -r -a EXTRA_ROOT_ITEMS <<< "$CLAUDE_HISTORY_RAG_EXTRA_WATCH_ROOTS"
    for extra_root in "${EXTRA_ROOT_ITEMS[@]}"; do
      [[ -n "$extra_root" ]] || continue
      require_absolute_clean "$extra_root" "extra watch root"
      if [[ ! -e "$extra_root" && ! -L "$extra_root" ]]; then
        mkdir -p "$extra_root"
        chmod 700 "$extra_root"
      fi
      [[ -d "$extra_root" && ! -L "$extra_root" ]] || fail "extra watch root must be a non-link directory"
      WATCH_ROOTS+=("$extra_root")
    done
  fi
  for os_root in \
    "$HOME/Library/Application Support/Claude/local-agent-mode-sessions" \
    "$HOME/Library/Application Support/Claude/claude-code-sessions" \
    "$HOME/Library/Application Support/Antigravity/User"; do
    if [[ -d "$os_root" && ! -L "$os_root" ]]; then
      WATCH_ROOTS+=("$os_root")
    fi
  done
fi

"$LAUNCHCTL_BIN" bootout "gui/$(id -u)/$LABEL" >/dev/null 2>&1 || true
if [[ "$RESET_FORWARD_CUTOFF" == true ]]; then
  rm -f "$STATE_DIR/forward.json"
fi

WATCH_ROOTS_JSON="[]"
if [[ "$READ_ONLY" == false ]]; then
  CANONICAL_ROOTS=()
  for root in "${WATCH_ROOTS[@]}"; do
    CANONICAL_ROOTS+=("$(cd "$root" && pwd -P)")
  done
  if [[ "$RESET_FORWARD_CUTOFF" == false && -f "$STATE_DIR/forward.json" ]]; then
    while IFS= read -r existing_root; do
      [[ -n "$existing_root" && -d "$existing_root" && ! -L "$existing_root" ]] || continue
      CANONICAL_ROOTS+=("$(cd "$existing_root" && pwd -P)")
    done < <(jq -r '.roots[]?.path // empty' "$STATE_DIR/forward.json" 2>/dev/null || true)
  fi
  WATCH_ROOTS_JSON="$(printf '%s\n' "${CANONICAL_ROOTS[@]}" | LC_ALL=C sort -u | jq -Rcn '[inputs | select(length > 0)]')"
fi

cat > "$CONFIG_PATH" <<EOF
{
  "state_dir": "$STATE_DIR",
  "listen": "127.0.0.1:$PORT",
  "read_only": $READ_ONLY,
  "container_mode": false,
  "watch_roots": $WATCH_ROOTS_JSON,
  "pid_file": "$STATE_DIR/daemon.pid",
  "auth_state_file": "$STATE_DIR/auth.json",
  "auth_enabled": true,
  "checkout_root": "$PROJECT_DIR",
  "executable": "$HISTORY_RAGD_BIN"
}
EOF
chmod 600 "$CONFIG_PATH"

BASE_IDENTITY_PLIST_ENTRY=""
if [[ -n "${CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY:-}" ]]; then
  BASE_IDENTITY_PLIST_ENTRY="    <key>CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY</key><string>$CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY</string>"$'\n'
fi

REAL_PROJECT_DIR="$(cd "$PROJECT_DIR" && pwd -P)"
PROGRAM_ARGS_XML="    <string>$HISTORY_RAGD_BIN</string>
    <string>supervise</string>
    <string>--config</string>
    <string>$CONFIG_PATH</string>"
if [[ "$PROJECT_DIR" == "$MAPPED_PROJECT_DIR" && "$REAL_PROJECT_DIR" == /Volumes/* && -x /opt/homebrew/bin/node ]]; then
  PROGRAM_ARGS_XML="    <string>/opt/homebrew/bin/node</string>
    <string>-e</string>
    <string>const {spawn}=require(&quot;child_process&quot;);const c=spawn(process.argv[1],process.argv.slice(2),{stdio:&quot;inherit&quot;});process.on(&quot;SIGTERM&quot;,()=>c.kill(&quot;SIGTERM&quot;));process.on(&quot;SIGINT&quot;,()=>c.kill(&quot;SIGINT&quot;));c.on(&quot;exit&quot;,(code,sig)=>process.exit(code!==null?code:1));</string>
    <string>$HISTORY_RAGD_BIN</string>
    <string>supervise</string>
    <string>--config</string>
    <string>$CONFIG_PATH</string>"
fi

cat > "$PLIST_DEST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
$PROGRAM_ARGS_XML
  </array>
  <key>WorkingDirectory</key><string>$PROJECT_DIR</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key><string>$SERVICE_HOME</string>
    <key>PATH</key><string>/usr/bin:/bin:/usr/sbin:/sbin</string>
    <key>CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT</key><string>$CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT</string>
    <key>CLAUDE_HISTORY_RAG_READ_ONLY</key><string>$READ_ONLY</string>
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
${BASE_IDENTITY_PLIST_ENTRY}    <key>CLAUDE_HISTORY_RAG_SERVER_PSK</key><string>$CLAUDE_HISTORY_RAG_SERVER_PSK</string>
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

"$LAUNCHCTL_BIN" bootstrap "gui/$UID" "$PLIST_DEST"

printf 'native launchd agent installed: %s\n' "$PLIST_DEST"
printf 'liveness: curl --fail http://127.0.0.1:%s/live\n' "$PORT"
printf 'readiness (authenticated): GET /health checks dependencies for the selected runtime mode\n'
