package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
)

type recordingSink struct {
	mu     sync.Mutex
	chunks [][]store.Chunk
	err    error
}

func (s *recordingSink) Upsert(_ context.Context, chunks []store.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.chunks = append(s.chunks, append([]store.Chunk(nil), chunks...))
	return nil
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	payload, err := os.ReadFile(filepath.Join("..", "source", "claude", "testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "session.jsonl"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestProducerScansPinnedSourcesAndReportsReadiness(t *testing.T) {
	sink := &recordingSink{}
	producer, err := NewProducer(Registry{MachineID: "fixture", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{fixtureRoot(t)}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	if err := producer.Ready(context.Background()); !errors.Is(err, ErrRootUnbound) {
		t.Fatalf("Ready before scan = %v", err)
	}
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := producer.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.chunks) != 1 || len(sink.chunks[0]) != 4 || sink.chunks[0][0].MachineID != "fixture" {
		t.Fatalf("upserts = %#v", sink.chunks)
	}
	if err := producer.Scan(context.Background()); err != nil || len(sink.chunks) != 2 {
		t.Fatalf("second scan = %v, %d upserts", err, len(sink.chunks))
	}
}

func TestProducerRejectsUnsafeConstructionAndRuntimeFailures(t *testing.T) {
	root := fixtureRoot(t)
	for name, setup := range map[string]func() (*Producer, error){
		"missing sink":     func() (*Producer, error) { return NewProducer(Registry{}, nil, []string{root}, time.Second) },
		"missing roots":    func() (*Producer, error) { return NewProducer(Registry{}, &recordingSink{}, nil, time.Second) },
		"missing interval": func() (*Producer, error) { return NewProducer(Registry{}, &recordingSink{}, []string{root}, 0) },
		"relative root": func() (*Producer, error) {
			return NewProducer(Registry{}, &recordingSink{}, []string{"relative"}, time.Second)
		},
		"missing root": func() (*Producer, error) {
			return NewProducer(Registry{}, &recordingSink{}, []string{filepath.Join(root, "missing")}, time.Second)
		},
		"duplicate root": func() (*Producer, error) {
			return NewProducer(Registry{}, &recordingSink{}, []string{root, root}, time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if producer, err := setup(); err == nil {
				_ = producer.Close()
				t.Fatal("NewProducer accepted invalid input")
			}
		})
	}
	sink := &recordingSink{err: errors.New("store unavailable")}
	producer, err := NewProducer(Registry{}, sink, []string{root}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.Scan(context.Background()); !errors.Is(err, sink.err) {
		t.Fatalf("Scan sink error = %v", err)
	}
	if err := producer.Scan(nil); err == nil {
		t.Fatal("Scan accepted nil context")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := producer.Scan(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan cancellation = %v", err)
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := producer.Scan(context.Background()); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed Scan = %v", err)
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProducerRunStopsOnCancellationAndRootChanges(t *testing.T) {
	root := fixtureRoot(t)
	producer, err := NewProducer(Registry{}, &recordingSink{}, []string{root}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- producer.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for {
		if producer.Ready(context.Background()) == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if err := os.Remove(filepath.Join(root, "session.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := producer.Ready(context.Background()); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("Ready changed root = %v", err)
	}
	_ = producer.Close()
}

func TestProducerBoundsRecognizedFiles(t *testing.T) {
	root := t.TempDir()
	for index := 0; index <= MaxFilesPerScan; index++ {
		path := filepath.Join(root, "source"+strconv.Itoa(index)+".jsonl")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	producer, err := NewProducer(Registry{}, &recordingSink{}, []string{root}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	if err := producer.Scan(context.Background()); !errors.Is(err, ErrScanLimit) {
		t.Fatalf("Scan limit = %v", err)
	}
}

func TestForwardProducerRunSurvivesTransientScanError(t *testing.T) {
	sourceRoot := t.TempDir()
	state, _ := openForwardState(t)
	sink := &recordingSink{err: errors.New("transient spanner error")}
	producer, err := NewForwardProducer(Registry{MachineID: "run-host", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, 5*time.Millisecond, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	writeForwardFile(t, filepath.Join(sourceRoot, "live.jsonl"), forwardTurn("run-session", "hello", time.Now().UTC().Add(24*time.Hour)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- producer.Run(ctx) }()

	time.Sleep(15 * time.Millisecond)
	sink.mu.Lock()
	sink.err = nil
	sink.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		n := len(sink.chunks)
		sink.mu.Unlock()
		if producer.Ready(context.Background()) == nil && n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	sink.mu.Lock()
	n := len(sink.chunks)
	sink.mu.Unlock()
	if err := producer.Ready(context.Background()); err != nil || n != 1 {
		t.Fatalf("forward Run did not recover after transient error: ready=%v batches=%d", err, n)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("forward Run exit = %v, want context.Canceled", err)
	}
}
