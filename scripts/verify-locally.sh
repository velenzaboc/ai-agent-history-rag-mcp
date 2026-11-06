#!/usr/bin/env bash
# Run only through the native arcserver3 resource guard in an owned run workspace.
set -euo pipefail
if [[ "${1-}" == --help ]]; then
  printf '%s\n' 'Run: python3 /home/lab/Meridian/bin/velenza-preci.py --cwd <unique-run-checkout> -- bash scripts/verify-locally.sh'
  exit 0
fi
[[ $# == 0 && "$(hostname -s)" == arcserver3 ]] || { printf '%s\n' 'verification requires arcserver3 and its native resource guard' >&2; exit 64; }
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$root"
[[ ! -e .local-verification ]] || { printf '%s\n' 'verification output already exists; select a clean unique run workspace' >&2; exit 64; }
evidence_dir="$root/.local-verification"
mkdir -m 700 "$evidence_dir"
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
