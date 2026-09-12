package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	historyauth "github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/auth"
)

type fakeVerifier struct{ key string }

func (f fakeVerifier) VerifyBearer(header string) (historyauth.KeyKind, error) {
	if header != "Bearer "+f.key {
		return "", historyauth.ErrInvalidCredential
	}
	return historyauth.ActiveKey, nil
}

type dependency struct {
	name string
	err  error
}

func (d dependency) Name() string                { return d.name }
func (d dependency) Ready(context.Context) error { return d.err }

func request(t *testing.T, handler http.Handler, method, path, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestReadinessFailsClosedUntilAllDependenciesAttach(t *testing.T) {
	server, err := New(Config{AuthEnabled: false}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, server.Handler(), http.MethodGet, "/health", "", "")
	if response.Code != http.StatusServiceUnavailable || response.Body.Len() > MaxResponseBytes {
		t.Fatalf("health = %d %q", response.Code, response.Body.String())
	}
	live := request(t, server.Handler(), http.MethodGet, "/live", "", "")
	if live.Code != http.StatusOK || live.Body.String() != "{\"status\":\"live\"}\n" {
		t.Fatalf("live = %d %q", live.Code, live.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "not_ready" {
		t.Fatalf("status = %#v", payload)
	}

	server, err = New(Config{AuthEnabled: false}, nil, []Readiness{
		dependency{name: "store", err: nil},
		dependency{name: "watcher", err: errors.New("not attached")},
	})
	if err != nil {
		t.Fatal(err)
	}
	response = request(t, server.Handler(), http.MethodGet, "/status", "", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d", response.Code)
	}

	server, err = New(Config{AuthEnabled: false}, nil, []Readiness{
		dependency{name: "watcher"}, dependency{name: "store"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response = request(t, server.Handler(), http.MethodGet, "/health", "", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ready"`) {
		t.Fatalf("ready health = %d %q", response.Code, response.Body.String())
	}
}

func TestReadinessRequiresExactClosedDependencyRoster(t *testing.T) {
	for _, dependencies := range [][]Readiness{
		{dependency{name: "store"}},
		{dependency{name: "watcher"}},
	} {
		server, err := New(Config{AuthEnabled: false}, nil, dependencies)
		if err != nil {
			t.Fatal(err)
		}
		response := request(t, server.Handler(), http.MethodGet, "/health", "", "")
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"status":"not_ready"`) {
			t.Fatalf("incomplete roster returned %d %q", response.Code, response.Body.String())
		}
	}

	if _, err := New(Config{AuthEnabled: false}, nil, []Readiness{dependency{name: "other"}}); err == nil {
		t.Fatal("unknown readiness dependency was accepted")
	}
	if _, err := New(Config{AuthEnabled: false}, nil, []Readiness{
		dependency{name: "store"}, dependency{name: "store"},
	}); err == nil {
		t.Fatal("duplicate readiness dependency was accepted")
	}
}

func TestReadinessSnapshotsValidatedDependencyNames(t *testing.T) {
	store := &dependency{name: "store"}
	watcher := &dependency{name: "watcher"}
	server, err := New(Config{AuthEnabled: false}, nil, []Readiness{store, watcher})
	if err != nil {
		t.Fatal(err)
	}

	store.name = "other"
	watcher.name = "other"
	response := request(t, server.Handler(), http.MethodGet, "/health", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("health = %d %q", response.Code, response.Body.String())
	}
	if response.Body.String() != "{\"status\":\"ready\",\"dependencies\":[{\"name\":\"store\",\"ready\":true},{\"name\":\"watcher\",\"ready\":true}]}\n" {
		t.Fatalf("mutable dependency names escaped snapshot: %q", response.Body.String())
	}
}

func TestOperationalRoutesAuthenticateAndBoundInputs(t *testing.T) {
	server, err := New(Config{AuthEnabled: true}, fakeVerifier{key: "secret"}, []Readiness{dependency{name: "store"}, dependency{name: "watcher"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/health", "/status"} {
		if got := request(t, server.Handler(), http.MethodGet, path, "", ""); got.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated = %d", path, got.Code)
		}
		if got := request(t, server.Handler(), http.MethodGet, path, "Bearer secret", ""); got.Code != http.StatusOK {
			t.Fatalf("%s authenticated = %d %q", path, got.Code, got.Body.String())
		}
	}
	oversizedAuth := "Bearer " + strings.Repeat("x", historyauth.MaxCredentialBytes+1)
	if got := request(t, server.Handler(), http.MethodGet, "/health", oversizedAuth, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("oversized auth = %d", got.Code)
	}
	body := strings.Repeat("x", int(MaxRequestBodyBytes+1))
	if got := request(t, server.Handler(), http.MethodPost, "/api/positions", "Bearer secret", body); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d", got.Code)
	}
	chunked := httptest.NewRequest(http.MethodPost, "/api/positions", strings.NewReader(`{"value":"`+strings.Repeat("x", int(MaxRequestBodyBytes))+`"}`))
	chunked.ContentLength = -1
	chunked.Header.Set("Authorization", "Bearer secret")
	chunkedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(chunkedResponse, chunked)
	if chunkedResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversized body = %d %q", chunkedResponse.Code, chunkedResponse.Body.String())
	}
}

func TestDirectCursorMutationAlwaysRefused(t *testing.T) {
	server, err := New(Config{AuthEnabled: true}, fakeVerifier{key: "secret"}, []Readiness{dependency{name: "store"}, dependency{name: "watcher"}})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, server.Handler(), http.MethodPost, "/api/positions", "Bearer secret", `{}`)
	if response.Code != http.StatusConflict || response.Body.String() != "{\"error\":\"cursor_sync_forbidden\"}\n" {
		t.Fatalf("cursor route = %d %q", response.Code, response.Body.String())
	}
}

func TestHTTPServerTimeoutsAndBoundsAreFixed(t *testing.T) {
	server, err := New(Config{AuthEnabled: false}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := server.HTTPServer("127.0.0.1:4680")
	if httpServer.Addr != "127.0.0.1:4680" || httpServer.ReadHeaderTimeout <= 0 || httpServer.ReadTimeout <= 0 || httpServer.WriteTimeout <= 0 || httpServer.IdleTimeout <= 0 || httpServer.MaxHeaderBytes != MaxHeaderBytes {
		t.Fatalf("unsafe HTTP server: %#v", httpServer)
	}
}

type authAuthority struct{ pending, fail bool }

func (a *authAuthority) VerifyBearer(string) (historyauth.KeyKind, error) {
	return historyauth.ActiveKey, nil
}
func (a *authAuthority) BeginRotation(s string) error {
	if a.fail {
		return errors.New("failed")
	}
	if s == "" {
		return errors.New("empty")
	}
	a.pending = true
	return nil
}
func (a *authAuthority) PromotePending() error {
	if a.fail {
		return errors.New("failed")
	}
	if !a.pending {
		return errors.New("none")
	}
	a.pending = false
	return nil
}

func (a *authAuthority) RotationState() (historyauth.RotationState, error) {
	if a.fail {
		return historyauth.RotationState{}, errors.New("failed")
	}
	return historyauth.RotationState{ActiveID: "a", PendingID: map[bool]string{true: "p", false: ""}[a.pending]}, nil
}
func TestAuthRotationRoutes(t *testing.T) {
	a := &authAuthority{}
	s, e := New(Config{AuthEnabled: true, Authority: a}, a, nil)
	if e != nil {
		t.Fatal(e)
	}
	if r := request(t, s.Handler(), http.MethodGet, "/api/auth/state", "Bearer x", ""); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if r := request(t, s.Handler(), http.MethodPost, "/api/auth/rotate", "Bearer x", `{"secret":"new"}`); r.Code != 202 {
		t.Fatal(r.Code)
	}
	if r := request(t, s.Handler(), http.MethodPost, "/api/auth/rotation-ack", "Bearer x", `{}`); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if r := request(t, s.Handler(), http.MethodPost, "/api/auth/rotate", "Bearer x", `{}`); r.Code != 400 {
		t.Fatal(r.Code)
	}
}

func TestAuthRotationRoutesFailClosed(t *testing.T) {
	a := &authAuthority{fail: true}
	s, err := New(Config{AuthEnabled: true, Authority: a}, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := request(t, s.Handler(), http.MethodGet, "/api/auth/state", "Bearer x", ""); r.Code != http.StatusServiceUnavailable {
		t.Fatal(r.Code)
	}
	if r := request(t, s.Handler(), http.MethodPost, "/api/auth/rotate", "Bearer x", `{"secret":"new"}`); r.Code != http.StatusBadRequest {
		t.Fatal(r.Code)
	}
	if r := request(t, s.Handler(), http.MethodPost, "/api/auth/rotation-ack", "Bearer x", `{}`); r.Code != http.StatusConflict {
		t.Fatal(r.Code)
	}
	a.fail = false
	if r := request(t, s.Handler(), http.MethodPost, "/api/auth/rotate", "Bearer x", `{"secret":"`+strings.Repeat("x", int(MaxRequestBodyBytes))+`"}`); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatal(r.Code)
	}
	if r := request(t, s.Handler(), http.MethodPost, "/api/auth/rotation-ack", "Bearer x", `{}`+`{}`); r.Code != http.StatusBadRequest {
		t.Fatal(r.Code)
	}
}
