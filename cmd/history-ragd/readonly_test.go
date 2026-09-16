package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/config"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
)

type readOnlyTestStore struct {
	fakeDaemonStore
	ready, indexed, searched, writes atomic.Int32
}

func (s *readOnlyTestStore) Ready(context.Context) error           { s.ready.Add(1); return nil }
func (s *readOnlyTestStore) DiscoverIndexes(context.Context) error { s.indexed.Add(1); return nil }
func (s *readOnlyTestStore) Upsert(context.Context, []store.Chunk) error {
	s.writes.Add(1)
	return os.ErrPermission
}
func (s *readOnlyTestStore) Search(context.Context, store.Query) ([]store.Result, error) {
	s.searched.Add(1)
	return []store.Result{}, nil
}
func (s *readOnlyTestStore) HybridSearch(ctx context.Context, q store.Query) ([]store.Result, error) {
	return s.Search(ctx, q)
}
func (s *readOnlyTestStore) Summaries(context.Context, store.Filter, int) ([]store.Result, error) {
	return []store.Result{}, nil
}

func TestReadOnlyDaemonServesAuthenticatedRetrievalWithoutIngestion(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Dir(executable)
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(checkout); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDirectory) })
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{StateDir: state, Listen: "127.0.0.1:4681", ReadOnly: true, AuthEnabled: true, PIDFile: filepath.Join(state, "daemon.pid"), AuthStateFile: filepath.Join(state, "auth.json"), CheckoutRoot: checkout, Executable: executable}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "config.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &readOnlyTestStore{fakeDaemonStore: fakeDaemonStore{statsErr: os.ErrPermission}}
	previousStore, previousShutdown := newProductionStore, newShutdownSignal
	newProductionStore = func(context.Context, func(string) string) (daemonStore, error) { return backend, nil }
	shutdown := make(chan os.Signal, 1)
	newShutdownSignal = func() (<-chan os.Signal, func()) { return shutdown, func() {} }
	t.Cleanup(func() { newProductionStore, newShutdownSignal = previousStore, previousShutdown })
	t.Setenv("CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT", "production")
	t.Setenv("CLAUDE_HISTORY_RAG_READ_ONLY", "true")
	const secret = "fixture-private-history-read-only-secret"
	t.Setenv(pskEnvironment, secret)
	listener := isolatedDaemonListener(t)
	requested := useDaemonListener(t, listener)
	done := make(chan error, 1)
	go func() { done <- run([]string{"start", "--config", path}) }()
	client := &http.Client{Timeout: 3 * time.Second}
	healthRequest, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	healthRequest.Header.Set("Authorization", "Bearer "+secret)
	response, err := client.Do(healthRequest)
	if err != nil {
		select {
		case daemonErr := <-done:
			t.Fatalf("daemon startup: %v", daemonErr)
		default:
			t.Fatal(err)
		}
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("read-only health: %d", response.StatusCode)
	}
	for _, token := range []string{"", "Bearer " + secret} {
		request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/api/search", strings.NewReader(`{"query":"fixture"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", token)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		want := 200
		if token == "" {
			want = 401
		}
		if response.StatusCode != want {
			t.Fatalf("search status = %d; want %d", response.StatusCode, want)
		}
	}
	shutdown <- os.Interrupt
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read-only shutdown timed out")
	}
	if backend.ready.Load() == 0 || backend.indexed.Load() != 1 || backend.searched.Load() != 1 || backend.writes.Load() != 0 || !backend.closed {
		t.Fatal("read-only lifecycle failed to preserve store boundaries")
	}
	if _, address := requested(); address != "127.0.0.1:4681" {
		t.Fatalf("listener: %s", address)
	}
}
