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
