#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MCP_SCRIPT="${MCP_SCRIPT:-$ROOT_DIR/scripts/history-rag-mcp-native.sh}"
PASS_COUNT=0
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf 'PASS %s\n' "$1"
}

fail() {
  printf 'FAIL %s\n' "$1" >&2
  exit 1
}

assert_failure() {
  local label="$1"
  shift
  if "$@" >"$TMP_ROOT/stdout" 2>"$TMP_ROOT/stderr"; then
    fail "$label unexpectedly succeeded"
  fi
  pass "$label"
}

assert_success() {
  local label="$1"
  shift
  if ! "$@" >"$TMP_ROOT/stdout" 2>"$TMP_ROOT/stderr"; then
    fail "$label failed: $(tr '\n' ' ' <"$TMP_ROOT/stderr")"
  fi
  pass "$label"
}

windows_acl_principal() {
  if [[ -n "${USERDOMAIN-}" && -n "${USERNAME-}" ]]; then
    printf '%s\\%s' "$USERDOMAIN" "$USERNAME"
    return 0
  fi
  powershell.exe -NoProfile -NonInteractive -Command \
    '[Environment]::UserDomainName + "\\" + [Environment]::UserName' \
    | tr -d '\r\n'
}

restrict_file_acl() {
  local path="$1"
  chmod 600 "$path"
  if [[ -n "${MSYSTEM-}" ]]; then
    local windows_path principal
    windows_path="$(cygpath -w -- "$path")"
    principal="$(windows_acl_principal)"
    icacls.exe "$windows_path" /inheritance:r /grant:r "${principal}:(F)" >/dev/null
  fi
}

loosen_file_acl() {
  local path="$1"
  if [[ -n "${MSYSTEM-}" ]]; then
    local windows_path principal
    windows_path="$(cygpath -w -- "$path")"
    principal="$(windows_acl_principal)"
    icacls.exe "$windows_path" /inheritance:r /grant:r "${principal}:(F)" 'BUILTIN\Users:(R)' >/dev/null
  else
    chmod 644 "$path"
  fi
}

write_adc() {
  local path="$1"
  local source_type="$2"
  local include_private="$3"
  local delegates="$4"
  local target_identity="$5"
  jq -n \
    --arg source_type "$source_type" \
    --arg target_identity "$target_identity" \
    --argjson include_private "$include_private" \
    --argjson delegates "$delegates" '
      {
        type: "impersonated_service_account",
        source_credentials: (
          if $source_type == "authorized_user" then
            {type: "authorized_user", client_id: "synthetic-client", client_secret: "synthetic-secret", refresh_token: "synthetic-refresh"}
          else
            {
              type: "service_account",
              project_id: "sample-project",
              private_key_id: "synthetic-key-id",
              private_key: "fixture-private-key-material",
              client_email: "device-source@sample-project.iam.gserviceaccount.com",
              token_uri: "https://oauth2.googleapis.com/token",
              client_id: "synthetic-client",
              auth_uri: "https://accounts.google.com/o/oauth2/auth",
              auth_provider_x509_cert_url: "https://www.googleapis.com/oauth2/v1/certs",
              client_x509_cert_url: "https://www.googleapis.com/robot/v1/metadata/x509/device-source%40sample-project.iam.gserviceaccount.com",
              universe_domain: "googleapis.com"
            }
          end
        ),
        service_account_impersonation_url: (
          "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" +
          $target_identity + ":generateAccessToken"
        ),
        delegates: $delegates
      }
      | if $include_private then .metadata = {nested: [{private_key: "DO_NOT_PRINT_PRIVATE_FIXTURE"}]} else . end
    ' >"$path"
  restrict_file_acl "$path"
}

fixture_env() {
  local fixture="$1"
  local adc_path="$2"
  local profile="$3"
  local identity="history-rag-operator@sample-project.iam.gserviceaccount.com"
  mkdir -p "$fixture/home/.claude/projects" \
    "$fixture/home/.codex/sessions" \
    "$fixture/home/.gemini/tmp" \
    "$fixture/home/.gemini/antigravity" \
    "$fixture/home/.claude-history-rag/imports/chatgpt" \
    "$fixture/home/.claude-history-rag/imports/claude-app"
  FIXTURE_ENV=(
    "HOME=$fixture/home"
    "PATH=$PATH"
    "CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT=production"
    "CLAUDE_HISTORY_RAG_STORAGE_BACKEND=spanner"
    "CLAUDE_HISTORY_RAG_SPANNER_PROJECT=sample-project"
    "CLAUDE_HISTORY_RAG_SPANNER_INSTANCE=sample-instance"
    "CLAUDE_HISTORY_RAG_SPANNER_DATABASE=sample-database"
    "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE=spanner"
    "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID=ConversationEmbeddingModel"
    "CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER=vertex"
    "CLAUDE_HISTORY_RAG_EMBEDDING_MODEL=gemini-embedding-001"
    "CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION=3072"
    "CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST=127.0.0.1"
    "CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT=4680"
    "CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE=application_default"
    "CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE=$profile"
    "CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY=$identity"
    "CLAUDE_HISTORY_RAG_AUTH_ENABLED=false"
  )
  if [[ -n "$adc_path" ]]; then
    FIXTURE_ENV+=("GOOGLE_APPLICATION_CREDENTIALS=$adc_path")
  fi
}

# shellcheck disable=SC2120
run_fixture() {
  env -i "${FIXTURE_ENV[@]}" "$MCP_SCRIPT" "$@"
}

[[ -x "$MCP_SCRIPT" ]] || fail "native MCP script is missing or not executable"

identity="history-rag-operator@sample-project.iam.gserviceaccount.com"

valid="$TMP_ROOT/valid"
mkdir -p "$valid"
write_adc "$valid/adc.json" authorized_user false '[]' "$identity"
fixture_env "$valid" "$valid/adc.json" impersonated_service_account
assert_success "valid exact-target user ADC" run_fixture --validate-only

device="$TMP_ROOT/device-service-account"
mkdir -p "$device"
write_adc "$device/adc.json" service_account false '[]' "$identity"
fixture_env "$device" "$device/adc.json" impersonated_service_account
assert_success "valid device-sourced ADC" run_fixture --validate-only

production_shape_mutations=(
  'CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT|development'
  'CLAUDE_HISTORY_RAG_STORAGE_BACKEND|unsupported-store'
  'CLAUDE_HISTORY_RAG_SPANNER_PROJECT|'
  'CLAUDE_HISTORY_RAG_SPANNER_INSTANCE|'
  'CLAUDE_HISTORY_RAG_SPANNER_DATABASE|'
  'CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE|app'
  'CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID|OtherModel'
  'CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER|openai'
  'CLAUDE_HISTORY_RAG_EMBEDDING_MODEL|other-model'
  'CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION|768'
  'CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST|0.0.0.0'
  'CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT|4681'
  'CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE|credential_file'
  'CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY|not-an-identity'
  'CLAUDE_HISTORY_RAG_PROJECTS_PATH|/tmp/not-the-production-root'
  'CLAUDE_HISTORY_RAG_AUTH_ENABLED|maybe'
)
for mutation in "${production_shape_mutations[@]}"; do
  fixture_env "$valid" "$valid/adc.json" impersonated_service_account
  name="${mutation%%|*}"
  bad_value="${mutation#*|}"
  FIXTURE_ENV+=("$name=$bad_value")
  assert_failure "production shape rejects $name drift" run_fixture --validate-only
done

nested="$TMP_ROOT/nested-service-account"
mkdir -p "$nested"
write_adc "$nested/adc.json" service_account false '[]' "$identity"
fixture_env "$nested" "$nested/adc.json" impersonated_service_account
assert_success "nested service-account source accepted" run_fixture --validate-only

unsupported_source="$TMP_ROOT/unsupported-source"
mkdir -p "$unsupported_source"
write_adc "$unsupported_source/adc.json" authorized_user false '[]' "$identity"
jq '.source_credentials.type = "external_account"' "$unsupported_source/adc.json" >"$unsupported_source/rewritten.json"
mv "$unsupported_source/rewritten.json" "$unsupported_source/adc.json"
restrict_file_acl "$unsupported_source/adc.json"
fixture_env "$unsupported_source" "$unsupported_source/adc.json" impersonated_service_account
assert_failure "unsupported nested source type rejected" run_fixture --validate-only

private="$TMP_ROOT/private-marker"
mkdir -p "$private"
write_adc "$private/adc.json" authorized_user true '[]' "$identity"
fixture_env "$private" "$private/adc.json" impersonated_service_account
assert_failure "private-key marker rejected at arbitrary depth" run_fixture --validate-only
if grep -Eq 'DO_NOT_PRINT_PRIVATE_FIXTURE' "$TMP_ROOT/stdout" "$TMP_ROOT/stderr"; then
  fail "private fixture value leaked in validator output"
fi
pass "validator output is secret-safe"

delegates="$TMP_ROOT/delegates"
mkdir -p "$delegates"
write_adc "$delegates/adc.json" authorized_user false '["delegate@example.invalid"]' "$identity"
fixture_env "$delegates" "$delegates/adc.json" impersonated_service_account
assert_failure "delegated impersonation rejected" run_fixture --validate-only

wrong_target="$TMP_ROOT/wrong-target"
mkdir -p "$wrong_target"
write_adc "$wrong_target/adc.json" authorized_user false '[]' "other-operator@sample-project.iam.gserviceaccount.com"
fixture_env "$wrong_target" "$wrong_target/adc.json" impersonated_service_account
assert_failure "wrong impersonation target rejected" run_fixture --validate-only

loose="$TMP_ROOT/loose-mode"
mkdir -p "$loose"
write_adc "$loose/adc.json" authorized_user false '[]' "$identity"
loosen_file_acl "$loose/adc.json"
fixture_env "$loose" "$loose/adc.json" impersonated_service_account
assert_failure "group-readable ADC rejected" run_fixture --validate-only

symlinked="$TMP_ROOT/symlink"
mkdir -p "$symlinked"
write_adc "$symlinked/real.json" authorized_user false '[]' "$identity"
ln -s "$symlinked/real.json" "$symlinked/adc.json"
fixture_env "$symlinked" "$symlinked/adc.json" impersonated_service_account
assert_failure "symlink ADC rejected" run_fixture --validate-only

attached="$TMP_ROOT/attached"
mkdir -p "$attached"
fixture_env "$attached" "" attached_service_account
assert_success "attached workload identity without file carrier" run_fixture --validate-only
FIXTURE_ENV+=("GOOGLE_APPLICATION_CREDENTIALS=$valid/adc.json")
assert_failure "attached workload rejects file-selected ADC" run_fixture --validate-only

override="$TMP_ROOT/override"
mkdir -p "$override"
write_adc "$override/adc.json" authorized_user false '[]' "$identity"
fixture_env "$override" "$override/adc.json" impersonated_service_account
FIXTURE_ENV+=("CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE=$override/adc.json")
assert_failure "gcloud credential-file override rejected" run_fixture --validate-only

network="$TMP_ROOT/network-order"
mkdir -p "$network/bin"
printf '#!/usr/bin/env bash\nprintf invoked >%q\nprintf "{\\"ok\\":true}\\n200"\n' "$network/curl-invoked" >"$network/bin/curl"
chmod +x "$network/bin/curl"
write_adc "$network/adc.json" service_account true '[]' "$identity"
fixture_env "$network" "$network/adc.json" impersonated_service_account
FIXTURE_ENV[1]="PATH=$network/bin:$PATH"
# shellcheck disable=SC2119
if printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | run_fixture >"$TMP_ROOT/stdout" 2>"$TMP_ROOT/stderr"; then
  fail "invalid contract unexpectedly entered MCP loop"
fi
[[ ! -e "$network/curl-invoked" ]] || fail "network client ran before production gate"
pass "production gate precedes network client"

protocol="$TMP_ROOT/protocol"
mkdir -p "$protocol/bin"
# shellcheck disable=SC2016
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$*" >>%q\nheader_file=""\nprevious=""\nfor argument in "$@"; do\n  if [[ "$previous" == "--header" && "$argument" == @* ]]; then header_file="${argument#@}"; fi\n  previous="$argument"\ndone\nif [[ -n "$header_file" && -r "$header_file" ]]; then cat "$header_file" >>%q; elif [[ -r /dev/fd/3 ]]; then cat /dev/fd/3 >>%q; fi\nbody=""\nif [[ " $* " == *" --data-binary @- "* ]]; then body="$(cat)"; fi\nprintf "%%s\\n---\\n" "$body" >>%q\nprintf "{\\"ok\\":true,\\"route\\":\\"daemon\\"}\\n200"\n' \
  "$protocol/curl-args" "$protocol/curl-headers" "$protocol/curl-headers" "$protocol/curl-bodies" >"$protocol/bin/curl"
chmod +x "$protocol/bin/curl"
write_adc "$protocol/adc.json" authorized_user false '[]' "$identity"
fixture_env "$protocol" "$protocol/adc.json" impersonated_service_account
FIXTURE_ENV[1]="PATH=$protocol/bin:$PATH"
FIXTURE_ENV+=("CLAUDE_HISTORY_RAG_AUTH_ENABLED=true" "CLAUDE_HISTORY_RAG_SERVER_PSK=SYNTHETIC_PSK_FOR_TEST_ONLY")
# shellcheck disable=SC2119
{
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_conversations","arguments":{"query":"needle","limit":2}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search_file_changes","arguments":{"file_path":"src/example.txt","operation_filter":"edit","limit":3}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_session_summary","arguments":{"session_id":"session-fixture","count":2}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"get_index_status","arguments":{}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_server_status","arguments":{"detail_level":"basic"}}}'
} | run_fixture >"$protocol/output" 2>"$protocol/stderr"
jq -s -e '
  length == 7 and
  .[0].id == 1 and .[0].result.protocolVersion == "2025-06-18" and
  .[1].id == 2 and (.[1].result.tools | length) == 5 and
  ([.[1].result.tools[].name] | sort) == (["get_index_status", "get_server_status", "get_session_summary", "search_conversations", "search_file_changes"] | sort) and
  ([.[2:][].id] == [3,4,5,6,7]) and
  all(.[2:][]; .result.isError == false and .result.structuredContent.route == "daemon")
' "$protocol/output" >/dev/null || {
  jq -c . "$protocol/output" >&2
  fail "MCP lifecycle/tool responses are invalid"
}
grep -Eq '/api/search' "$protocol/curl-args" || fail "search tool did not map to daemon search route"
grep -Eq '/api/search/files' "$protocol/curl-args" || fail "file tool did not map to daemon file route"
grep -Eq '/api/sessions' "$protocol/curl-args" || fail "session tool did not map to daemon session route"
[[ "$(grep -Ec '/status\?detail=' "$protocol/curl-args")" == "2" ]] || fail "status tools did not map to daemon status route"
grep -Eq '"query":"needle","limit":2' "$protocol/curl-bodies" || fail "search request body was not preserved"
grep -Eq '"file_path":"src/example.txt"' "$protocol/curl-bodies" || fail "file request body was not preserved"
grep -Eq '"session_id":"session-fixture"' "$protocol/curl-bodies" || fail "session request body was not preserved"
if grep -Eq 'Authorization:|synthetic-secret|synthetic-refresh' "$protocol/curl-args"; then
  fail "credential material appeared in curl argv"
fi
grep -Eq '^Authorization: Bearer SYNTHETIC_PSK_FOR_TEST_ONLY$' "$protocol/curl-headers" || fail "daemon authorization header was not forwarded through the protected descriptor"
pass "MCP lifecycle and five-tool daemon proxy"
pass "daemon authorization is not carried in argv"

auth_state="$TMP_ROOT/auth-state"
mkdir -p "$auth_state/bin" "$auth_state/home/.claude-history-rag"
printf '#!/usr/bin/env bash\nprintf invoked >%q\nheader_file=""\nprevious=""\nfor argument in "$@"; do\n  if [[ "$previous" == "--header" && "$argument" == @* ]]; then header_file="${argument#@}"; fi\n  previous="$argument"\ndone\nif [[ -n "$header_file" && -r "$header_file" ]]; then cat "$header_file" >%q; elif [[ -r /dev/fd/3 ]]; then cat /dev/fd/3 >%q; fi\nprintf "{\\"ok\\":true}\\n200"\n' \
  "$auth_state/curl-invoked" "$auth_state/curl-header" "$auth_state/curl-header" >"$auth_state/bin/curl"
chmod +x "$auth_state/bin/curl"
write_adc "$auth_state/adc.json" authorized_user false '[]' "$identity"
jq -n '{active:{key_plain:"SYNTHETIC_AUTH_STATE_PSK"}}' >"$auth_state/home/.claude-history-rag/auth.json"
restrict_file_acl "$auth_state/home/.claude-history-rag/auth.json"
fixture_env "$auth_state" "$auth_state/adc.json" impersonated_service_account
FIXTURE_ENV[1]="PATH=$auth_state/bin:$PATH"
FIXTURE_ENV+=("CLAUDE_HISTORY_RAG_AUTH_ENABLED=true")
{
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_server_status","arguments":{}}}'
} | run_fixture >"$auth_state/output" 2>"$auth_state/stderr"
grep -Eq '^Authorization: Bearer SYNTHETIC_AUTH_STATE_PSK$' "$auth_state/curl-header" || fail "active auth-state PSK was not forwarded"
pass "owner-only daemon auth state is supported"
loosen_file_acl "$auth_state/home/.claude-history-rag/auth.json"
rm -f "$auth_state/curl-invoked"
assert_failure "permissive daemon auth state rejected" run_fixture --validate-only
restrict_file_acl "$auth_state/home/.claude-history-rag/auth.json"

install="$TMP_ROOT/install"
mkdir -p "$install"
write_adc "$install/adc.json" authorized_user false '[]' "$identity"
fixture_env "$install" "$install/adc.json" impersonated_service_account
FIXTURE_ENV+=("CLAUDE_HISTORY_RAG_SERVER_PSK=SYNTHETIC_INSTALLER_PSK_MUST_NOT_PERSIST")
printf '{"preserved":true,"mcpServers":{}}\n' >"$install/config.json"
restrict_file_acl "$install/config.json"
assert_success "native JSON installer" run_fixture --install-json "$install/config.json" "$MCP_SCRIPT"
jq -e --arg command "$MCP_SCRIPT" '
  .preserved == true and
  .mcpServers["ai-agent-history-rag"].command == $command and
  .mcpServers["ai-agent-history-rag"].args == [] and
  .mcpServers["ai-agent-history-rag"].env.CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT == "production" and
  .mcpServers["ai-agent-history-rag"].env.CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE == "impersonated_service_account" and
  .mcpServers["ai-agent-history-rag"].env.GOOGLE_APPLICATION_CREDENTIALS != null and
  (.mcpServers["ai-agent-history-rag"].env | has("CLAUDE_HISTORY_RAG_SERVER_PSK") | not) and
  (.mcpServers["ai-agent-history-rag"].env | has("CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE") | not)
' "$install/config.json" >/dev/null || fail "native installer emitted unsafe or incomplete config"
pass "native installer preserves config and projects no PSK"

if find "$ROOT_DIR" -path "$ROOT_DIR/.git" -prune -o -name '*.py' -print -quit | grep -q .; then
  fail "retired runtime source remains tracked"
fi
for legacy_manifest in .python-version pyproject.toml uv.lock; do
  [[ ! -e "$ROOT_DIR/$legacy_manifest" ]] || fail "retired runtime manifest remains: $legacy_manifest"
done
operator_files=(
  "$ROOT_DIR/README.md"
  "$ROOT_DIR/docs"
  "$ROOT_DIR/.github"
  "$ROOT_DIR/scripts/start.sh"
  "$ROOT_DIR/scripts/install-launchd.sh"
  "$ROOT_DIR/scripts/uninstall-launchd.sh"
  "$ROOT_DIR/scripts/install-systemd.sh"
  "$ROOT_DIR/scripts/uninstall-systemd.sh"
  "$ROOT_DIR/scripts/install-windows.ps1"
  "$ROOT_DIR/scripts/uninstall-windows.ps1"
)
for operator_path in "${operator_files[@]}"; do
  [[ -e "$operator_path" ]] || fail "operator source is missing: $operator_path"
done

operator_file_count=0
while IFS= read -r -d '' operator_file; do
  operator_file_count=$((operator_file_count + 1))
  if grep -Eqi -- '(^|[^[:alnum:]_])(python|uv|pip|pytest|ruff)([^[:alnum:]_]|$)' "$operator_file"; then
    fail "documented or scripted legacy runtime route remains: $operator_file"
  fi
done < <(find "${operator_files[@]}" -type f \( \
  -name '*.sh' -o -name '*.ps1' -o -name '*.md' -o -name '*.yml' -o -name '*.yaml' \
\) -print0)
(( operator_file_count > 0 )) || fail "operator runtime scan selected no files"
grep -Eq 'history-rag-mcp-native.sh' "$ROOT_DIR/scripts/start.sh" || fail "start helper does not select native MCP"
pass "legacy runtime sources, manifests, and launch routes are retired"

printf 'PASS total=%d\n' "$PASS_COUNT"
