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

SERVICE_NAME="ai-agent-history-rag.service"
RESET_FORWARD_CUTOFF="${CLAUDE_HISTORY_RAG_RESET_FORWARD_CUTOFF:-false}"
case "$RESET_FORWARD_CUTOFF" in true|false) ;; *) fail "reset forward cutoff must be true or false" ;; esac
SYSTEMCTL_BIN="${SYSTEMCTL_BIN:-systemctl}"
STATE_DIR="${CLAUDE_HISTORY_RAG_STATE_DIR:-$HOME/.claude-history-rag}"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/ai-agent-history-rag"
CONFIG_PATH="$CONFIG_DIR/history-ragd.json"
ENV_PATH="$CONFIG_DIR/history-ragd.env"
SERVICE_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
SERVICE_DEST="$SERVICE_DIR/$SERVICE_NAME"
HISTORY_RAGD_BIN="${HISTORY_RAGD_BIN:-$PROJECT_DIR/bin/history-ragd}"

require_absolute_clean "$STATE_DIR" "state directory"
require_absolute_clean "$CONFIG_DIR" "config directory"
require_absolute_clean "$HISTORY_RAGD_BIN" "history-ragd binary"
[[ -x "$HISTORY_RAGD_BIN" && ! -L "$HISTORY_RAGD_BIN" ]] || fail "history-ragd binary must be an executable non-link file"
if [[ "$PROJECT_DIR" != "$MAPPED_PROJECT_DIR" || "$HISTORY_RAGD_BIN" != "$PROJECT_DIR"/* ]]; then
  BIN_PARENT="$(cd "$(dirname "$HISTORY_RAGD_BIN")" && pwd -P)"
  HISTORY_RAGD_BIN="$BIN_PARENT/$(basename "$HISTORY_RAGD_BIN")"
fi
case "$HISTORY_RAGD_BIN" in "$PROJECT_DIR"/*) ;; *) fail "history-ragd binary must be beneath the project directory" ;; esac

CREDENTIAL_HOME=""
if [[ -n "${CLAUDE_HISTORY_RAG_CREDENTIAL_HOME:-}" ]]; then
  require_safe_value CLAUDE_HISTORY_RAG_CREDENTIAL_HOME
  require_absolute_clean "$CLAUDE_HISTORY_RAG_CREDENTIAL_HOME" "credential home"
  [[ -d "$CLAUDE_HISTORY_RAG_CREDENTIAL_HOME" && ! -L "$CLAUDE_HISTORY_RAG_CREDENTIAL_HOME" ]] || fail "credential home must be an existing non-link directory"
  CREDENTIAL_HOME="$(cd "$CLAUDE_HISTORY_RAG_CREDENTIAL_HOME" && pwd -P)"
  [[ "$(path_uid "$CREDENTIAL_HOME")" == "$(id -u)" ]] || fail "credential home must be owned by the current user"
  [[ "$(path_mode "$CREDENTIAL_HOME")" == "700" ]] || fail "credential home must have mode 0700"
  CREDENTIAL_ADC="$CREDENTIAL_HOME/.config/gcloud/application_default_credentials.json"
  [[ -f "$CREDENTIAL_ADC" && ! -L "$CREDENTIAL_ADC" ]] || fail "credential home ADC must be a regular non-link file"
  [[ "$(path_uid "$CREDENTIAL_ADC")" == "$(id -u)" ]] || fail "credential home ADC must be owned by the current user"
  case "$(path_mode "$CREDENTIAL_ADC")" in
    600|400) ;;
    *) fail "credential home ADC must have mode 0600 or 0400" ;;
  esac
fi

# The production selector is intentionally closed. The service never offers a
# local database, emulator, or unauthenticated listener.
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
CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT="${CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT:-4680}"
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
require_exact CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT 4680
require_exact CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE application_default
require_safe_value CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE
for required in \
  CLAUDE_HISTORY_RAG_SPANNER_PROJECT \
  CLAUDE_HISTORY_RAG_SPANNER_INSTANCE \
  CLAUDE_HISTORY_RAG_SPANNER_DATABASE \
  CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY \
  CLAUDE_HISTORY_RAG_SERVER_PSK; do
  require_safe_value "$required"
done

for forbidden in CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE CLOUDSDK_CONFIG SPANNER_EMULATOR_HOST; do
  [[ -z "${!forbidden:-}" ]] || fail "$forbidden must be unset"
done

SERVICE_ACCOUNT_RE='^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z][a-z0-9-]{4,28}[a-z0-9]\.iam\.gserviceaccount\.com$'
WIF_AUDIENCE_RE='^//iam\.googleapis\.com/projects/[1-9][0-9]*/locations/global/workloadIdentityPools/[a-z][a-z0-9-]{2,31}/providers/[a-z][a-z0-9-]{2,31}$'

case "$CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE" in
  device_service_account)
    [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]] || fail "GOOGLE_APPLICATION_CREDENTIALS must be unset"
    ;;
  impersonated_service_account)
    [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]] || fail "GOOGLE_APPLICATION_CREDENTIALS must be unset"
    ADC_FILE="${CREDENTIAL_HOME:-$HOME}/.config/gcloud/application_default_credentials.json"
    if [[ -e "$ADC_FILE" || -L "$ADC_FILE" ]]; then
      [[ -f "$ADC_FILE" && ! -L "$ADC_FILE" ]] || fail "ADC file must be a regular non-link file"
      [[ "$(path_uid "$ADC_FILE")" == "$(id -u)" ]] || fail "ADC file must be owned by the current user"
      case "$(path_mode "$ADC_FILE")" in
        600|400) ;;
        *) fail "ADC file must have mode 0600 or 0400" ;;
      esac
    fi
    if [[ -n "${CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY:-}" ]]; then
      require_safe_value CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY
      [[ "$CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY" =~ $SERVICE_ACCOUNT_RE ]] || fail "CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY must be a service-account email"
    fi
    ;;
  workload_identity_federation)
    require_safe_value GOOGLE_APPLICATION_CREDENTIALS
    require_absolute_clean "$GOOGLE_APPLICATION_CREDENTIALS" "GOOGLE_APPLICATION_CREDENTIALS"
    [[ -f "$GOOGLE_APPLICATION_CREDENTIALS" && ! -L "$GOOGLE_APPLICATION_CREDENTIALS" ]] || fail "GOOGLE_APPLICATION_CREDENTIALS must be an existing regular non-link file"
    [[ "$(path_uid "$GOOGLE_APPLICATION_CREDENTIALS")" == "$(id -u)" ]] || fail "GOOGLE_APPLICATION_CREDENTIALS must be owned by the current user"
    case "$(path_mode "$GOOGLE_APPLICATION_CREDENTIALS")" in
      600|400) ;;
      *) fail "GOOGLE_APPLICATION_CREDENTIALS must have mode 0600 or 0400" ;;
    esac
    [[ "$(jq -r '.type // empty' "$GOOGLE_APPLICATION_CREDENTIALS" 2>/dev/null)" == "external_account" ]] || fail "GOOGLE_APPLICATION_CREDENTIALS must have type external_account"
    require_safe_value CLAUDE_HISTORY_RAG_CREDENTIALS_AUDIENCE
    [[ "$CLAUDE_HISTORY_RAG_CREDENTIALS_AUDIENCE" =~ $WIF_AUDIENCE_RE ]] || fail "CLAUDE_HISTORY_RAG_CREDENTIALS_AUDIENCE must match workload identity pool provider format"
    if [[ -n "${CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY:-}" ]]; then
      require_safe_value CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY
      [[ "$CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY" =~ $SERVICE_ACCOUNT_RE ]] || fail "CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY must be a service-account email"
      [[ "$CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY" != "$CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY" ]] || fail "CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY must be distinct from CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY"
    fi
    ;;
  *)
    fail "unsupported credential profile"
    ;;
esac

umask 077
install -d -m 700 "$STATE_DIR" "$CONFIG_DIR" "$SERVICE_DIR"
for directory in "$STATE_DIR" "$CONFIG_DIR" "$SERVICE_DIR"; do
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
  "$HOME/.config/Claude/local-agent-mode-sessions" \
  "$HOME/.config/Claude/claude-code-sessions" \
  "$HOME/.config/Antigravity/User"; do
  if [[ -d "$os_root" && ! -L "$os_root" ]]; then
    WATCH_ROOTS+=("$os_root")
  fi
done

"$SYSTEMCTL_BIN" --user stop "$SERVICE_NAME" >/dev/null 2>&1 || true
if [[ "$RESET_FORWARD_CUTOFF" == true ]]; then
  rm -f "$STATE_DIR/forward.json"
fi

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

cat > "$CONFIG_PATH" <<EOF
{
  "state_dir": "$STATE_DIR",
  "listen": "127.0.0.1:4680",
  "read_only": false,
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

{
  if [[ -n "$CREDENTIAL_HOME" ]]; then
    printf 'HOME=%s\n' "$CREDENTIAL_HOME"
  fi
  printf 'CLAUDE_HISTORY_RAG_READ_ONLY=false\n'
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
  if [[ "$CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE" == "workload_identity_federation" ]]; then
    printf 'GOOGLE_APPLICATION_CREDENTIALS=%s\n' "$GOOGLE_APPLICATION_CREDENTIALS"
    printf 'CLAUDE_HISTORY_RAG_CREDENTIALS_AUDIENCE=%s\n' "$CLAUDE_HISTORY_RAG_CREDENTIALS_AUDIENCE"
  fi
  if [[ -n "${CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY:-}" ]]; then
    printf 'CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY=%s\n' "$CLAUDE_HISTORY_RAG_CREDENTIALS_BASE_IDENTITY"
  fi
} > "$ENV_PATH"
chmod 600 "$ENV_PATH"

UNSET_ENV="CLOUDSDK_CONFIG CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE GOOGLE_API_CERTIFICATE_CONFIG GOOGLE_APPLICATION_CREDENTIALS"
if [[ "$CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE" == "workload_identity_federation" ]]; then
  UNSET_ENV="CLOUDSDK_CONFIG CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE GOOGLE_API_CERTIFICATE_CONFIG"
fi

cat > "$SERVICE_DEST" <<EOF
[Unit]
Description=AI Agent History RAG native daemon
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$PROJECT_DIR
UnsetEnvironment=$UNSET_ENV
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
