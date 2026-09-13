package recovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

	client, err := NewMCPFleetClient(FleetConfig{Mode: "mcp_http", Endpoint: server.URL, ProjectID: "project-a", SnapshotTool: "snapshot", WorklinksTool: "worklinks", SearchTool: "search", Scope: "project", Limit: 10, TaskPromptLimit: 10, SessionLinkLimit: 10, SessionLinkConcurrency: 2, MaxResponseBytes: 1 << 20}, nil, 2*time.Second)
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

func TestMCPFleetClientOverlaysActiveTasksOutsideBoundedSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int64 `json:"id"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var result any
		switch request.Params.Name {
		case "snapshot":
			result = FleetSnapshot{ProjectID: "project-a", Tasks: []Task{{TaskID: "T-1", Title: "Old title", Status: "in_progress"}}}
		case "tasks":
			statuses, ok := request.Params.Arguments["statuses"].([]any)
			if !ok || len(statuses) != 2 {
				t.Fatalf("active statuses were not configured: %#v", request.Params.Arguments)
			}
			result = taskListResult{Tasks: []Task{{ProjectID: "project-a", TaskID: "T-1", Title: "Current title", Status: "in_progress"}, {ProjectID: "project-a", TaskID: "T-2", Title: "Outside cap", Status: "blocked"}}}
		default:
			t.Fatalf("unexpected tool %q", request.Params.Name)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"structuredContent": result}})
	}))
	defer server.Close()

	client, err := NewMCPFleetClient(FleetConfig{Mode: "mcp_http", Endpoint: server.URL, ProjectID: "project-a", SnapshotTool: "snapshot", TasksTool: "tasks", WorklinksTool: "worklinks", SearchTool: "search", Scope: "project", Limit: 1, ActiveLimit: 10, TaskPromptLimit: 10, SessionLinkLimit: 10, SessionLinkConcurrency: 2, MaxResponseBytes: 1 << 20}, []string{"in_progress", "blocked"}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SnapshotTasks != 1 || snapshot.ActiveTasks != 2 || len(snapshot.Tasks) != 2 {
		t.Fatalf("unexpected overlay coverage: %#v", snapshot)
	}
	if snapshot.Tasks[0].Title != "Current title" || snapshot.Tasks[0].Projection != "snapshot+active_overlay" || snapshot.Tasks[1].Projection != "active_overlay" {
		t.Fatalf("unexpected overlay merge: %#v", snapshot.Tasks)
	}
}

func TestMCPFleetClientLoadsExactTaskWorklinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int64 `json:"id"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Params.Name != "worklinks" || request.Params.Arguments["task_id"] != "T-1" {
			t.Fatalf("unexpected exact worklink request: %#v", request.Params)
		}
		result := map[string]any{"worklinks": []Worklink{{ProjectID: "project-a", TaskID: "T-1", ArtifactID: "A-1", ArtifactType: "pr", ArtifactRef: "https://example.invalid/pr/1"}}}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"structuredContent": result}})
	}))
	defer server.Close()

	client, err := NewMCPFleetClient(FleetConfig{Mode: "mcp_http", Endpoint: server.URL, ProjectID: "project-a", SnapshotTool: "snapshot", WorklinksTool: "worklinks", SearchTool: "search", Scope: "project", Limit: 10, TaskPromptLimit: 10, SessionLinkLimit: 10, SessionLinkConcurrency: 2, MaxResponseBytes: 1 << 20}, nil, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	links, err := client.Worklinks(context.Background(), "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ArtifactID != "A-1" {
		t.Fatalf("unexpected exact worklinks: %#v", links)
	}
}

func TestMCPFleetClientLoadsExactSessionWorklinksFromSearch(t *testing.T) {
	const sessionID = "00000000-0000-0000-0000-000000000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int64 `json:"id"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Params.Name != "search" || request.Params.Arguments["query"] != sessionID || request.Params.Arguments["limit"] != float64(10) {
			t.Fatalf("unexpected session worklink request: %#v", request.Params)
		}
		if _, supplied := request.Params.Arguments["node_kind"]; supplied {
			t.Fatalf("session worklink search must include artifact nodes: %#v", request.Params.Arguments)
		}
		result := map[string]any{"nodes": []any{
			map[string]any{
				"node_id": "A-1", "node_kind": "artifact", "project_id": "project-a",
				"current_version": map[string]any{
					"contract": map[string]any{"artifact_ref": "commit-1", "artifact_type_kind": "commit", "project_id": "project-a"},
					"payload":  map[string]any{"task_id": "T-1", "thread": "GRAPHTRUTH", "session_id": sessionID, "note": "historical session", "project_id": "project-a", "fleet_created_at": "2026-09-11T10:00:00Z"},
				},
			},
			map[string]any{
				"node_id": "A-DECOY", "node_kind": "artifact", "project_id": "project-a",
				"current_version": map[string]any{
					"contract": map[string]any{"artifact_ref": "different", "artifact_type_kind": "finding", "project_id": "project-a"},
					"payload":  map[string]any{"task_id": "T-2", "thread": "different", "note": sessionID, "project_id": "project-a"},
				},
			},
			map[string]any{"node_id": "T-1", "node_kind": "task", "project_id": "project-a", "current_version": map[string]any{"payload": map[string]any{"task_id": "T-1"}}},
		}}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"structuredContent": result}})
	}))
	defer server.Close()

	client, err := NewMCPFleetClient(FleetConfig{Mode: "mcp_http", Endpoint: server.URL, ProjectID: "project-a", SnapshotTool: "snapshot", WorklinksTool: "worklinks", SearchTool: "search", Scope: "project", Limit: 10, TaskPromptLimit: 10, SessionLinkLimit: 10, SessionLinkConcurrency: 2, MaxResponseBytes: 1 << 20}, nil, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	links, err := client.SessionWorklinks(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ArtifactID != "A-1" || links[0].TaskID != "T-1" || links[0].ArtifactType != "commit" || links[0].Thread != "GRAPHTRUTH" || links[0].SessionID != sessionID {
		t.Fatalf("unexpected exact session worklinks: %#v", links)
	}
}

func TestMCPFleetClientRefusesCappedSessionWorklinkSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID int64 `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		result := map[string]any{"nodes": []any{
			map[string]any{"node_id": "A-1", "node_kind": "artifact", "project_id": "project-a"},
		}}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"structuredContent": result}})
	}))
	defer server.Close()

	client, err := NewMCPFleetClient(FleetConfig{Mode: "mcp_http", Endpoint: server.URL, ProjectID: "project-a", SnapshotTool: "snapshot", WorklinksTool: "worklinks", SearchTool: "search", Scope: "project", Limit: 10, TaskPromptLimit: 10, SessionLinkLimit: 1, SessionLinkConcurrency: 1, MaxResponseBytes: 1 << 20}, nil, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SessionWorklinks(context.Background(), "00000000-0000-0000-0000-000000000001"); err == nil || !strings.Contains(err.Error(), "exact coverage is unknown") {
		t.Fatalf("capped exact search did not fail closed: %v", err)
	}
}

func TestMCPFleetClientSearchesConfiguredTaskTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int64 `json:"id"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Params.Name != "search" || request.Params.Arguments["query"] != "recovery console" || request.Params.Arguments["node_kind"] != "task" || request.Params.Arguments["limit"] != float64(7) {
			t.Fatalf("unexpected task search request: %#v", request.Params)
		}
		result := map[string]any{"nodes": []any{map[string]any{
			"node_id": "T-2", "project_id": "project-a",
			"current_version": map[string]any{"payload": map[string]any{"title": "Prior recovery console", "status": "complete", "pillar": "delivery"}},
		}}}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"structuredContent": result}})
	}))
	defer server.Close()

	client, err := NewMCPFleetClient(FleetConfig{Mode: "mcp_http", Endpoint: server.URL, ProjectID: "project-a", SnapshotTool: "snapshot", WorklinksTool: "worklinks", SearchTool: "search", Scope: "project", Limit: 10, TaskPromptLimit: 10, SessionLinkLimit: 10, SessionLinkConcurrency: 2, MaxResponseBytes: 1 << 20}, nil, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := client.Search(context.Background(), "recovery console", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != "T-2" || tasks[0].ProjectID != "project-a" || tasks[0].Status != "complete" {
		t.Fatalf("unexpected task search result: %#v", tasks)
	}
}

func TestMCPFleetClientLoadsConfiguredScopedSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int64 `json:"id"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Params.Name != "snapshot" || request.Params.Arguments["scope"] != "execution_subtree" || request.Params.Arguments["root_task_id"] != "ROOT-1" || request.Params.Arguments["limit"] != float64(250) {
			t.Fatalf("unexpected scoped snapshot request: %#v", request.Params)
		}
		result := FleetSnapshot{ProjectID: "project-a", Revision: "r-program", Tasks: []Task{{ProjectID: "project-a", TaskID: "ROOT-1"}}}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"structuredContent": result}})
	}))
	defer server.Close()

	client, err := NewMCPFleetClient(FleetConfig{Mode: "mcp_http", Endpoint: server.URL, ProjectID: "project-a", SnapshotTool: "snapshot", WorklinksTool: "worklinks", SearchTool: "search", Scope: "project", Limit: 10, TaskPromptLimit: 10, SessionLinkLimit: 10, SessionLinkConcurrency: 2, MaxResponseBytes: 1 << 20}, nil, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.ScopedSnapshot(context.Background(), "execution_subtree", "ROOT-1", 250)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != "r-program" || snapshot.SnapshotTasks != 1 {
		t.Fatalf("unexpected scoped snapshot: %#v", snapshot)
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
