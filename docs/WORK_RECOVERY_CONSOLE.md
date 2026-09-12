# Work Recovery Console

The Work Recovery Console is the repository's visual, read-only layer over two
existing authorities:

- a task-graph MCP endpoint for tasks, hierarchy, dependencies, and durable work
  links;
- the history daemon compatibility API for recent sessions, search, and session
  summaries.

The history daemon was already a local API and MCP service. It was not a visual
dashboard. This command supplies that missing browser interface without creating
a second work-state database.

## What it shows

- A recovery inbox, sorted by configured severity and age, for abandoned or
  unlinked history, stale active threads, missing history references, ambiguous
  bindings, and active tasks without a durable work link.
- Status lanes whose grouping, order, colors, and active/terminal meaning come
  entirely from configuration.
- A task inventory with the task's source projection, owner, level, and work-link
  count.
- Milestone rollups derived from configured level names, parent relationships,
  and visible dependency edges.
- A bounded hierarchy and dependency graph prioritizing critical, gap, and
  well-connected nodes.
- Live history and file-change search.
- A copyable restart prompt that anchors resumption in the selected history
  summary first, followed by current task-graph context, dependencies, and
  durable work links.

The console never writes either source. A relationship is a scored display
projection, not a task-graph mutation or new authority.

## Build and run

Copy [`configs/work-recovery-console.example.json`](../configs/work-recovery-console.example.json)
outside the repository and replace its deployment values. Keep secrets out of
the JSON; bearer credentials are read from the configured environment variable.

```powershell
go build -o bin\work-recovery-console.exe .\cmd\work-recovery-console
$env:WORK_RECOVERY_HISTORY_TOKEN = "<history API bearer>"
.\bin\work-recovery-console.exe --config "C:\absolute\path\console.json"
```

```bash
go build -o bin/work-recovery-console ./cmd/work-recovery-console
export WORK_RECOVERY_HISTORY_TOKEN='<history API bearer>'
./bin/work-recovery-console --config /absolute/path/console.json
```

The config path must be absolute and point to a regular, non-link file. Unknown
fields, duplicate JSON keys, malformed URLs, non-loopback plain-HTTP endpoints,
inconsistent score thresholds, and invalid status references fail startup.

## Source and coverage behavior

The fleet read begins with one coherent `snapshot_tool` result. Because a large
project can hit that tool's configured cap, an optional `tasks_tool` overlay
refreshes every status in `active_group_ids`. The UI labels each task as
`snapshot`, `snapshot+active_overlay`, or `active_overlay` and always displays
both limits and returned counts.

An overlay-only task proves that the task exists now; it does not prove that the
capped coherent snapshot contains all of its work links or dependencies. The
console therefore does not classify an overlay-only task as unlinked. This keeps
the recovery inbox useful without turning a partial read into false absence
evidence.

Recent history is independently bounded by `recent_limit`. Session IDs found in
work links can be checked with `probe_limit`; setting that value to zero disables
the probes. Search results and recent results populate a bounded in-memory cache
so a restart prompt can still be assembled when an older compatibility endpoint
cannot retrieve the same session by exact ID.

## Configuration map

| Section | Controls |
|---|---|
| root | listener, base path, request timeout, shutdown timeout |
| `access` | optional bearer protection for the console itself; `none` requires a loopback listener |
| `fleet` | MCP URL, project and root scope, tool names, coherent/active limits, response bound |
| `history` | compatibility API URL and paths, auth environment name, search behavior, cache/probe/response bounds |
| `matching` | session-ID grammar, cross-machine path normalization, evidence weights, acceptance thresholds |
| `classification` | stale/abandoned ages plus finding labels, severities, and colors |
| `status` | lane taxonomy, active and terminal groups, milestone level names |
| `view` | title, wording, refresh/cache timing, display bounds, labels, and theme |

All environment-specific values belong in the deployment config or process
environment. No project ID, hostname, filesystem root, status vocabulary,
milestone vocabulary, label, color, scoring rule, or source endpoint is compiled
into the command.

## Private tailnet access

Keep `listen` on a loopback address and publish it with Tailscale Serve, not
Funnel. Current Tailscale Serve persists a background proxy across daemon and
machine restarts and applies the tailnet access-control policy:

```text
tailscale serve --bg http://127.0.0.1:<configured-port>
tailscale serve status --json
```

Serve terminates HTTPS at the Tailscale daemon. The backend remains loopback-only,
which also prevents a network peer from bypassing Serve or spoofing its identity
headers. See the current [Tailscale Serve documentation](https://tailscale.com/docs/features/tailscale-serve)
before changing the proxy configuration.

## Why this instead of Looker

Looker Studio now has a direct Cloud Spanner connector, so it remains useful for
ad hoc reporting. It is not the fast path for this console because the workflow
needs two live APIs, graph traversal, coverage semantics, and generation of a
history-first restart prompt. Looker (Google Cloud core) also remains a quoted
platform plus per-user product rather than a small internal utility. The custom
Go command adds no runtime dependency and leaves both authorities intact.

References: [Cloud Spanner connector for Looker Studio](https://cloud.google.com/looker/docs/studio/connect-to-google-cloud-spanner),
[Looker pricing](https://cloud.google.com/looker/pricing), and
[Cloud Run integration overview](https://docs.cloud.google.com/run/docs/overview/what-is-cloud-run).

## Verification

```bash
go test -race ./internal/recovery
go vet ./...
go build ./...
go test ./...
node --check internal/recovery/web/app.js
```

After starting a configured instance, verify `GET /healthz`, force one
`GET /api/dashboard?fresh=1`, exercise history search, open a recovery item, and
copy its restart prompt from a real browser.
