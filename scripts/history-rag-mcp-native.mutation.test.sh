#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOURCE="$ROOT_DIR/scripts/history-rag-mcp-native.sh"
TEST="$ROOT_DIR/scripts/history-rag-mcp-native.test.sh"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

[[ -x "$SOURCE" ]] || { printf 'native source missing\n' >&2; exit 1; }

expect_killed() {
  local label="$1"
  local expression="$2"
  local replacement="$3"
  local mutant="$TMP_ROOT/${label// /-}.sh"
  cp "$SOURCE" "$mutant"
  chmod +x "$mutant"
  EXPRESSION="$expression" REPLACEMENT="$replacement" perl -0pi -e '
    BEGIN { $from = $ENV{"EXPRESSION"}; $to = $ENV{"REPLACEMENT"}; }
    $count = s/\Q$from\E/$to/g;
    END { exit 91 unless $count == 1; }
  ' "$mutant"
  if MCP_SCRIPT="$mutant" bash "$TEST" >"$TMP_ROOT/${label// /-}.out" 2>&1; then
    printf 'FAIL mutation survived: %s\n' "$label" >&2
    exit 1
  fi
  printf 'PASS mutation killed: %s\n' "$label"
}

# shellcheck disable=SC2016
expect_killed \
  "nested source type" \
  '[[ "$source_type" == "authorized_user" || "$source_type" == "service_account" ]] || contract_fail "impersonated ADC source_credentials must be authorized_user or service_account"' \
  ': # MUTANT accepts arbitrary nested source type'

# shellcheck disable=SC2016
expect_killed \
  "recursive private marker" \
  'contains_private_key_fields_outside_source "$adc_path" || contract_fail "GOOGLE_APPLICATION_CREDENTIALS must not contain private key material outside source_credentials"' \
  ': # MUTANT accepts private-key fields outside the source'

expect_killed \
  "gate before network" \
  'validate_production_contract # GATE_BEFORE_NETWORK_OR_HANDLER' \
  ': # MUTANT bypasses pre-network production gate'

# shellcheck disable=SC2016
expect_killed \
  "native installer command" \
  '--arg command "$binary_path"' \
  '--arg command "retired-runtime"'

printf 'PASS mutations=4\n'
