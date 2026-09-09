package statusmetrics

import (
	"context"
	"errors"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/store"
	"sync"
	"testing"
	"time"
)

type fake struct {
	stats store.Stats
	err   error
}

func (f fake) Stats(context.Context) (store.Stats, error) { return f.stats, f.err }
func TestSnapshots(t *testing.T) {
	r := New(fake{stats: store.Stats{Backend: "spanner", TotalChunks: 3, EmbeddedChunks: 2, AwaitingEmbedding: 1}})
	r.now = func() time.Time { return time.Unix(1, 0) }
	if e := r.SetQueueDepth(4); e != nil {
		t.Fatal(e)
	}
	s := r.Snapshot(context.Background())
	if !s.Available || s.Backend != "spanner" || s.QueueDepth != 4 || s.At.Location() != time.UTC {
		t.Fatal(s)
	}
	if e := r.SetQueueDepth(-1); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if s := New(nil).Snapshot(context.Background()); s.Available || s.Error != "unavailable" {
		t.Fatal(s)
	}
	if s := New(fake{err: errors.New("x")}).Snapshot(context.Background()); s.Available || s.Error != "unavailable" {
		t.Fatal(s)
	}
}
func TestRaceSafeUpdates(t *testing.T) {
	r := New(fake{stats: store.Stats{Backend: "spanner", TotalChunks: 13, EmbeddedChunks: 8, AwaitingEmbedding: 5}})
	r.now = func() time.Time { return time.Unix(9, 0) }
	var w sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 20)
	for i := 0; i < 10; i++ {
		w.Add(1)
		go func(i int) {
			defer w.Done()
			<-start
			if err := r.SetQueueDepth(i); err != nil {
				errs <- err
				return
			}
			snapshot := r.Snapshot(context.Background())
			if !snapshot.Available || snapshot.Backend != "spanner" || snapshot.Total != 13 || snapshot.Embedded != 8 || snapshot.Awaiting != 5 || snapshot.QueueDepth < 0 || snapshot.QueueDepth > 9 || !snapshot.At.Equal(time.Unix(9, 0).UTC()) {
				errs <- errors.New("concurrent snapshot violated metrics contract")
			}
		}(i)
	}
	close(start)
	w.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// A final write after the concurrent phase establishes a deterministic
	// observable state while the concurrent snapshots above exercise the lock.
	if err := r.SetQueueDepth(10); err != nil {
		t.Fatal(err)
	}
	if snapshot := r.Snapshot(context.Background()); !snapshot.Available || snapshot.Backend != "spanner" || snapshot.Total != 13 || snapshot.Embedded != 8 || snapshot.Awaiting != 5 || snapshot.QueueDepth != 10 || !snapshot.At.Equal(time.Unix(9, 0).UTC()) {
		t.Fatalf("unexpected deterministic post-concurrency snapshot: %+v", snapshot)
	}
}
