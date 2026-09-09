#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
INSTALLER="$ROOT_DIR/scripts/install-launchd.sh"
UNINSTALLER="$ROOT_DIR/scripts/uninstall-launchd.sh"
TEMPLATE="$ROOT_DIR/scripts/com.ai-agent-history-rag.daemon.plist.template"
PASS_COUNT=0

pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1" >&2; exit 1; }
require_literal() { grep -Fq -- "$2" "$1" || fail "$3: missing $2"; }
require_absent() { grep -Eqi -- "$2" "$1" && fail "$3: forbidden $2" || true; }
file_mode() { stat -f '%Lp' "$1"; }

for file in "$INSTALLER" "$UNINSTALLER" "$TEMPLATE"; do
  [[ -r "$file" ]] || fail "missing launchd file: $file"
done
bash -n "$INSTALLER" "$UNINSTALLER" "$ROOT_DIR/scripts/launchd-native.test.sh"
plutil -lint "$TEMPLATE" >/dev/null

for literal in \
  '<string>__HISTORY_RAGD_BIN__</string>' \
  '<string>supervise</string>' \
  '<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>' \
  '<key>ThrottleInterval</key><integer>5</integer>' \
  '<key>CLAUDE_HISTORY_RAG_STORAGE_BACKEND</key><string>spanner</string>' \
  '<key>CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST</key><string>127.0.0.1</string>'; do
  require_literal "$TEMPLATE" "$literal" "native launchd template"
done
require_absent "$TEMPLATE" '(^|[^[:alnum:]_])(python|uv)([^[:alnum:]_]|$)' "native launchd template"
require_absent "$TEMPLATE" 'GOOGLE_APPLICATION_CREDENTIALS|CLOUDSDK_CONFIG|SPANNER_EMULATOR_HOST' "native launchd template"
pass "template selects hardened native production daemon"

TMP_DIR="$(mktemp -d)"
TEST_PROJECT="$(mktemp -d "$ROOT_DIR/.launchd-native-test.XXXXXX")"
cleanup() { rm -rf -- "$TMP_DIR" "$TEST_PROJECT"; }
trap cleanup EXIT
TEST_HOME="$TMP_DIR/home"
TEST_BIN="$TEST_PROJECT/history-ragd"
TEST_LAUNCHCTL="$TMP_DIR/launchctl"
TEST_LOG="$TMP_DIR/launchctl.log"
mkdir -p "$TEST_HOME"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TEST_BIN"
chmod 700 "$TEST_BIN"
# shellcheck disable=SC2016 # The fake receives LAUNCHCTL_LOG at execution time.
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$*" >> "${LAUNCHCTL_LOG:?}"\n' > "$TEST_LAUNCHCTL"
chmod 700 "$TEST_LAUNCHCTL"

env \
  HOME="$TEST_HOME" \
  LAUNCHCTL_BIN="$TEST_LAUNCHCTL" \
  LAUNCHCTL_LOG="$TEST_LOG" \
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

INSTALLED_PLIST="$TEST_HOME/Library/LaunchAgents/com.ai-agent-history-rag.daemon.plist"
INSTALLED_CONFIG="$TEST_HOME/.config/ai-agent-history-rag/history-ragd.json"
for file in "$INSTALLED_PLIST" "$INSTALLED_CONFIG"; do
  [[ -f "$file" ]] || fail "installer did not write $file"
  [[ "$(file_mode "$file")" == 600 ]] || fail "installer did not protect $file"
done
plutil -lint "$INSTALLED_PLIST" >/dev/null
require_literal "$INSTALLED_PLIST" "<string>$TEST_BIN</string>" "installed native binary"
require_literal "$INSTALLED_PLIST" '<string>supervise</string>' "installed lifecycle"
require_literal "$INSTALLED_PLIST" '<key>CLAUDE_HISTORY_RAG_SERVER_PSK</key><string>fixture-secret</string>' "protected authentication"
require_literal "$INSTALLED_CONFIG" '"listen": "127.0.0.1:4680"' "host loopback config"
require_literal "$INSTALLED_CONFIG" '"auth_enabled": true' "authenticated config"
require_literal "$INSTALLED_CONFIG" '"watch_roots": [' "six-source watcher config"
require_absent "$INSTALLED_PLIST" 'GOOGLE_APPLICATION_CREDENTIALS|CLOUDSDK_CONFIG|SPANNER_EMULATOR_HOST' "environment overrides"
require_literal "$TEST_LOG" "bootstrap gui/$UID $INSTALLED_PLIST" "launchctl bootstrap"
pass "installer writes protected native loopback LaunchAgent"

env HOME="$TEST_HOME" LAUNCHCTL_BIN="$TEST_LAUNCHCTL" LAUNCHCTL_LOG="$TEST_LOG" "$UNINSTALLER"
[[ ! -e "$INSTALLED_PLIST" && ! -e "$INSTALLED_CONFIG" ]] || fail "uninstaller retained deployed agent material"
[[ -d "$TEST_HOME/.claude-history-rag" ]] || fail "uninstaller removed durable state without explicit request"
pass "uninstaller boots out agent and retains state by default"

printf 'PASS total=%d\n' "$PASS_COUNT"
