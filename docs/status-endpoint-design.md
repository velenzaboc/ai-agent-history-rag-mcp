# Native status endpoints

The native daemon exposes a deliberately small, bounded JSON status surface on
the configured listener. Host services use `127.0.0.1:4680`; the container
uses its fixed internal listener and exposes it through container networking.

| Endpoint | Authentication | Meaning |
|---|---|---|
| `GET /live` | none | Process and listener liveness only; returns 200 with `{"status":"live"}`. |
| `GET /health` | bearer | Dependency readiness; returns 200 only when storage and watcher are ready, otherwise 503. |
| `GET /status` | bearer | Same bounded readiness projection as `/health`. |

The response contains dependency names and boolean readiness values. It does
not expose credentials, source paths, host details, dashboard markup, or
metrics. Request bodies, response bodies, and HTTP timeouts are bounded by the
native API implementation.

Use `/live` for a process supervisor or container healthcheck. Use authenticated
`/health` for an operator readiness probe only after storage is configured.
Do not substitute liveness for readiness or disable bearer authentication to
make a failed dependency appear healthy.
