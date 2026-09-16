# Native history search verification — 2026-09-16

Task: HARNESS-NEW-SESSION-VALIDATION-20260916

The selected outcome is conversation, file-change and session retrieval through
the existing MCP clients on both Macs, using each host's existing `kopia-*`
device source. This is developer history tooling; no kernel execution,
admission, embedding migration or schema change is involved.

## Implemented and deployed

Runtime source: `61a97117e5b7cf3e33c04819522eb6acc20e7bc8`.
Additional lifecycle and credential tests: `a10b6ca`.
Both hosts run the same macOS arm64 executable, SHA-256:
`e5efdd09756073b762b13c1e27a3099307fb1f3704bb6e30d377d49111088379`.

| Host | Installed runtime | Google identity |
|---|---|---|
| Mac mini | `/Users/brandon/.local/share/history-native-search/61a9711` | `kopia-macmini@jeeves-486102.iam.gserviceaccount.com` |
| MacBook | `/Users/brandonmeyer/Meridian/runtime/history-native-search/61a9711` | `kopia-macbook@jeeves-486102.iam.gserviceaccount.com` |

The separate `com.ai-agent-history-rag.search` LaunchAgent binds authenticated
read-only retrieval to `127.0.0.1:4681`. It uses its own private state directory.
The existing ingestion agent, its configuration and its credential carrier
were retained. The search process performs no ingestion, corpus writes,
re-embedding, DDL, key creation or IAM changes.

The Mac mini executable is installed on its internal disk: launchd's loader
stalled before entering the program when it opened the executable from the
external Meridian volume. Installing the exact same binary internally resolved
that startup failure.

Client entries in `.claude.json` and `.codex/config.toml` select the native
proxy, read-only port and exact local device identity. An existing Claude
Desktop history connector on the Mac mini was aligned too. The MacBook uses
its existing Desktop Code session bridge; no new Chat connector was invented.
History client entries explicitly clear inherited credential-file overrides.
Unrelated client entries and Codex hook trust were preserved.

## Operational checks

Fresh stdio MCP processes launched from each host's actual client entry passed
initialization, catalog enumeration and all five exposed tools:

| Tool | Mac mini | MacBook |
|---|---|---|
| `get_server_status` basic | ready | ready |
| `search_conversations` (Velenza, limit 1) | 1 result | 1 result |
| `search_file_changes` (file changes, limit 1) | 1 result | 1 result |
| `get_session_summary` (count 1) | 1 summary | 1 summary |
| `get_index_status` | ready | ready |

Direct HTTP positive controls also returned 200 on all five routes on both
hosts. Unauthenticated search returned 401 on both. Session retrieval took
approximately 24 seconds on Mac mini and 30 seconds on MacBook; conversation
search took approximately 10 and 5 seconds respectively. These are bounded
observations, not latency guarantees.

Claude Desktop's existing validation session
`local_a00c9f05-babe-4473-9a87-009fb935f2e6` passed all five actual MCP calls.
The parent inspected the tool-result rows in its underlying session transcript
`6d978bc1-1f8b-4bf3-8baa-a8b747a76144`, not only the model's summary.

A fresh Codex Desktop session, `01a0ac4c-53b1-7e53-afc0-aa1ba6c63257`, also
passed all five actual MCP tools, with one conversation, one file change and
one summary. The older pre-repair Codex session retained its existing MCP
process and reproduced the old error. Already-running clients need a new
session or MCP reconnection to consume the new configuration. No unrelated
user sessions were terminated. Full MacBook Desktop model sessions were not
re-run; its actual configured fresh MCP process was exercised successfully.

Before/after SHA-256 comparison proved the well-known ADC carrier, ingestion
LaunchAgent plist and legacy bearer-state file were unchanged on each host.
No credential values or retrieved transcript contents are included here.

## Validation and security boundaries

- Full Go suite and race suite passed; vet passed.
- Required coverage gate: 534 tests, zero skips, zero failures, all 26 packages
  at or above the unchanged 85% floor.
- Native MCP fixture: 39 passes. Launchd fixture: 4 passes. Documentation
  fixture: 6 passes. Four MCP security mutations were killed.
- Docker, systemd, Windows static/cross-build and CI concurrency fixtures passed.
  Windows PowerShell execution and Linux systemd runtime validation were not
  represented as macOS runtime tests.
- The live read test is opt-in with the `integration` build tag and
  `HISTORY_RAG_LIVE_READ_CHECK=true`; it performs no writes.
- Independent frozen review of runtime source `61a9711` returned PASS with
  zero blocking findings. Later source changes are tests and this report.

The device profile validates the owner-only well-known carrier and binds its
nested service-account identity exactly. It neither changes the carrier nor
impersonates the unrelated target carried for another application. Duplicate
fields, redirected endpoints, user-source substitution and ambient transport
or post-quantum downgrade overrides remain rejected.

Current official Google Cloud KMS algorithm and launch-stage documentation was
checked for the security audit. This repair introduces no KMS, load-balancer,
key-import, TLS algorithm or signing-algorithm configuration. Existing Google
IAM device-key authentication is an external-provider compatibility boundary;
it is not claimed to be post-quantum. No new keys or fallback identities were
introduced, and no pre-GA feature was selected. References:
[KMS algorithms](https://docs.cloud.google.com/kms/docs/algorithms),
[quantum-safe import](https://docs.cloud.google.com/kms/docs/quantum-safe-key-import),
[Google service-account authentication](https://cloud.google.com/iam/docs/service-account-creds).

The earlier harness delivery is
[no13-infra PR 2614](https://github.com/velenzaboc/no13-infra/pull/2614).
Its frozen report records the original failure; this report records the repair.
