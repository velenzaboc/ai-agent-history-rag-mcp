#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
INSTALLER="$ROOT_DIR/scripts/install-windows.ps1"
UNINSTALLER="$ROOT_DIR/scripts/uninstall-windows.ps1"
PASS_COUNT=0

pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1" >&2; exit 1; }
require_literal() { grep -Fq -- "$2" "$1" || fail "$3: missing $2"; }
require_absent() { grep -Eqi -- "$2" "$1" && fail "$3: forbidden $2" || true; }

for file in "$INSTALLER" "$UNINSTALLER"; do
  [[ -r "$file" ]] || fail "missing Windows file: $file"
done
# shellcheck disable=SC2016 # PowerShell variables are intentionally literal.
required_literals=(
  'Set-StrictMode -Version Latest'
  'history-ragd.exe'
  'New-ScheduledTaskAction -Execute $HistoryRagdPath'
  'supervise --config'
  'CLAUDE_HISTORY_RAG_STORAGE_BACKEND" "spanner'
  'CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST" "127.0.0.1'
  'Set-CurrentUserOnlyAcl'
  'RestartCount 3'
  'RestartInterval (New-TimeSpan -Minutes 1)'
  'Start-ScheduledTask -TaskName $TaskName'
)
for literal in "${required_literals[@]}"; do
  require_literal "$INSTALLER" "$literal" "native Windows installer"
done
require_absent "$INSTALLER" '(^|[^[:alnum:]_])(python|uv)([^[:alnum:]_]|$)' "native Windows installer"
require_literal "$INSTALLER" 'GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "CLOUDSDK_CONFIG", "SPANNER_EMULATOR_HOST"' "native Windows installer rejects ambient overrides"
# shellcheck disable=SC2016 # PowerShell switches are intentionally literal.
require_literal "$UNINSTALLER" '[switch]$PurgeState' "explicit state purge"
# shellcheck disable=SC2016 # PowerShell switches are intentionally literal.
require_literal "$UNINSTALLER" '[switch]$PurgeEnvironment' "explicit environment purge"
require_literal "$UNINSTALLER" 'Unregister-ScheduledTask' "deterministic task stop"
pass "PowerShell sources require native production-only task shape"

if command -v pwsh >/dev/null 2>&1; then
  installer_for_pwsh="$INSTALLER"
  uninstaller_for_pwsh="$UNINSTALLER"
  if command -v cygpath >/dev/null 2>&1; then
    installer_for_pwsh="$(cygpath -w -- "$INSTALLER")"
    uninstaller_for_pwsh="$(cygpath -w -- "$UNINSTALLER")"
  fi
  INSTALLER_FOR_PWSH="$installer_for_pwsh" UNINSTALLER_FOR_PWSH="$uninstaller_for_pwsh" \
    pwsh -NoLogo -NoProfile -Command '[ScriptBlock]::Create([IO.File]::ReadAllText($env:INSTALLER_FOR_PWSH)) | Out-Null; [ScriptBlock]::Create([IO.File]::ReadAllText($env:UNINSTALLER_FOR_PWSH)) | Out-Null'
  pass "PowerShell parser accepts task scripts"
else
  pass "PowerShell runtime unavailable on macOS; static validation completed"
fi

OUT_DIR="$(mktemp -d)"
cleanup() { rm -rf -- "$OUT_DIR"; }
trap cleanup EXIT
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$OUT_DIR/history-ragd.exe" ./cmd/history-ragd
[[ -s "$OUT_DIR/history-ragd.exe" ]] || fail "Windows native executable was not produced"
pass "Windows amd64 history-ragd cross-build succeeds"

printf 'PASS total=%d\n' "$PASS_COUNT"
