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

func TestServerServesDashboardAndEmbeddedApplication(t *testing.T) {
	cfg := mustTestConfig(t)
	application := stubApplication{dashboard: Dashboard{GeneratedAt: time.Now().UTC(), ProjectID: "project-a", View: cfg.View}}
	server, err := NewServer(cfg, application, nil)
	if err != nil {
		t.Fatal(err)
	}
	for requestPath, contentType := range map[string]string{
		"/":                  "text/html",
		"/assets/app.js":     "text/javascript",
		"/assets/styles.css": "text/css",
		"/api/dashboard":     "application/json",
	} {
		request := httptest.NewRequest(http.MethodGet, requestPath, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d body=%s", requestPath, response.Code, response.Body.String())
		}
		if !strings.Contains(response.Header().Get("Content-Type"), contentType) {
			t.Fatalf("GET %s content type = %q", requestPath, response.Header().Get("Content-Type"))
		}
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("GET %s did not include a content security policy", requestPath)
		}
	}
}

func TestServerSearchRejectsUnknownInputAndReturnsNormalizedBatch(t *testing.T) {
	cfg := mustTestConfig(t)
	application := stubApplication{search: HistoryBatch{Sessions: []SessionSummary{{SessionID: "s-1"}}, Returned: 1}}
	server, err := NewServer(cfg, application, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/history/search", strings.NewReader(`{"query":"recovery","limit":5,"surprise":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/history/search", strings.NewReader(`{"query":"recovery","limit":5}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", response.Code, response.Body.String())
	}
	var batch HistoryBatch
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil || len(batch.Sessions) != 1 {
		t.Fatalf("unexpected search response: %#v error=%v", batch, err)
	}
}

func TestServerRequiresConfiguredBearer(t *testing.T) {
	cfg := mustTestConfig(t)
	cfg.Access = AccessConfig{Mode: "bearer_env", BearerEnv: "TEST_CONSOLE_TOKEN"}
	t.Setenv("TEST_CONSOLE_TOKEN", "console-secret")
	server, err := NewServer(cfg, stubApplication{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Authorization", "Bearer console-secret")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d", response.Code)
	}
}

func TestServerReturnsExactTaskWorklinks(t *testing.T) {
	cfg := mustTestConfig(t)
	application := stubApplication{worklinks: TaskWorklinks{TaskID: "T-1", Worklinks: []Worklink{{TaskID: "T-1", ArtifactID: "A-1"}}}}
	server, err := NewServer(cfg, application, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/task/worklinks?task_id=T-1", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("worklinks status = %d body=%s", response.Code, response.Body.String())
	}
	var result TaskWorklinks
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.TaskID != "T-1" || len(result.Worklinks) != 1 {
		t.Fatalf("unexpected worklinks response: %#v error=%v", result, err)
	}
}

func TestServerReturnsTaskPromptAndProgramView(t *testing.T) {
	cfg := mustTestConfig(t)
	application := stubApplication{
		taskPrompt: TaskPrompt{TaskID: "T-1", Text: "execute T-1"},
		program:    ProgramView{Program: cfg.Programs[0], Counts: ProgramCounts{Lanes: 2}},
	}
	server, err := NewServer(cfg, application, nil)
	if err != nil {
		t.Fatal(err)
	}
	for requestPath, expected := range map[string]string{
		"/api/task/prompt?task_id=T-1": `"text":"execute T-1"`,
		"/api/program?id=delivery":     `"lanes":2`,
	} {
		request := httptest.NewRequest(http.MethodGet, requestPath, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("GET %s status=%d body=%s", requestPath, response.Code, response.Body.String())
		}
	}
}

type stubApplication struct {
	dashboard  Dashboard
	search     HistoryBatch
	worklinks  TaskWorklinks
	packet     ResumePacket
	taskPrompt TaskPrompt
	program    ProgramView
	err        error
}

func (s stubApplication) Dashboard(context.Context) (Dashboard, error) { return s.dashboard, s.err }
func (s stubApplication) Search(context.Context, HistorySearch) (HistoryBatch, error) {
	return s.search, s.err
}
func (s stubApplication) Worklinks(context.Context, string) (TaskWorklinks, error) {
	return s.worklinks, s.err
}
func (s stubApplication) ResumePacket(context.Context, string, string) (ResumePacket, error) {
	return s.packet, s.err
}
func (s stubApplication) TaskPrompt(context.Context, string) (TaskPrompt, error) {
	return s.taskPrompt, s.err
}
func (s stubApplication) Program(context.Context, string) (ProgramView, error) {
	return s.program, s.err
}
