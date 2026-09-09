# Claude History RAG MCP Server
# Multi-stage build for minimal image size

FROM golang:1.27-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/history-ragd ./cmd/history-ragd

FROM busybox:1.37.0-musl AS runtime

WORKDIR /app
COPY --from=build /out/history-ragd /app/history-ragd
RUN mkdir -p /data/db /data/state /data/logs /data/sources/claude /data/sources/codex /data/sources/gemini /data/sources/antigravity /data/sources/chatgpt /data/sources/claude-app \
    && chmod 0700 /data/state \
    && printf '%s\n' '{"state_dir":"/data/state","listen":"0.0.0.0:4680","container_mode":true,"watch_roots":["/data/sources/claude","/data/sources/codex","/data/sources/gemini","/data/sources/antigravity","/data/sources/chatgpt","/data/sources/claude-app"],"pid_file":"/data/state/daemon.pid","auth_state_file":"/data/state/auth.json","auth_enabled":true,"checkout_root":"/app","executable":"/app/history-ragd"}' > /app/history-ragd.json \
    && chmod 0600 /app/history-ragd.json \
    && chown -R 65532:65532 /app /data

# Default environment variables for server mode
ENV CLAUDE_HISTORY_RAG_STATE_PATH=/data/state/state.json \
    CLAUDE_HISTORY_RAG_STATUS_SERVER_HOST=0.0.0.0 \
    CLAUDE_HISTORY_RAG_STATUS_SERVER_PORT=4680 \
    CLAUDE_HISTORY_RAG_LOG_LEVEL=INFO

# Expose the API/dashboard port
EXPOSE 4680

HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD wget -q -T 5 -O /dev/null http://127.0.0.1:4680/live

USER 65532:65532
CMD ["/app/history-ragd", "start", "--config", "/app/history-ragd.json"]
