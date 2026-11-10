#!/usr/bin/env bash
# Run only through the native remote resource guard in an owned verification workspace.
set -euo pipefail
if [[ "${1-}" == --help ]]; then
  printf '%s\n' 'Run: /home/lab/Meridian/bin/velenza-remote-guard.py --host <arcserver1|arcserver2|arcserver4> -- bash scripts/verify-locally.sh'
  exit 0
fi
[[ $# == 0 && -n "${VELENZA_VERIFY_HOST:-}" && "$(hostname -s)" == "$VELENZA_VERIFY_HOST" && "$(hostname -s)" != arcserver3 ]] || { printf '%s\n' 'verification requires its designated guarded remote host (never arcserver3)' >&2; exit 64; }
VELENZA_VERIFY_SCOPE_PREFIX="${VELENZA_VERIFY_SCOPE_PREFIX:-velenza-remote}"
export VELENZA_VERIFY_SCOPE_PREFIX
python3 - <<'PY' >/dev/null
import os, pathlib, re
prefix = os.environ['VELENZA_VERIFY_SCOPE_PREFIX']
if not re.fullmatch(r'[A-Za-z0-9_-]+', prefix):
    raise SystemExit('Resource-guard scope prefix is malformed')
line = next(x[3:] for x in pathlib.Path('/proc/self/cgroup').read_text().splitlines() if x.startswith('0::'))
name = pathlib.PurePosixPath(line).name
if not re.fullmatch(re.escape(prefix) + r'-[0-9]+-[a-f0-9]+\.scope', name):
    raise SystemExit('Native resource-guard scope is required')
base = pathlib.Path('/sys/fs/cgroup') / line.lstrip('/')
quota, period = (base / 'cpu.max').read_text().split()
if quota == 'max' or not 0 < int(quota) / int(period) <= 4:
    raise SystemExit('CPU cap is required')
if not 0 < int((base / 'memory.max').read_text()) <= 8 * 1024 ** 3:
    raise SystemExit('RAM cap is required')
if int((base / 'memory.swap.max').read_text()) != 0:
    raise SystemExit('No-swap cap is required')
PY
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$root"
if [[ -n "${VERIFY_EVIDENCE_DIR-}" ]]; then
  [[ "$VERIFY_EVIDENCE_DIR" == /* ]] || { printf '%s\n' 'evidence path must be absolute' >&2; exit 64; }
  evidence_parent="$(cd "$(dirname "$VERIFY_EVIDENCE_DIR")" && pwd -P)"
  evidence_name="$(basename "$VERIFY_EVIDENCE_DIR")"
  [[ "$evidence_name" != . && "$evidence_name" != .. && "$evidence_parent" != "$root" && "$evidence_parent" != "$root/"* ]] || { printf '%s\n' 'evidence must remain outside the source checkout' >&2; exit 64; }
  evidence_dir="$evidence_parent/$evidence_name"
  [[ ! -e "$evidence_dir" && ! -L "$evidence_dir" ]] || { printf '%s\n' 'evidence output already exists' >&2; exit 64; }
  mkdir -m 700 "$evidence_dir"
else
  evidence_parent="$(cd "${TMPDIR:-/tmp}" && pwd -P)"
  [[ "$evidence_parent" != "$root" && "$evidence_parent" != "$root/"* ]] || { printf '%s\n' 'temporary evidence must remain outside the source checkout' >&2; exit 64; }
  evidence_dir="$(mktemp -d "$evidence_parent/history-rag-verification.XXXXXXXX")"
fi
cleanup() {
  rm -rf -- "$evidence_dir"
}
trap cleanup EXIT
export GOMAXPROCS=4
export GOFLAGS=-p=4
bash scripts/go-coverage-gate.test.sh
bash scripts/go-coverage-gate.sh
go vet ./...
go build ./...
bash -n scripts/history-rag-mcp-native.sh
bash scripts/history-rag-mcp-native.test.sh
bash scripts/history-rag-mcp-native.mutation.test.sh
bash scripts/docker-native.test.sh
bash scripts/systemd-native.test.sh
bash scripts/windows-native.test.sh
bash scripts/docs-native-contract.test.sh
printf '%s\n' 'Local verification completed; preserve this run evidence before removing the owned workspace.'
