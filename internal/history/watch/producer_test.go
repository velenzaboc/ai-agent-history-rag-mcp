package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/store"
)

type recordingSink struct {
	chunks [][]store.Chunk
	err    error
}

func (s *recordingSink) Upsert(_ context.Context, chunks []store.Chunk) error {
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
