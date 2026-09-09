# Native API reference

The native API is intentionally narrow. It is served by `internal/history/api`
and receives requests only through the configured daemon listener.

## Readiness

`GET /live` is unauthenticated liveness. `GET /health` and `GET /status` are
authenticated readiness endpoints. A successful readiness result proves both
the Spanner store and source watcher are ready; 503 reports a dependency that
is not ready.

## Authentication authority

When the daemon has an authority, these authenticated bounded endpoints are
available:

- `GET /api/auth/state`
- `POST /api/auth/rotate`
- `POST /api/auth/rotation-ack`

The rotation request body is bounded and malformed requests are rejected. The
API exposes state identifiers only, never bearer values.

## Cursor mutation boundary

`POST /api/positions` is authenticated but intentionally returns a conflict:
direct cursor synchronization is forbidden. Durable cursor and upload state is
owned by their native packages rather than an unbounded HTTP mutation route.

## MCP boundary

`scripts/history-rag-mcp-native.sh` validates the native production contract
before starting its STDIO loop. It is a bounded proxy and does not open an
independent storage or credential path.
