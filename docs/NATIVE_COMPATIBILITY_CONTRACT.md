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

The well-known carrier accepts either the legacy `authorized_user` source or a
device-bound `service_account` source. A service-account source is legal only
as the nested source of that pinned impersonation carrier; private-key fields
outside that object, unapproved source fields, target drift, and non-owner ACLs
are rejected. Credential material and machine-specific paths remain deployment
state, never repository content.

The explicit `device_service_account` profile uses that carrier's existing
nested service-account source directly. `credentials_identity` must equal the
source email. It does not impersonate the carrier's target, modify shared ADC,
create keys, or fall back to user credentials. The same source-field, canonical
Google endpoint, private-file, scope, and transport-override checks apply.

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

An explicit authenticated host-only `read_only` configuration uses
`127.0.0.1:4681` and an empty watch-root list. Its runtime and MCP environments
set `CLAUDE_HISTORY_RAG_READ_ONLY=true` and the matching status port. Readiness
then reports storage only; it makes no watcher or indexing claim. This mode
serves an existing corpus without taking over ingestion. All other runtime
modes retain their original endpoint and watcher requirement.

The bounded native HTTP surface additionally supports cursor-mutation refusal
and authenticated authority rotation endpoints. It has no dashboard or metrics
operator surface.

Authenticated `POST /api/search`, `/api/search/files`, and `/api/sessions`
serve the existing MCP retrieval contract. Search binds project and inclusive
date filters; file search additionally binds file path and operation. Session
retrieval applies the session filter before selecting the latest stored summary
per session. These routes never initialize schema, backfill, or write chunks.
Requests are bounded to 64 KiB, results to 8 MiB, and database work to 60 seconds.
Oversized results fail explicitly instead of silently truncating source evidence.

## Evidence

`scripts/docs-native-contract.test.sh` checks the operator documentation and
example container configuration against this contract. Platform-specific tests
check generated systemd, launchd, and Windows task artifacts. The Go test suite
checks bounded configuration, authentication, source roots, watcher behavior,
and readiness semantics.
