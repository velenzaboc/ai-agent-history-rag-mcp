#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
INSTALLER="$ROOT_DIR/scripts/install-systemd.sh"
UNINSTALLER="$ROOT_DIR/scripts/uninstall-systemd.sh"
UNIT_TEMPLATE="$ROOT_DIR/scripts/ai-agent-history-rag.service"
PASS_COUNT=0

pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1" >&2; exit 1; }
require_literal() { grep -Fq -- "$2" "$1" || fail "$3: missing $2"; }
require_absent() { grep -Eqi -- "$2" "$1" && fail "$3: forbidden $2" || true; }
file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

for file in "$INSTALLER" "$UNINSTALLER" "$UNIT_TEMPLATE"; do
  [[ -r "$file" ]] || fail "missing systemd file: $file"
done
bash -n "$INSTALLER" "$UNINSTALLER" "$ROOT_DIR/scripts/systemd-native.test.sh"

for literal in \
  'history-ragd supervise --config' \
  'Restart=on-failure' \
  'NoNewPrivileges=true' \
  'ProtectSystem=strict' \
  'ProtectHome=read-only' \
  'UMask=0077' \
  'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6'; do
  require_literal "$UNIT_TEMPLATE" "$literal" "native unit contract"
done
require_absent "$UNIT_TEMPLATE" '(^|[^[:alnum:]_])(python|uv)([^[:alnum:]_]|$)' "native unit contract"
require_absent "$INSTALLER" '(^|[^[:alnum:]_])(python|uv)([^[:alnum:]_]|$)' "native installer contract"
pass "unit selects the hardened native foreground daemon"

TMP_DIR="$(mktemp -d)"
TEST_PROJECT="$(mktemp -d "$ROOT_DIR/.systemd-native-test.XXXXXX")"
cleanup() { rm -rf -- "$TMP_DIR" "$TEST_PROJECT"; }
trap cleanup EXIT
TEST_HOME="$TMP_DIR/home"
TEST_BIN="$TEST_PROJECT/history-ragd"
TEST_SYSTEMCTL="$TMP_DIR/systemctl"
TEST_LOG="$TMP_DIR/systemctl.log"
mkdir -p "$TEST_HOME"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TEST_BIN"
chmod 700 "$TEST_BIN"
# shellcheck disable=SC2016 # The fake receives SYSTEMCTL_LOG at execution time.
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$*" >> "${SYSTEMCTL_LOG:?}"\n' > "$TEST_SYSTEMCTL"
chmod 700 "$TEST_SYSTEMCTL"

env \
  HOME="$TEST_HOME" \
  XDG_CONFIG_HOME="$TEST_HOME/.config" \
  SYSTEMCTL_BIN="$TEST_SYSTEMCTL" \
  SYSTEMCTL_LOG="$TEST_LOG" \
  HISTORY_RAGD_BIN="$TEST_BIN" \
  GOOGLE_APPLICATION_CREDENTIALS= \
  CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE= \
  CLOUDSDK_CONFIG= \
  SPANNER_EMULATOR_HOST= \
  CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT=production \
  CLAUDE_HISTORY_RAG_STORAGE_BACKEND=spanner \
  CLAUDE_HISTORY_RAG_SPANNER_PROJECT=fixture-project \
  CLAUDE_HISTORY_RAG_SPANNER_INSTANCE=fixture-instance \
  CLAUDE_HISTORY_RAG_SPANNER_DATABASE=fixture-database \
  CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE=spanner \
  CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID=ConversationEmbeddingModel \
  CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER=vertex \
  CLAUDE_HISTORY_RAG_EMBEDDING_MODEL=gemini-embedding-001 \
  CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION=3072 \
  CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST=127.0.0.1 \
  CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT=4680 \
  CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE=application_default \
  CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE=impersonated_service_account \
  CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY=fixture-agent@fixture-project.iam.gserviceaccount.com \
  CLAUDE_HISTORY_RAG_SERVER_PSK=fixture-secret \
  "$INSTALLER"

INSTALLED_UNIT="$TEST_HOME/.config/systemd/user/ai-agent-history-rag.service"
INSTALLED_CONFIG="$TEST_HOME/.config/ai-agent-history-rag/history-ragd.json"
INSTALLED_ENV="$TEST_HOME/.config/ai-agent-history-rag/history-ragd.env"
for file in "$INSTALLED_UNIT" "$INSTALLED_CONFIG" "$INSTALLED_ENV"; do
  [[ -f "$file" ]] || fail "installer did not write $file"
  [[ "$(file_mode "$file")" == 600 ]] || fail "installer did not protect $file"
done
require_literal "$INSTALLED_UNIT" "ExecStart=$TEST_BIN supervise --config $INSTALLED_CONFIG" "installed unit command"
require_literal "$INSTALLED_UNIT" 'ReadWritePaths='"$TEST_HOME/.claude-history-rag" "installed unit state boundary"
require_literal "$INSTALLED_CONFIG" '"listen": "127.0.0.1:4680"' "host loopback config"
require_literal "$INSTALLED_CONFIG" '"auth_enabled": true' "authenticated config"
require_literal "$INSTALLED_CONFIG" '"watch_roots": [' "six-source watcher config"
require_literal "$INSTALLED_ENV" 'CLAUDE_HISTORY_RAG_STORAGE_BACKEND=spanner' "Spanner-only environment"
require_absent "$INSTALLED_ENV" 'GOOGLE_APPLICATION_CREDENTIALS|SPANNER_EMULATOR_HOST' "environment overrides"
require_literal "$TEST_LOG" '--user daemon-reload' "systemctl reload"
require_literal "$TEST_LOG" '--user enable --now ai-agent-history-rag' "systemctl enable"
pass "installer writes protected loopback-only production unit and config"

if command -v systemd-analyze >/dev/null 2>&1; then
  systemd-analyze verify "$INSTALLED_UNIT"
  pass "systemd-analyze verifies generated user unit"
else
  pass "systemd-analyze unavailable; bounded static unit verification completed"
fi

env HOME="$TEST_HOME" XDG_CONFIG_HOME="$TEST_HOME/.config" SYSTEMCTL_BIN="$TEST_SYSTEMCTL" SYSTEMCTL_LOG="$TEST_LOG" "$UNINSTALLER"
[[ ! -e "$INSTALLED_UNIT" && ! -e "$INSTALLED_CONFIG" && ! -e "$INSTALLED_ENV" ]] || fail "uninstaller retained deployed unit material"
[[ -d "$TEST_HOME/.claude-history-rag" ]] || fail "uninstaller removed durable state without explicit request"
pass "uninstaller disables service and retains state by default"

printf 'PASS total=%d\n' "$PASS_COUNT"
