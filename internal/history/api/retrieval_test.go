package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
)

type retrievalFake struct {
	query  store.Query
	filter store.Filter
	calls  int
	hybrid bool
	err    error
}

func (f *retrievalFake) Search(_ context.Context, q store.Query) ([]store.Result, error) {
	f.query = q
	f.calls++
	return []store.Result{{ID: "chunk", Content: "evidence", SessionID: "session", Timestamp: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC), SearchType: store.SearchTypeExact}}, f.err
}
func (f *retrievalFake) HybridSearch(ctx context.Context, q store.Query) ([]store.Result, error) {
	f.hybrid = true
	return f.Search(ctx, q)
}
func (f *retrievalFake) Summaries(ctx context.Context, filter store.Filter, limit int) ([]store.Result, error) {
	f.filter = filter
	return f.Search(ctx, store.Query{Limit: limit})
}

func TestRetrievalRoutesAuthenticateValidateAndPreserveFilters(t *testing.T) {
	f := &retrievalFake{}
	s, err := New(Config{AuthEnabled: true, Retrieval: f}, fakeVerifier{key: "secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/search", "/api/search/files", "/api/sessions"} {
		if got := request(t, s.Handler(), http.MethodPost, path, "", `{}`); got.Code != 401 {
			t.Fatalf("%s auth=%d", path, got.Code)
		}
	}
	if f.calls != 0 {
		t.Fatal("unauthorized request reached store")
	}
	r := request(t, s.Handler(), http.MethodPost, "/api/search", "Bearer secret", `{"query":"history","limit":2,"project_filter":"/project","date_from":"2026-09-01","date_to":"2026-09-16","enable_analysis":true}`)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"count":1`) || !strings.Contains(r.Body.String(), `"session_id":"session"`) {
		t.Fatalf("search=%d %s", r.Code, r.Body)
	}
	if !f.hybrid || f.query.Limit != 2 || f.query.Filter.ProjectPath != "/project" || f.query.Filter.DateTo.Hour() != 23 {
		t.Fatalf("lost filters: %#v", f.query)
	}
	r = request(t, s.Handler(), http.MethodPost, "/api/search/files", "Bearer secret", `{"file_path":"foo.go","operation_filter":"edit","query":"fix","limit":3}`)
	if r.Code != 200 || f.query.Filter.ChunkType != "file_change" || f.query.Filter.FilePath != "foo.go" || f.query.Filter.Operation != "edit" {
		t.Fatalf("files=%d %#v", r.Code, f.query)
	}
	r = request(t, s.Handler(), http.MethodPost, "/api/sessions", "Bearer secret", `{"session_id":"exact-session","project_filter":"/project","count":1}`)
	if r.Code != 200 || f.filter.SessionID != "exact-session" || !strings.Contains(r.Body.String(), `"summaries"`) {
		t.Fatalf("sessions=%d %#v %s", r.Code, f.filter, r.Body)
	}
}

func TestRetrievalRejectsInvalidInputBeforeStore(t *testing.T) {
	f := &retrievalFake{}
	s, _ := New(Config{Retrieval: f}, nil, nil)
	for _, body := range []string{`{}`, `null`, `{"query":"x","limit":0}`, `{"query":"x","limit":51}`, `{"query":"x","date_from":"yesterday"}`, `{"query":"x","date_from":"2026-09-17","date_to":"2026-09-16"}`, `{"query":"x"} rubbish`, `{"query":"x","unknown":1}`, `{"query":"x","query":"y"}`} {
		r := request(t, s.Handler(), http.MethodPost, "/api/search", "", body)
		if r.Code != 400 {
			t.Errorf("%s = %d %s", body, r.Code, r.Body)
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid request reached store")
	}
	f.err = errors.New("secret database details")
	r := request(t, s.Handler(), http.MethodPost, "/api/search", "", `{"query":"x"}`)
	if r.Code != 503 || strings.Contains(r.Body.String(), "secret") {
		t.Fatalf("error leaked: %d %s", r.Code, r.Body)
	}
	s, _ = New(Config{}, nil, nil)
	if r = request(t, s.Handler(), http.MethodPost, "/api/search", "", `{"query":"x"}`); r.Code != 503 {
		t.Fatalf("missing backend=%d", r.Code)
	}
}

func TestReadOnlyReadinessRequiresStoreAndNeverClaimsWatcher(t *testing.T) {
	s, err := New(Config{ReadOnly: true, AuthEnabled: true, Retrieval: &retrievalFake{}}, fakeVerifier{key: "secret"}, []Readiness{dependency{name: "store"}})
	if err != nil {
		t.Fatal(err)
	}
	r := request(t, s.Handler(), http.MethodGet, "/status", "Bearer secret", "")
	if r.Code != 200 || strings.Contains(r.Body.String(), "watcher") {
		t.Fatalf("read-only status=%d %s", r.Code, r.Body)
	}
	if _, err = New(Config{ReadOnly: true, Retrieval: &retrievalFake{}}, nil, nil); err == nil {
		t.Fatal("unauthenticated read-only accepted")
	}
}
