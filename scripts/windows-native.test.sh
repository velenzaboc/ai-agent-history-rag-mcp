#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
INSTALLER="$ROOT_DIR/scripts/install-windows.ps1"
UNINSTALLER="$ROOT_DIR/scripts/uninstall-windows.ps1"
PASS_COUNT=0
OUT_DIR="$(mktemp -d)"

cleanup() { rm -rf -- "$OUT_DIR"; }
trap cleanup EXIT

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
  out_dir_for_pwsh="$OUT_DIR"
  if command -v cygpath >/dev/null 2>&1; then
    installer_for_pwsh="$(cygpath -w -- "$INSTALLER")"
    uninstaller_for_pwsh="$(cygpath -w -- "$UNINSTALLER")"
    out_dir_for_pwsh="$(cygpath -w -- "$OUT_DIR")"
  fi
  INSTALLER_FOR_PWSH="$installer_for_pwsh" UNINSTALLER_FOR_PWSH="$uninstaller_for_pwsh" \
    pwsh -NoLogo -NoProfile -Command '[ScriptBlock]::Create([IO.File]::ReadAllText($env:INSTALLER_FOR_PWSH)) | Out-Null; [ScriptBlock]::Create([IO.File]::ReadAllText($env:UNINSTALLER_FOR_PWSH)) | Out-Null'
  pass "PowerShell parser accepts task scripts"

  if pwsh -NoLogo -NoProfile -Command 'if ($IsWindows) { exit 0 }; exit 1'; then
    HARNESS="$OUT_DIR/windows-native-harness.ps1"
    cat >"$HARNESS" <<'POWERSHELL'
param(
    [Parameter(Mandatory = $true)][string]$Installer,
    [Parameter(Mandatory = $true)][string]$Sandbox
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "windows-native generated config: $Message" }
}

$ProjectDir = Join-Path $Sandbox "project"
$ScriptDir = Join-Path $ProjectDir "scripts"
$BinDir = Join-Path $ProjectDir "bin"
$UserProfile = Join-Path $Sandbox "home"
$LocalAppData = Join-Path $Sandbox "local-app-data"
$CopiedInstaller = Join-Path $ScriptDir "install-windows.ps1"
$Binary = Join-Path $BinDir "history-ragd.exe"
$ExpectedConfig = Join-Path $LocalAppData "ai-agent-history-rag\history-ragd.json"

New-Item -ItemType Directory -Path $ScriptDir, $BinDir, $UserProfile, $LocalAppData -Force | Out-Null
Copy-Item -LiteralPath $Installer -Destination $CopiedInstaller
Set-Content -LiteralPath $Binary -Value "test executable" -NoNewline

$TaskEnvironmentNames = @(
    "CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT",
    "CLAUDE_HISTORY_RAG_STORAGE_BACKEND",
    "CLAUDE_HISTORY_RAG_SPANNER_PROJECT",
    "CLAUDE_HISTORY_RAG_SPANNER_INSTANCE",
    "CLAUDE_HISTORY_RAG_SPANNER_DATABASE",
    "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE",
    "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID",
    "CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER",
    "CLAUDE_HISTORY_RAG_EMBEDDING_MODEL",
    "CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION",
    "CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST",
    "CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT",
    "CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE",
    "CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE",
    "CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY",
    "CLAUDE_HISTORY_RAG_SERVER_PSK"
)
$ForbiddenNames = @(
    "GOOGLE_APPLICATION_CREDENTIALS",
    "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE",
    "CLOUDSDK_CONFIG",
    "SPANNER_EMULATOR_HOST"
)
$PreviousUserEnvironment = @{}
foreach ($Name in $TaskEnvironmentNames + $ForbiddenNames) {
    $PreviousUserEnvironment[$Name] = [Environment]::GetEnvironmentVariable($Name, "User")
}

$env:USERPROFILE = $UserProfile
$env:LOCALAPPDATA = $LocalAppData
$ProcessEnvironment = @{
    CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT = "production"
    CLAUDE_HISTORY_RAG_STORAGE_BACKEND = "spanner"
    CLAUDE_HISTORY_RAG_SPANNER_PROJECT = "test-project"
    CLAUDE_HISTORY_RAG_SPANNER_INSTANCE = "test-instance"
    CLAUDE_HISTORY_RAG_SPANNER_DATABASE = "test-database"
    CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE = "spanner"
    CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID = "ConversationEmbeddingModel"
    CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER = "vertex"
    CLAUDE_HISTORY_RAG_EMBEDDING_MODEL = "gemini-embedding-001"
    CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION = "3072"
    CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST = "127.0.0.1"
    CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT = "4680"
    CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE = "application_default"
    CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE = "impersonated_service_account"
    CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY = "test-service-account"
    CLAUDE_HISTORY_RAG_SERVER_PSK = "test-psk"
}
function Set-Acl {
    param([string]$LiteralPath, [object]$AclObject)
}
function Get-ScheduledTask {
    param([string]$TaskName, [object]$ErrorAction)
    return $null
}
function Unregister-ScheduledTask {
    param([string]$TaskName, [switch]$Confirm)
    throw "unexpected attempt to unregister a test task"
}
function New-ScheduledTaskAction {
    param([string]$Execute, [string]$Argument, [string]$WorkingDirectory)
    return [pscustomobject]@{ Execute = $Execute; Argument = $Argument; WorkingDirectory = $WorkingDirectory }
}
function New-ScheduledTaskTrigger {
    param([switch]$AtLogOn, [string]$User)
    return [pscustomobject]@{ User = $User }
}
function New-ScheduledTaskSettingsSet {
    param(
        [switch]$AllowStartIfOnBatteries,
        [switch]$DontStopIfGoingOnBatteries,
        [switch]$StartWhenAvailable,
        [int]$RestartCount,
        [timespan]$RestartInterval
    )
    return [pscustomobject]@{ RestartCount = $RestartCount; RestartInterval = $RestartInterval }
}
function Register-ScheduledTask {
    param(
        [string]$TaskName,
        [object]$Action,
        [object]$Trigger,
        [object]$Settings,
        [string]$Description
    )
}
function Start-ScheduledTask {
    param([string]$TaskName)

    Assert-True (Test-Path -LiteralPath $ExpectedConfig -PathType Leaf) "config must exist before task start"
    $Config = Get-Content -Raw -LiteralPath $ExpectedConfig | ConvertFrom-Json
    $ExpectedRoots = @(
        (Join-Path $UserProfile ".claude\projects"),
        (Join-Path $UserProfile ".codex\sessions"),
        (Join-Path $UserProfile ".gemini\tmp"),
        (Join-Path $UserProfile ".gemini\antigravity"),
        (Join-Path $UserProfile ".claude-history-rag\imports\chatgpt"),
        (Join-Path $UserProfile ".claude-history-rag\imports\claude-app"),
        (Join-Path $UserProfile ".velenza\provider-profiles\claude"),
        (Join-Path $UserProfile ".velenza\provider-profiles\codex")
    ) | ForEach-Object { [IO.Path]::GetFullPath($_) }
    $ActualRoots = @($Config.watch_roots)

    Assert-True ($Config.listen -ceq "127.0.0.1:4680") "listen must remain loopback-only"
    Assert-True ($Config.auth_enabled -eq $true) "authentication must remain enabled"
    Assert-True ($ActualRoots.Count -eq 8) "watch_roots must contain exactly eight entries"
    for ($Index = 0; $Index -lt $ExpectedRoots.Count; $Index++) {
        Assert-True ([string]::Equals($ActualRoots[$Index], $ExpectedRoots[$Index], [StringComparison]::OrdinalIgnoreCase)) "watch root $Index differs from the generated contract"
        Assert-True (Test-Path -LiteralPath $ActualRoots[$Index] -PathType Container) "watch root $Index must exist before task start"
    }
}

try {
    foreach ($Entry in $ProcessEnvironment.GetEnumerator()) {
        [Environment]::SetEnvironmentVariable([string]$Entry.Key, [string]$Entry.Value, "Process")
    }
    foreach ($Name in $ForbiddenNames) {
        [Environment]::SetEnvironmentVariable($Name, $null, "Process")
        [Environment]::SetEnvironmentVariable($Name, $null, "User")
    }
    & $CopiedInstaller -HistoryRagdPath $Binary
} finally {
    foreach ($Name in $TaskEnvironmentNames + $ForbiddenNames) {
        [Environment]::SetEnvironmentVariable($Name, $PreviousUserEnvironment[$Name], "User")
    }
}
POWERSHELL
    harness_for_pwsh="$HARNESS"
    if command -v cygpath >/dev/null 2>&1; then
      harness_for_pwsh="$(cygpath -w -- "$HARNESS")"
    fi
    pwsh -NoLogo -NoProfile -File "$harness_for_pwsh" -Installer "$installer_for_pwsh" -Sandbox "$out_dir_for_pwsh"
    pass "generated Windows config has eight existing roots and preserves loopback authentication before task start"
  else
    pass "Windows runtime unavailable; executable generated-config validation skipped"
  fi
else
  pass "PowerShell runtime unavailable; static validation completed"
fi

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$OUT_DIR/history-ragd.exe" ./cmd/history-ragd
[[ -s "$OUT_DIR/history-ragd.exe" ]] || fail "Windows native executable was not produced"
pass "Windows amd64 history-ragd cross-build succeeds"

printf 'PASS total=%d\n' "$PASS_COUNT"
