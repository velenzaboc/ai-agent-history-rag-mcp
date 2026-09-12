package recovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMCPFleetClientParsesStreamableHTTPEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Accept") != "application/json, text/event-stream" {
			t.Fatalf("unexpected request: %s accept=%q", r.Method, r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"project_id\":\"project-a\",\"read_timestamp\":\"2026-09-11T10:00:00Z\",\"revision\":\"r1\",\"tasks\":[{\"task_id\":\"T-1\",\"title\":\"Task one\",\"status\":\"in_progress\",\"level\":\"task\"}],\"dependencies\":[],\"worklinks\":[]}}}\n\n"))
	}))
	defer server.Close()

	client, err := NewMCPFleetClient(FleetConfig{Mode: "mcp_http", Endpoint: server.URL, ProjectID: "project-a", SnapshotTool: "snapshot", Scope: "project", Limit: 10, MaxResponseBytes: 1 << 20}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].TaskID != "T-1" || snapshot.Revision != "r1" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestLegacyHistoryClientUsesConfiguredBearerAndNormalizesSummaries(t *testing.T) {
	t.Setenv("TEST_HISTORY_TOKEN", "secret-value")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-value" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/sessions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"summaries": []map[string]any{{"id": "chunk-1", "content": "durable summary", "chunk_type": "summary", "session_id": "00000000-0000-0000-0000-000000000001", "project_path": "/work/repo", "project_name": "repo", "timestamp": "2026-09-10T10:00:00Z", "machine_id": "host-a"}}, "count": 1})
		case "/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "healthy"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewLegacyHistoryClient(HistoryConfig{Mode: "legacy_http", Endpoint: server.URL, StatusPath: "/status", SessionsPath: "/sessions", SearchPath: "/search", FilesPath: "/files", Auth: UpstreamAuthConfig{Mode: "bearer_env", BearerEnv: "TEST_HISTORY_TOKEN"}, RecentLimit: 10, SearchLimit: 10, SessionCacheLimit: 100, ProbeLimit: 10, ProbeConcurrency: 2, MaxResponseBytes: 1 << 20}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	recent, err := client.Recent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recent.Sessions) != 1 || recent.Sessions[0].Summary != "durable summary" || recent.Sessions[0].MachineID != "host-a" {
		t.Fatalf("unexpected sessions: %#v", recent)
	}
}
