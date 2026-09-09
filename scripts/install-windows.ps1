[CmdletBinding()]
param(
    [string]$HistoryRagdPath = ""
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Fail([string]$Message) {
    throw "install-windows: $Message"
}

function Require-Value([string]$Name) {
    $Value = [Environment]::GetEnvironmentVariable($Name, "Process")
    if ([string]::IsNullOrWhiteSpace($Value)) { Fail "$Name must be set" }
    if ($Value -notmatch '^[A-Za-z0-9._~+/:=@-]+$') { Fail "$Name contains unsupported task-environment characters" }
    return $Value
}

function Require-Exact([string]$Name, [string]$Expected) {
    $Value = Require-Value $Name
    if ($Value -cne $Expected) { Fail "$Name must equal $Expected" }
    return $Value
}

function Require-CleanAbsolutePath([string]$Path, [string]$Label) {
    if (-not [System.IO.Path]::IsPathFullyQualified($Path)) { Fail "$Label must be absolute" }
    if ($Path -match '[\r\n"]') { Fail "$Label contains unsupported path characters" }
    return [System.IO.Path]::GetFullPath($Path)
}

function Set-CurrentUserOnlyAcl([string]$Path) {
    $Identity = [Security.Principal.WindowsIdentity]::GetCurrent().User
    if ($null -eq $Identity) { Fail "current Windows identity is unavailable" }
    $Acl = New-Object Security.AccessControl.FileSecurity
    $Acl.SetAccessRuleProtection($true, $false)
    $Rule = New-Object Security.AccessControl.FileSystemAccessRule($Identity, "FullControl", "ContainerInherit,ObjectInherit", "None", "Allow")
    $Acl.SetAccessRule($Rule)
    Set-Acl -LiteralPath $Path -AclObject $Acl
}

function New-PrivateDirectory([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        New-Item -ItemType Directory -Path $Path -Force | Out-Null
    }
    Set-CurrentUserOnlyAcl $Path
}

function Set-TaskEnvironment([hashtable]$Values) {
    foreach ($Entry in $Values.GetEnumerator()) {
        [Environment]::SetEnvironmentVariable([string]$Entry.Key, [string]$Entry.Value, "User")
    }
}

$TaskName = "AIAgentHistoryRAG"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectDir = (Resolve-Path -LiteralPath (Join-Path $ScriptDir "..")).Path
$DataDir = Require-CleanAbsolutePath (Join-Path $env:USERPROFILE ".claude-history-rag") "state directory"
$ConfigDir = Require-CleanAbsolutePath (Join-Path $env:LOCALAPPDATA "ai-agent-history-rag") "config directory"
$ConfigPath = Join-Path $ConfigDir "history-ragd.json"

if ([string]::IsNullOrWhiteSpace($HistoryRagdPath)) {
    $HistoryRagdPath = Join-Path $ProjectDir "bin\history-ragd.exe"
}
$HistoryRagdPath = Require-CleanAbsolutePath $HistoryRagdPath "history-ragd binary"
if (-not (Test-Path -LiteralPath $HistoryRagdPath -PathType Leaf)) { Fail "history-ragd binary does not exist" }
if ((Get-Item -LiteralPath $HistoryRagdPath).Attributes -band [IO.FileAttributes]::ReparsePoint) { Fail "history-ragd binary must not be a link" }
if (-not $HistoryRagdPath.StartsWith("$ProjectDir\", [StringComparison]::OrdinalIgnoreCase)) { Fail "history-ragd binary must be beneath the project directory" }

# The native production selector is intentionally closed. No local database,
# emulator, credential-file override, public bind, or auth-off task can install.
$RuntimeContract = Require-Exact "CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT" "production"
$StorageBackend = Require-Exact "CLAUDE_HISTORY_RAG_STORAGE_BACKEND" "spanner"
$EmbeddingMode = Require-Exact "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE" "spanner"
$EmbeddingModelID = Require-Exact "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID" "ConversationEmbeddingModel"
$EmbeddingProvider = Require-Exact "CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER" "vertex"
$EmbeddingModel = Require-Exact "CLAUDE_HISTORY_RAG_EMBEDDING_MODEL" "gemini-embedding-001"
$EmbeddingDimension = Require-Exact "CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION" "3072"
$StatusHost = Require-Exact "CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST" "127.0.0.1"
$StatusPort = Require-Exact "CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT" "4680"
$CredentialsSource = Require-Exact "CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE" "application_default"
$CredentialsProfile = Require-Exact "CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE" "impersonated_service_account"
$SpannerProject = Require-Value "CLAUDE_HISTORY_RAG_SPANNER_PROJECT"
$SpannerInstance = Require-Value "CLAUDE_HISTORY_RAG_SPANNER_INSTANCE"
$SpannerDatabase = Require-Value "CLAUDE_HISTORY_RAG_SPANNER_DATABASE"
$CredentialsIdentity = Require-Value "CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY"
$ServerPSK = Require-Value "CLAUDE_HISTORY_RAG_SERVER_PSK"
foreach ($Forbidden in "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "CLOUDSDK_CONFIG", "SPANNER_EMULATOR_HOST") {
    if (-not [string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($Forbidden, "Process"))) { Fail "$Forbidden must be unset" }
    if (-not [string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($Forbidden, "User"))) { Fail "$Forbidden must be unset from the user environment" }
}

$ClaudeRoot = Require-CleanAbsolutePath ($(if ($env:CLAUDE_HISTORY_RAG_PROJECTS_PATH) { $env:CLAUDE_HISTORY_RAG_PROJECTS_PATH } else { Join-Path $env:USERPROFILE ".claude\projects" })) "Claude watch root"
$CodexRoot = Require-CleanAbsolutePath ($(if ($env:CLAUDE_HISTORY_RAG_CODEX_SESSIONS_PATH) { $env:CLAUDE_HISTORY_RAG_CODEX_SESSIONS_PATH } else { Join-Path $env:USERPROFILE ".codex\sessions" })) "Codex watch root"
$GeminiRoot = Require-CleanAbsolutePath ($(if ($env:CLAUDE_HISTORY_RAG_GEMINI_SESSIONS_PATH) { $env:CLAUDE_HISTORY_RAG_GEMINI_SESSIONS_PATH } else { Join-Path $env:USERPROFILE ".gemini\tmp" })) "Gemini watch root"
$AntigravityRoot = Require-CleanAbsolutePath ($(if ($env:CLAUDE_HISTORY_RAG_ANTIGRAVITY_SESSIONS_PATH) { $env:CLAUDE_HISTORY_RAG_ANTIGRAVITY_SESSIONS_PATH } else { Join-Path $env:USERPROFILE ".gemini\antigravity" })) "Antigravity watch root"
$ChatGPTRoot = Require-CleanAbsolutePath ($(if ($env:CLAUDE_HISTORY_RAG_CHATGPT_EXPORTS_PATH) { $env:CLAUDE_HISTORY_RAG_CHATGPT_EXPORTS_PATH } else { Join-Path $DataDir "imports\chatgpt" })) "ChatGPT watch root"
$ClaudeAppRoot = Require-CleanAbsolutePath ($(if ($env:CLAUDE_HISTORY_RAG_CLAUDE_APP_EXPORTS_PATH) { $env:CLAUDE_HISTORY_RAG_CLAUDE_APP_EXPORTS_PATH } else { Join-Path $DataDir "imports\claude-app" })) "Claude App watch root"
$WatchRoots = @($ClaudeRoot, $CodexRoot, $GeminiRoot, $AntigravityRoot, $ChatGPTRoot, $ClaudeAppRoot)
if (($WatchRoots | Select-Object -Unique).Count -ne 6) { Fail "watch roots must be unique" }

New-PrivateDirectory $DataDir
New-PrivateDirectory $ConfigDir
foreach ($Root in $WatchRoots) {
    if (-not (Test-Path -LiteralPath $Root -PathType Container)) { New-Item -ItemType Directory -Path $Root -Force | Out-Null }
}

$Config = [ordered]@{
    state_dir = $DataDir
    listen = "127.0.0.1:4680"
    container_mode = $false
    watch_roots = $WatchRoots
    pid_file = (Join-Path $DataDir "daemon.pid")
    auth_state_file = (Join-Path $DataDir "auth.json")
    auth_enabled = $true
    checkout_root = $ProjectDir
    executable = $HistoryRagdPath
}
$Config | ConvertTo-Json -Depth 3 -Compress | Set-Content -LiteralPath $ConfigPath -Encoding utf8 -NoNewline
Set-CurrentUserOnlyAcl $ConfigPath

$TaskEnvironment = [ordered]@{
    CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT = $RuntimeContract
    CLAUDE_HISTORY_RAG_STORAGE_BACKEND = $StorageBackend
    CLAUDE_HISTORY_RAG_SPANNER_PROJECT = $SpannerProject
    CLAUDE_HISTORY_RAG_SPANNER_INSTANCE = $SpannerInstance
    CLAUDE_HISTORY_RAG_SPANNER_DATABASE = $SpannerDatabase
    CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE = $EmbeddingMode
    CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID = $EmbeddingModelID
    CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER = $EmbeddingProvider
    CLAUDE_HISTORY_RAG_EMBEDDING_MODEL = $EmbeddingModel
    CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION = $EmbeddingDimension
    CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST = $StatusHost
    CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT = $StatusPort
    CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE = $CredentialsSource
    CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE = $CredentialsProfile
    CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY = $CredentialsIdentity
    CLAUDE_HISTORY_RAG_SERVER_PSK = $ServerPSK
}
Set-TaskEnvironment $TaskEnvironment

$ExistingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if ($ExistingTask) { Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false }
$Action = New-ScheduledTaskAction -Execute $HistoryRagdPath -Argument "supervise --config `"$ConfigPath`"" -WorkingDirectory $ProjectDir
$Trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$Settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName $TaskName -Action $Action -Trigger $Trigger -Settings $Settings -Description "AI Agent History RAG native daemon"
Start-ScheduledTask -TaskName $TaskName

Write-Host "Native scheduled task installed: $TaskName"
Write-Host "Liveness: http://127.0.0.1:4680/live"
Write-Host "Readiness: authenticated /health stays 503 until Spanner and the watcher are ready"
