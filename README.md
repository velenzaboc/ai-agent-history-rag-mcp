# AI Agent History RAG

AI Agent History RAG is a native Go daemon and MCP proxy for searchable history
from Claude Code, Codex, Gemini, Antigravity, ChatGPT exports, and Claude app
exports. Production storage is Cloud Spanner with a registered Vertex embedding
model. The daemon has no local-storage or unauthenticated production mode.

## Supported native entrypoints

| Platform | Operator entrypoint | Lifecycle |
|---|---|---|
| Container | `history-ragd start --config /app/history-ragd.json` | Docker owns process restart; `/live` is its healthcheck. |
| macOS | `scripts/install-launchd.sh` | Per-user LaunchAgent runs `history-ragd supervise`. |
| Linux | `scripts/install-systemd.sh` | Per-user systemd unit runs `history-ragd supervise`. |
| Windows | `scripts/install-windows.ps1` | Per-user logon task runs `history-ragd.exe supervise`. |
| MCP | `scripts/history-rag-mcp-native.sh` | Authenticated STDIO proxy to the loopback daemon. |

## Work recovery console

The repository also includes a config-driven, read-only browser console that
joins task-graph state to AI history evidence. It surfaces a KANBAN view,
config-driven program subtree pages, hierarchy and dependency graphs,
milestones, abandoned or unlinked sessions, history search, copyable
history-first restart prompts, and task prompts that need no session match—all
without creating a second work-state store.

See [Work Recovery Console](docs/WORK_RECOVERY_CONSOLE.md) and the
[complete example configuration](configs/work-recovery-console.example.json).

The repository's Compose file is retained as a compatibility artifact and is
not a supported native production operator entrypoint. Use the Dockerfile or a
platform installer until that artifact is separately migrated.

## Build

```bash
go build -o bin/history-ragd ./cmd/history-ragd
go test ./...
```

For a Windows binary from another platform:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/history-ragd.exe ./cmd/history-ragd
```

## Production contract

Every service installer validates the following values before registering a
native process. Deployment identities are inputs; never place real values in
repository files.

```text
CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT=production
CLAUDE_HISTORY_RAG_STORAGE_BACKEND=spanner
CLAUDE_HISTORY_RAG_SPANNER_PROJECT=<project>
CLAUDE_HISTORY_RAG_SPANNER_INSTANCE=<instance>
CLAUDE_HISTORY_RAG_SPANNER_DATABASE=<database>
CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODE=spanner
CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID=ConversationEmbeddingModel
CLAUDE_HISTORY_RAG_EMBEDDING_PROVIDER=vertex
CLAUDE_HISTORY_RAG_EMBEDDING_MODEL=gemini-embedding-001
CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION=3072
CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST=127.0.0.1
CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT=4680
CLAUDE_HISTORY_RAG_CREDENTIALS_SOURCE=application_default
CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE=impersonated_service_account
CLAUDE_HISTORY_RAG_CREDENTIALS_IDENTITY=<service-account-email>
CLAUDE_HISTORY_RAG_SERVER_PSK=<bearer-secret>
```

The host runtime uses the well-known, owner-protected impersonated ADC carrier.
Credential-file overrides, emulator configuration, and ambient Cloud SDK
configuration are rejected. In particular, leave `GOOGLE_APPLICATION_CREDENTIALS`,
`CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE`, `CLOUDSDK_CONFIG`, and
`SPANNER_EMULATOR_HOST` unset.

The carrier may retain the legacy `authorized_user` source or use a
device-bound `service_account` source. The latter is accepted only inside the
validated impersonation carrier; its target identity, scope, quota project,
source shape, and owner-only file ACL are checked before any Google client is
constructed. Do not commit a carrier, private key, refresh token, or machine
path to this repository.

## Source roots

The daemon watches exactly these six source families. Platform installers create
missing directories and generate owner-protected native configuration.

| Source | Default root |
|---|---|
| Claude Code | `~/.claude/projects` |
| Codex | `~/.codex/sessions` |
| Gemini | `~/.gemini/tmp` |
| Antigravity | `~/.gemini/antigravity` |
| ChatGPT export | `~/.claude-history-rag/imports/chatgpt` |
| Claude app export | `~/.claude-history-rag/imports/claude-app` |

ChatGPT and Claude app exports belong in their respective drop folders. The
watcher preserves source identity and rejects unsafe path changes.

## Container

Copy `.env.docker.example` to a private environment file, replace each
placeholder, then build and run the native image:

```bash
docker build --tag history-ragd:local .
docker run --rm --env-file .env.docker -p 4680:4680 history-ragd:local
```

The container binds `0.0.0.0:4680` only inside its network namespace and
requires authentication. Its Docker healthcheck calls `/live`; it does not
pretend a missing Spanner dependency is ready. Mount persistent source and state
volumes when operating beyond a smoke test.

## Host service installation

Host services bind only `127.0.0.1:4680`; do not open that listener to the
network. Build the binary first, export the production contract, then install.

### macOS

```bash
HISTORY_RAGD_BIN="$PWD/bin/history-ragd" ./scripts/install-launchd.sh
./scripts/uninstall-launchd.sh
```

See [launchd operator notes](docs/LAUNCHD_NATIVE.md).

### Linux

```bash
HISTORY_RAGD_BIN="$PWD/bin/history-ragd" ./scripts/install-systemd.sh
./scripts/uninstall-systemd.sh
```

See [systemd operator notes](docs/SYSTEMD_NATIVE.md).

### Windows

```powershell
.\scripts\install-windows.ps1 -HistoryRagdPath "$PWD\bin\history-ragd.exe"
.\scripts\uninstall-windows.ps1
```

See [Windows operator notes](docs/WINDOWS_NATIVE.md). Windows Task Scheduler
validation requires a Windows host; the repository cross-build verifies only
that the native executable can be produced.

## Liveness and readiness

`GET /live` is intentionally unauthenticated and answers only whether the
process listener is alive:

```bash
curl --fail http://127.0.0.1:4680/live
```

`GET /health` and `GET /status` require a bearer credential. They are readiness
checks: they return 200 only when both Spanner storage and the source watcher
are ready, otherwise 503. They never expose credentials or source paths.

```bash
curl --fail -H "Authorization: Bearer $CLAUDE_HISTORY_RAG_SERVER_PSK" \
  http://127.0.0.1:4680/health
```

## MCP proxy

Use the native STDIO proxy only after the loopback daemon is running with the
same production contract:

```bash
./scripts/history-rag-mcp-native.sh --validate-only
./scripts/history-rag-mcp-native.sh
```

## Troubleshooting

- `/live` fails: inspect the platform service manager and its native process.
- `/live` succeeds but `/health` returns 503: confirm Spanner reachability and
  that all six watcher roots are available to the service user.
- `/health` returns 401: supply the configured bearer secret; do not disable
  authentication to diagnose the issue.
- Installer rejects credentials: remove the forbidden ambient credential and
  emulator overrides, then verify the well-known impersonated ADC carrier.
- A source root is rejected: replace links with an owned real directory; the
  watcher intentionally refuses identity changes.

## Verification

```bash
./scripts/docs-native-contract.test.sh
go test -race ./internal/history/config ./internal/history/watch ./internal/history/api ./cmd/history-ragd
go vet ./...
go build ./...
go test ./...
```

The exact native contract and its bounded API surface are recorded in
[docs/NATIVE_COMPATIBILITY_CONTRACT.md](docs/NATIVE_COMPATIBILITY_CONTRACT.md).
