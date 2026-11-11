# Native systemd user service

`scripts/install-systemd.sh` installs the native `history-ragd` foreground
process as a per-user systemd unit from `~/Meridian/repos/ai-agent-history-rag-mcp` (`~/Meridian/repos/ai-agent-history-rag-mcp/bin/history-ragd`). By default on Velenza workstations it selects `CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE=device_service_account` with the host's own `kopia-<host>@jeeves-486102.iam.gserviceaccount.com` identity resolved from `~/.config/no13-backup/kopia-<host>.json` and includes `UnsetEnvironment=` in the unit to isolate the service from ambient session overrides. It is intentionally a production-only
installer: it refuses a local database, emulator, credential-file override,
public bind, unauthenticated mode, or a partial Spanner/Vertex configuration.

Build or provide an executable beneath this checkout, then run the installer
with the production environment already present. The installer writes:

- an owner-only config at `~/.config/ai-agent-history-rag/history-ragd.json`;
- an owner-only systemd environment file containing the service bearer secret;
- a user unit at `~/.config/systemd/user/ai-agent-history-rag.service`.

The daemon binds only `127.0.0.1:4680` on a host. `GET /live` is the
unauthenticated process/listener liveness endpoint. Authenticated `GET /health`
remains dependency readiness and returns 503 until both production Spanner and
the source watcher are ready.

Use `scripts/uninstall-systemd.sh` to stop and remove the service while keeping
durable auth and cursor state. Pass `--purge-state` only when that explicit
state deletion is intended.
