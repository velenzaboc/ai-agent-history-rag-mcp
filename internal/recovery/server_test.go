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

type stubApplication struct {
	dashboard Dashboard
	search    HistoryBatch
	packet    ResumePacket
	err       error
}

func (s stubApplication) Dashboard(context.Context) (Dashboard, error) { return s.dashboard, s.err }
func (s stubApplication) Search(context.Context, HistorySearch) (HistoryBatch, error) {
	return s.search, s.err
}
func (s stubApplication) ResumePacket(context.Context, string, string) (ResumePacket, error) {
	return s.packet, s.err
}
