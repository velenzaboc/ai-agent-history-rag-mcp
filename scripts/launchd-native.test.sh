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
file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}
plist_lint() {
  if command -v plutil >/dev/null 2>&1; then
    plutil -lint "$1" >/dev/null
  else
    python3 -c 'import plistlib, sys; plistlib.load(open(sys.argv[1], "rb"))' "$1"
  fi
}
plist_to_json() {
  if command -v plutil >/dev/null 2>&1; then
    plutil -convert json -o - "$1"
  else
    python3 -c 'import json, plistlib, sys; print(json.dumps(plistlib.load(open(sys.argv[1], "rb"))))' "$1"
  fi
}

for file in "$INSTALLER" "$UNINSTALLER" "$TEMPLATE"; do
  [[ -r "$file" ]] || fail "missing launchd file: $file"
done
bash -n "$INSTALLER" "$UNINSTALLER" "$ROOT_DIR/scripts/launchd-native.test.sh"
plist_lint "$TEMPLATE"

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

TMP_DIR="$(cd "$(mktemp -d)" && pwd -P)"
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
# The fake validates the generated ingestion config at launchctl bootstrap so
# root creation and loopback/auth policy are proven before process start.
cat > "$TEST_LAUNCHCTL" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${LAUNCHCTL_LOG:?}"
if [[ "$*" == *"bootstrap "* && "$*" == *"com.ai-agent-history-rag.daemon.plist"* && "${SKIP_DEFAULT_BOOTSTRAP_CHECK:-}" != "1" ]]; then
  jq -e --arg home "${EXPECTED_HOME:?}" '
    .listen == "127.0.0.1:4680" and
    .auth_enabled == true and
    .watch_roots == [
      ($home + "/.claude-history-rag/imports/chatgpt"),
      ($home + "/.claude-history-rag/imports/claude-app"),
      ($home + "/.claude/projects"),
      ($home + "/.codex/archived_sessions"),
      ($home + "/.codex/sessions"),
      ($home + "/.gemini/antigravity"),
      ($home + "/.gemini/tmp"),
      ($home + "/.velenza/provider-profiles/claude"),
      ($home + "/.velenza/provider-profiles/codex")
    ]
  ' "${EXPECTED_CONFIG:?}" >/dev/null
  while IFS= read -r root; do
    [[ -d "$root" && ! -L "$root" ]]
  done < <(jq -r '.watch_roots[]' "${EXPECTED_CONFIG:?}")
fi
EOF
chmod 700 "$TEST_LAUNCHCTL"

env \
  HOME="$TEST_HOME" \
  LAUNCHCTL_BIN="$TEST_LAUNCHCTL" \
  LAUNCHCTL_LOG="$TEST_LOG" \
  EXPECTED_HOME="$TEST_HOME" \
  EXPECTED_CONFIG="$TEST_HOME/.config/ai-agent-history-rag/history-ragd.json" \
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
plist_lint "$INSTALLED_PLIST"
require_literal "$INSTALLED_PLIST" "<string>$TEST_BIN</string>" "installed native binary"
require_literal "$INSTALLED_PLIST" '<string>supervise</string>' "installed lifecycle"
require_literal "$INSTALLED_PLIST" '<key>CLAUDE_HISTORY_RAG_SERVER_PSK</key><string>fixture-secret</string>' "protected authentication"
require_literal "$INSTALLED_CONFIG" '"listen": "127.0.0.1:4680"' "host loopback config"
require_literal "$INSTALLED_CONFIG" '"auth_enabled": true' "authenticated config"
require_literal "$INSTALLED_CONFIG" '"read_only": false' "collector read_only config"
jq -e --arg home "$TEST_HOME" '
  .listen == "127.0.0.1:4680" and
  .auth_enabled == true and
  .read_only == false and
  .watch_roots == [
    ($home + "/.claude-history-rag/imports/chatgpt"),
    ($home + "/.claude-history-rag/imports/claude-app"),
    ($home + "/.claude/projects"),
    ($home + "/.codex/archived_sessions"),
    ($home + "/.codex/sessions"),
    ($home + "/.gemini/antigravity"),
    ($home + "/.gemini/tmp"),
    ($home + "/.velenza/provider-profiles/claude"),
    ($home + "/.velenza/provider-profiles/codex")
  ]
' "$INSTALLED_CONFIG" >/dev/null || fail "generated config does not carry the exact nine sorted loopback roots"
while IFS= read -r root; do
  [[ -d "$root" && ! -L "$root" ]] || fail "watch root was not created before launch: $root"
done < <(jq -r '.watch_roots[]' "$INSTALLED_CONFIG")
require_absent "$INSTALLED_PLIST" 'GOOGLE_APPLICATION_CREDENTIALS|CLOUDSDK_CONFIG|SPANNER_EMULATOR_HOST' "environment overrides"
require_literal "$TEST_LOG" "bootstrap gui/$UID $INSTALLED_PLIST" "launchctl bootstrap"
pass "installer writes protected native loopback LaunchAgent with nine pre-created sorted roots"

# Exercise default workstation derivation (only CLAUDE_HISTORY_RAG_SERVER_PSK required)
# and mapped ~/Meridian checkout preservation.
mkdir -p -m 700 "$TEST_HOME/.config/no13-backup"
printf '{"type":"service_account"}\n' > "$TEST_HOME/.config/no13-backup/kopia-macmini.json"
chmod 600 "$TEST_HOME/.config/no13-backup/kopia-macmini.json"
MAPPED_STORAGE="$TMP_DIR/meridian-storage"
mkdir -p "$MAPPED_STORAGE/repos/ai-agent-history-rag-mcp/bin"
cp -R "$ROOT_DIR/scripts" "$MAPPED_STORAGE/repos/ai-agent-history-rag-mcp/scripts"
cp "$TEST_BIN" "$MAPPED_STORAGE/repos/ai-agent-history-rag-mcp/bin/history-ragd"
chmod 700 "$MAPPED_STORAGE/repos/ai-agent-history-rag-mcp/bin/history-ragd"
ln -sfn "$MAPPED_STORAGE" "$TEST_HOME/Meridian"
env \
  HOME="$TEST_HOME" \
  LAUNCHCTL_BIN="$TEST_LAUNCHCTL" \
  LAUNCHCTL_LOG="$TEST_LOG" \
  EXPECTED_HOME="$TEST_HOME" \
  EXPECTED_CONFIG="$INSTALLED_CONFIG" \
  GOOGLE_APPLICATION_CREDENTIALS= \
  CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE= \
  CLOUDSDK_CONFIG= \
  SPANNER_EMULATOR_HOST= \
  CLAUDE_HISTORY_RAG_SERVER_PSK=fixture-secret \
  "$TEST_HOME/Meridian/repos/ai-agent-history-rag-mcp/scripts/install-launchd.sh"
require_literal "$INSTALLED_PLIST" '<key>CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE</key><string>device_service_account</string>' "default device_service_account profile"
require_literal "$INSTALLED_PLIST" "<key>WorkingDirectory</key><string>$TEST_HOME/Meridian/repos/ai-agent-history-rag-mcp</string>" "mapped ~/Meridian WorkingDirectory"
require_literal "$INSTALLED_PLIST" "<string>$TEST_HOME/Meridian/repos/ai-agent-history-rag-mcp/bin/history-ragd</string>" "mapped ~/Meridian binary path"
jq -e --arg checkout "$TEST_HOME/Meridian/repos/ai-agent-history-rag-mcp" '.checkout_root == $checkout and .read_only == false' "$INSTALLED_CONFIG" >/dev/null || fail "default installer did not preserve mapped ~/Meridian checkout_root"
pass "installer defaults to device_service_account and preserves mapped ~/Meridian checkout paths"

# Exercise CLAUDE_HISTORY_RAG_CREDENTIAL_HOME, CLAUDE_HISTORY_RAG_EXTRA_WATCH_ROOTS,
# CLAUDE_HISTORY_RAG_CHECKOUT_ROOT, and CLAUDE_HISTORY_RAG_RESET_FORWARD_CUTOFF=true.
CRED_HOME="$TMP_DIR/cred-home"
mkdir -p -m 700 "$CRED_HOME" "$CRED_HOME/.config/gcloud"
chmod 700 "$CRED_HOME"
printf '{"type":"impersonated_service_account"}\n' > "$CRED_HOME/.config/gcloud/application_default_credentials.json"
chmod 600 "$CRED_HOME/.config/gcloud/application_default_credentials.json"
EXTRA_A="$TMP_DIR/extra-codex/sessions"
EXTRA_B="$TMP_DIR/extra-codex/archived_sessions"
RUNTIME_CHECKOUT="$TMP_DIR/runtime-checkout"
mkdir -p "$RUNTIME_CHECKOUT/bin"
cp "$TEST_BIN" "$RUNTIME_CHECKOUT/bin/history-ragd"
chmod 700 "$RUNTIME_CHECKOUT/bin/history-ragd"
printf '{"cutoff":"stale"}\n' > "$TEST_HOME/.claude-history-rag/forward.json"

env \
  HOME="$TEST_HOME" \
  SKIP_DEFAULT_BOOTSTRAP_CHECK=1 \
  LAUNCHCTL_BIN="$TEST_LAUNCHCTL" \
  LAUNCHCTL_LOG="$TEST_LOG" \
  EXPECTED_HOME="$TEST_HOME" \
  EXPECTED_CONFIG="$INSTALLED_CONFIG" \
  CLAUDE_HISTORY_RAG_CHECKOUT_ROOT="$RUNTIME_CHECKOUT" \
  CLAUDE_HISTORY_RAG_CREDENTIAL_HOME="$CRED_HOME" \
  CLAUDE_HISTORY_RAG_EXTRA_WATCH_ROOTS="$EXTRA_A:$EXTRA_B" \
  CLAUDE_HISTORY_RAG_RESET_FORWARD_CUTOFF=true \
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

[[ ! -e "$TEST_HOME/.claude-history-rag/forward.json" ]] || fail "installer did not reset forward.json when requested"
require_literal "$INSTALLED_PLIST" "<key>HOME</key><string>$CRED_HOME</string>" "isolated credential HOME in plist"
require_literal "$INSTALLED_PLIST" "<key>WorkingDirectory</key><string>$RUNTIME_CHECKOUT</string>" "checkout root in plist"
jq -e --arg home "$TEST_HOME" --arg extra_a "$EXTRA_A" --arg extra_b "$EXTRA_B" --arg checkout "$RUNTIME_CHECKOUT" '
  .state_dir == ($home + "/.claude-history-rag") and
  .checkout_root == $checkout and
  (.watch_roots | index($extra_a) != null) and
  (.watch_roots | index($extra_b) != null) and
  (.watch_roots | index($home + "/.claude/projects") != null)
' "$INSTALLED_CONFIG" >/dev/null || fail "extra watch roots, credential home, or checkout root failed alignment"
pass "installer supports credential home, extra watch roots, checkout root, and forward cutoff reset"

legacy_hash="$(shasum -a 256 "$INSTALLED_PLIST")"
READ_ENV=()
while IFS=$'\t' read -r key value; do READ_ENV+=("$key=$value"); done < <(plist_to_json "$INSTALLED_PLIST" | jq -r '.EnvironmentVariables | to_entries[] | [.key,.value] | @tsv')
: > "$TEST_LOG"
env "${READ_ENV[@]}" HOME="$TEST_HOME" LAUNCHCTL_BIN="$TEST_LAUNCHCTL" LAUNCHCTL_LOG="$TEST_LOG" HISTORY_RAGD_BIN="$RUNTIME_CHECKOUT/bin/history-ragd" CLAUDE_HISTORY_RAG_CHECKOUT_ROOT="$RUNTIME_CHECKOUT" \
 GOOGLE_APPLICATION_CREDENTIALS= CLOUDSDK_CONFIG= CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE= SPANNER_EMULATOR_HOST= \
 CLAUDE_HISTORY_RAG_READ_ONLY=true CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT=4681 CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE=device_service_account "$INSTALLER"
SEARCH_CONFIG="$TEST_HOME/.config/ai-agent-history-rag/history-ragd-search.json"
jq -e '.read_only == true and .watch_roots == [] and .listen == "127.0.0.1:4681"' "$SEARCH_CONFIG" >/dev/null || fail "search mode can start ingestion"
[[ "$(shasum -a 256 "$INSTALLED_PLIST")" == "$legacy_hash" ]] || fail "search installer changed ingestion agent"
require_absent "$TEST_LOG" 'com.ai-agent-history-rag.daemon' "search installer touches ingestion label"
env HOME="$TEST_HOME" LAUNCHCTL_BIN="$TEST_LAUNCHCTL" LAUNCHCTL_LOG="$TEST_LOG" CLAUDE_HISTORY_RAG_READ_ONLY=true "$UNINSTALLER"
[[ -f "$INSTALLED_PLIST" && ! -f "$SEARCH_CONFIG" ]] || fail "search uninstall touched ingestion"
pass "read-only install and uninstall preserve ingestion agent"

env HOME="$TEST_HOME" LAUNCHCTL_BIN="$TEST_LAUNCHCTL" LAUNCHCTL_LOG="$TEST_LOG" "$UNINSTALLER"
[[ ! -e "$INSTALLED_PLIST" && ! -e "$INSTALLED_CONFIG" ]] || fail "uninstaller retained deployed agent material"
[[ -d "$TEST_HOME/.claude-history-rag" ]] || fail "uninstaller removed durable state without explicit request"
pass "uninstaller boots out agent and retains state by default"

printf 'PASS total=%d\n' "$PASS_COUNT"
