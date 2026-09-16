# Native launchd user agent

`scripts/install-launchd.sh` installs `history-ragd supervise --config …` as a
per-user LaunchAgent. It accepts only the production Spanner, Vertex,
impersonated-ADC, and bearer-secret contract. It rejects local storage,
emulators, credential-file overrides, public binds, and disabled authentication.

The installer writes owner-only files at:

- `~/.config/ai-agent-history-rag/history-ragd.json` for the native daemon;
- `~/Library/LaunchAgents/com.ai-agent-history-rag.daemon.plist`, including the
  required bearer secret.

The host daemon binds only `127.0.0.1:4680`. `GET /live` is unauthenticated
process/listener liveness. Authenticated `GET /health` is dependency readiness
and remains 503 until Spanner and the source watcher are ready. Launchd starts
the agent at load and restarts non-successful exits with a five-second throttle.

Use `scripts/uninstall-launchd.sh` to stop and remove the agent while retaining
durable state. `--purge-state` is required for state removal.

## Serve an existing corpus without taking over ingestion

Set `CLAUDE_HISTORY_RAG_READ_ONLY=true`, status port `4681`, and the explicit
`device_service_account` credential profile with the local device's email.
The installer uses the separate `com.ai-agent-history-rag.search` LaunchAgent,
`history-ragd-search.json`, and `~/.claude-history-rag-native-search` state.
It does not stop the ingestion agent or scan source files. Readiness checks
storage only. Use the same read-only environment in the MCP proxy.

The device source remains in the existing private well-known ADC carrier.
Neither global ADC nor another application's credentials are changed. Supply
the existing client bearer secret to the native service through the protected
installer environment; never put it in shell arguments or source control.
Set `CLAUDE_HISTORY_RAG_READ_ONLY=true` when uninstalling this search agent.
