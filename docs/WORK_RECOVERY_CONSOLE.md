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
- A KANBAN screen whose columns, order, colors, and active/terminal meaning come
  entirely from configuration.
- Configured program pages backed by complete `execution_subtree` reads. Each
  page shows its expected lane roster, stage controls, live descendant work,
  dependency and work-link totals, and optional source-handover coordinates.
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
- A copyable task prompt on every task drawer, including tasks with no linked
  history session. It is built from that task's coherent
  `acceptance_dependency_closure`, exact durable work links, and the same
  multipass related-work packet shown in the drawer.
- Automatic related-work discovery whenever a task opens: exact bindings,
  structural closure, ranked task search, bounded artifact hydration, and
  conversation/file-history search. Candidate verdicts distinguish an active
  binding collision, reusable terminal work, and weaker related evidence.

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

Selecting a task calls the configured `worklinks_tool` for that exact task. The
drawer and restart-prompt path therefore use current durable work links even
when the task itself arrived only through the active overlay.

The same click also runs the configured related-work pipeline. It derives a
bounded query from configured task fields and stop words, excludes the selected
task itself, hydrates exact work links for only the highest-ranked configured
number of candidates, and searches conversation and file-change history in
parallel. Title overlap, note overlap, repository, parent, pillar, terminal
status, and exact shared artifacts are weighted only by configuration. An exact
shared artifact on another active task is surfaced as a collision. Terminal
work above the reuse threshold is surfaced as reusable; other candidates must
cross the related threshold. Failures remain visible per pass rather than being
misreported as an empty result.

"Combine" is intentionally a read-only work-family projection. The console
never merges tasks or rewrites their status: two similar titles can still have
different acceptance contracts, and cognition does not own authoritative task
mutation. Exact stable bindings tell an agent to resume; ranked similarity tells
it what to inspect or reuse.

The task-prompt path does not depend on a history match. It requests a coherent
`acceptance_dependency_closure` rooted at the selected task, bounded by
`fleet.task_prompt_limit`, and combines the root task contract, context tasks,
dependency edges, revision evidence, and exact work links into a deterministic
prompt. The receiving agent is instructed to keep the stable task identity and
resume an existing binding before creating work.

Each `programs` entry is a lazy-loaded page. The console requests an
`execution_subtree` rooted at its configured `root_task_id`, then chooses lane
roots by immediate parent plus `lane_task_id_prefix`. Stage rows are recognized
only by the configured stage prefixes. `expected_lane_count` is displayed as a
completeness check; a mismatch or capped/incomplete source read is visible and
is never silently treated as complete. Program-specific titles, thread IDs,
checkpoint references, prefixes, and counts belong only in deployment config.

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
| `fleet` | MCP URL, project and root scope, snapshot/active/exact-worklink/search tool names, coherent/active/task-prompt limits, response bound |
| `history` | compatibility API URL and paths, auth environment name, search behavior, cache/probe/response bounds |
| `matching` | session-ID grammar, cross-machine path normalization, evidence weights, acceptance thresholds |
| `discovery` | query fields and stop words, task/history/hydration limits, concurrency, scoring weights, thresholds, and verdict labels/colors |
| `classification` | stale/abandoned ages plus finding labels, severities, and colors |
| `programs` | zero or more live subtree pages: navigation text, root and lane identity, expected lane count, stage prefixes, source-thread coordinates, and subtree limit |
| `status` | lane taxonomy, active and terminal groups, milestone level names |
| `view` | title, wording, refresh/cache timing, display bounds, labels, and theme |

All environment-specific values belong in the deployment config or process
environment. No project ID, hostname, filesystem root, task or thread ID,
program root, lane roster, status vocabulary, milestone vocabulary, label,
color, scoring rule, or source endpoint is compiled into the command.

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
`GET /api/dashboard?fresh=1`, load every configured `GET /api/program?id=...`,
exercise history search, open `GET /api/task/related?task_id=...` for a task with
known bindings and one without, open a KANBAN task in a real browser, follow a
related-task candidate, and copy both task and restart prompts.
