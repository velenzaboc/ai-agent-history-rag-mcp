package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/config"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/durable"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/watch"
)

func TestRunUsesForwardProducerForProductionWatchRoots(t *testing.T) {
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

	base := t.TempDir()
	statePath := filepath.Join(base, "state")
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	roots := make([]string, 0, 6)
	for _, name := range []string{"claude", "codex", "gemini", "antigravity", "chatgpt", "claude-app"} {
		root := filepath.Join(base, name)
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
	}
	cfg := config.Config{
		StateDir:      statePath,
		Listen:        "127.0.0.1:4680",
		WatchRoots:    roots,
		PIDFile:       filepath.Join(statePath, "daemon.pid"),
		AuthStateFile: filepath.Join(statePath, "auth.json"),
		CheckoutRoot:  checkout,
		Executable:    executable,
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(base, "daemon.json")
	if err := os.WriteFile(configPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	previousStore := newProductionStore
	newProductionStore = func(context.Context, func(string) string) (daemonStore, error) {
		return &fakeDaemonStore{}, nil
	}
	previousProducer := newWatchProducer
	wantErr := errors.New("forward producer sentinel")
	called := false
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	newWatchProducer = func(registry watch.Registry, sink watch.ChunkSink, paths []string, interval time.Duration, state *durable.Root) (*watch.Producer, error) {
		called = true
		if registry.MachineID != hostname || registry.MaxBytes != watch.MaxSourceSnapshotBytes {
			t.Fatalf("forward registry = %+v", registry)
		}
		if !slices.Equal(paths, roots) {
			t.Fatalf("forward roots = %#v, want %#v", paths, roots)
		}
		if sink == nil || interval != time.Second || state == nil {
			t.Fatalf("forward inputs = sink:%v interval:%v state:%v", sink, interval, state)
		}
		return nil, wantErr
	}
	t.Cleanup(func() {
		newProductionStore = previousStore
		newWatchProducer = previousProducer
	})
	t.Setenv("CLAUDE_HISTORY_RAG_RUNTIME_CONTRACT", "production")

	err = run([]string{"start", "--config", configPath})
	if !errors.Is(err, wantErr) || !called {
		t.Fatalf("run forward construction = %v, called=%t", err, called)
	}
}
