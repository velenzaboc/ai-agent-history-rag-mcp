#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
README="$ROOT_DIR/README.md"
ENV_EXAMPLE="$ROOT_DIR/.env.docker.example"
DOCS=("$README" "$ENV_EXAMPLE" "$ROOT_DIR"/docs/*.md)
PASS_COUNT=0

pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1" >&2; exit 1; }
require_literal() { grep -Fq -- "$2" "$1" || fail "$3: missing $2"; }
require_absent() { grep -Eqi -- "$2" "$1" && fail "$3: forbidden $2" || true; }

for file in "${DOCS[@]}"; do
  [[ -r "$file" ]] || fail "operator document missing: $file"
  require_absent "$file" '(^|[^[:alnum:]_])(python|uv|pip|pytest|ruff|lancedb|fastmcp|aiohttp|watchfiles)([^[:alnum:]_]|$)' "retired operator guidance"
  require_absent "$file" 'ai-agent-history-rag-daemon|claude_history_rag\.' "retired operator entrypoint"
done
pass "operator documents contain no retired runtime guidance"

for literal in \
  'history-ragd start --config /app/history-ragd.json' \
  'scripts/install-launchd.sh' \
  'scripts/install-systemd.sh' \
  'scripts/install-windows.ps1' \
  'scripts/history-rag-mcp-native.sh' \
  'docker build --tag history-ragd:local .' \
  'CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build'; do
  require_literal "$README" "$literal" "native entrypoint inventory"
done
pass "README inventories every supported native entrypoint"

for literal in \
  'CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT=production' \
  'CLAUDE_HISTORY_RAG_STORAGE_BACKEND=spanner' \
  'CLAUDE_HISTORY_RAG_SPANNER_EMBEDDING_MODEL_ID=ConversationEmbeddingModel' \
  'CLAUDE_HISTORY_RAG_EMBEDDING_MODEL=gemini-embedding-001' \
  'CLAUDE_HISTORY_RAG_EMBEDDING_DIMENSION=3072' \
  'CLAUDE_HISTORY_RAG_CREDENTIALS_PROFILE=impersonated_service_account' \
  'CLAUDE_HISTORY_RAG_SERVER_PSK=<bearer-secret>'; do
  require_literal "$README" "$literal" "production contract"
  require_literal "$ENV_EXAMPLE" "$literal" "container example contract"
done
pass "README and container example agree on production contract"

tilde='~'
for literal in \
  "$tilde/.claude/projects" \
  "$tilde/.codex/sessions" \
  "$tilde/.gemini/tmp" \
  "$tilde/.gemini/antigravity" \
  "$tilde/.claude-history-rag/imports/chatgpt" \
  "$tilde/.claude-history-rag/imports/claude-app"; do
  require_literal "$README" "$literal" "six-source root inventory"
done
pass "README names every native watcher root"

require_literal "$README" "\`GET /live\` is intentionally unauthenticated" "liveness semantics"
require_literal "$README" "\`GET /health\` and \`GET /status\` require a bearer credential" "readiness authentication"
require_literal "$README" 'otherwise 503' "readiness failure semantics"
require_literal "$README" 'not a supported native production operator entrypoint' "Compose boundary"
pass "README distinguishes liveness, readiness, and compatibility boundary"

require_literal "$ROOT_DIR/Dockerfile" 'CMD ["/app/history-ragd", "start", "--config", "/app/history-ragd.json"]' "Docker native command"
require_literal "$ROOT_DIR/Dockerfile" 'http://127.0.0.1:4680/live' "Docker liveness probe"
require_literal "$ROOT_DIR/scripts/install-systemd.sh" 'history-ragd binary must be' "systemd native binary"
require_literal "$ROOT_DIR/scripts/install-launchd.sh" 'history-ragd binary must be' "launchd native binary"
require_literal "$ROOT_DIR/scripts/install-windows.ps1" 'history-ragd binary does not exist' "Windows native binary"
pass "documented commands have native implementation witnesses"

printf 'PASS total=%d\n' "$PASS_COUNT"
