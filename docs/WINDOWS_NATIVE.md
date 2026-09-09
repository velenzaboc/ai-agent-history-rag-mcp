# Native Windows scheduled task

`scripts/install-windows.ps1` registers the native `history-ragd.exe` as a
per-user logon task. It accepts only the fixed production Spanner, Vertex,
impersonated-ADC, bearer-secret, and loopback listener contract. It rejects
local storage, emulators, credential-file overrides, public binds, and disabled
authentication.

The installer creates a user-ACL-restricted native config at
`%LOCALAPPDATA%\ai-agent-history-rag\history-ragd.json`. It records the
validated task environment in the current user's environment store so Task
Scheduler can launch the binary directly. The bearer secret is therefore never
placed in the task action command line.

`GET /live` is unauthenticated process/listener liveness at
`127.0.0.1:4680`. Authenticated `GET /health` is dependency readiness and
returns 503 until Spanner and the source watcher are ready. The task starts at
logon and retries non-successful exits three times at one-minute intervals.

Use `scripts/uninstall-windows.ps1` to unregister the task and remove native
config. Durable state and user task environment are retained unless
`-PurgeState` and/or `-PurgeEnvironment` is explicitly supplied.
