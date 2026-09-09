[CmdletBinding()]
param(
    [switch]$PurgeState,
    [switch]$PurgeEnvironment
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$TaskName = "AIAgentHistoryRAG"
$DataDir = Join-Path $env:USERPROFILE ".claude-history-rag"
$ConfigDir = Join-Path $env:LOCALAPPDATA "ai-agent-history-rag"
$ConfigPath = Join-Path $ConfigDir "history-ragd.json"

$ExistingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if ($ExistingTask) { Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false }
Remove-Item -LiteralPath $ConfigPath -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $ConfigDir -Force -ErrorAction SilentlyContinue

if ($PurgeEnvironment) {
    foreach ($Name in @(
        "CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT", "CLAUDE_HISTORY_RAG_STORAGE_BACKEND",
        "CLAUDE_HISTORY_RAG_SPANNER_PROJECT", "CLAUDE_HISTORY_RAG_SPANNER_INSTANCE",
        "CLAUDE_HISTORY_RAG_SPANNER_DATABASE", "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE",
        "CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID", "CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER",
        "CLAUDE_HISTORY_RAG_EMBEDDING_MODEL", "CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION",
        "CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST", "CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT",
        "CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE", "CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE",
        "CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY", "CLAUDE_HISTORY_RAG_SERVER_PSK"
    )) { [Environment]::SetEnvironmentVariable($Name, $null, "User") }
}
if ($PurgeState) { Remove-Item -LiteralPath $DataDir -Recurse -Force -ErrorAction SilentlyContinue }

Write-Host "Native scheduled task removed. State and task environment are retained unless their explicit purge switches are used."
