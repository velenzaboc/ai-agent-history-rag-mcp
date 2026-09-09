# Native operator contract

The supported runtime is the native `history-ragd` daemon plus its MCP STDIO
proxy. It has one production storage path: Cloud Spanner with the registered
`ConversationEmbeddingModel` and `gemini-embedding-001` at dimension 3072.

## Entry points

- Docker starts `history-ragd start --config /app/history-ragd.json` as a
  non-root user. Its healthcheck calls `/live`.
- Linux systemd, macOS launchd, and Windows Task Scheduler start the host
  binary with `supervise --config <owner-protected-config>`.
- `scripts/history-rag-mcp-native.sh` is the authenticated STDIO proxy. It is
  not an indexer and requires the loopback daemon to be running.

## Closed production inputs

The process requires the exact environment enumerated in the root README:
production contract, Spanner target, fixed embedding configuration, loopback
host/port, impersonated ADC selector, service-account identity, and bearer
secret. The process refuses missing, unknown, or substituted values. It also
refuses credential-file, emulator, and ambient Cloud SDK overrides.

## Source and state boundary

Configuration carries six unique absolute roots: Claude Code, Codex, Gemini,
Antigravity, ChatGPT export, and Claude app export. State, authentication, and
cursor files are held in a private state directory. Roots and snapshots are
identity-checked so link substitution and path replacement fail closed.

## HTTP boundary

`GET /live` is unauthenticated process/listener liveness. `GET /health` and
`GET /status` require a bearer credential and are ready only when both storage
and watcher dependencies succeed. A dependency failure is a 503 readiness
response, not a successful health response.

The bounded native HTTP surface additionally supports cursor-mutation refusal
and authenticated authority rotation endpoints. It has no dashboard or metrics
operator surface.

## Evidence

`scripts/docs-native-contract.test.sh` checks the operator documentation and
example container configuration against this contract. Platform-specific tests
check generated systemd, launchd, and Windows task artifacts. The Go test suite
checks bounded configuration, authentication, source roots, watcher behavior,
and readiness semantics.
