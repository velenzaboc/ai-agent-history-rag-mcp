#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOCKERFILE="$ROOT_DIR/Dockerfile"
PASS_COUNT=0

pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1" >&2; exit 1; }
require_literal() { grep -Fq -- "$2" "$1" || fail "$3: missing $2"; }
require_absent() { grep -Eqi -- "$2" "$1" && fail "$3: forbidden $2" || true; }

[[ -r "$DOCKERFILE" ]] || fail "container Dockerfile is missing"
for literal in \
  'FROM golang:1.27-bookworm AS build' \
  'CGO_ENABLED=0 GOOS=linux go build' \
  'FROM busybox:1.37.0-musl AS runtime' \
  'COPY --from=build /out/history-ragd /app/history-ragd' \
  'USER 65532:65532' \
  '"container_mode":true' \
  '"auth_enabled":true' \
  'CMD ["/app/history-ragd", "start", "--config", "/app/history-ragd.json"]' \
  'http://127.0.0.1:4680/live' \
  'EXPOSE 4680'; do
  require_literal "$DOCKERFILE" "$literal" "native image contract"
done
require_absent "$DOCKERFILE" '(^|[^[:alnum:]_])(python|uv)([^[:alnum:]_]|$)' "container runtime"
require_absent "$DOCKERFILE" 'pyproject|uv\.lock|COPY src/' "container build inputs"
pass "image executes the authenticated native daemon only"

printf 'PASS total=%d\n' "$PASS_COUNT"
