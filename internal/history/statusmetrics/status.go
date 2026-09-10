// Package statusmetrics produces bounded, secret-free operational snapshots.
package statusmetrics

import (
	"context"
	"errors"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("status unavailable")

type Provider interface {
	Stats(context.Context) (store.Stats, error)
}
type Snapshot struct {
	Available  bool      `json:"available"`
	Backend    string    `json:"backend,omitempty"`
	Total      int64     `json:"total_chunks,omitempty"`
	Embedded   int64     `json:"embedded_chunks,omitempty"`
	Awaiting   int64     `json:"awaiting_embedding,omitempty"`
	QueueDepth int       `json:"queue_depth"`
	At         time.Time `json:"at"`
	Error      string    `json:"error,omitempty"`
}
type Reporter struct {
	mu       sync.RWMutex
	provider Provider
	queue    int
	now      func() time.Time
}

func New(provider Provider) *Reporter { return &Reporter{provider: provider, now: time.Now} }
func (r *Reporter) SetQueueDepth(depth int) error {
	if depth < 0 {
		return ErrUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queue = depth
	return nil
}
func (r *Reporter) Snapshot(ctx context.Context) Snapshot {
	r.mu.RLock()
	p, q, n := r.provider, r.queue, r.now
	r.mu.RUnlock()
	out := Snapshot{QueueDepth: q, At: n().UTC()}
	if p == nil {
		out.Error = "unavailable"
		return out
	}
	s, e := p.Stats(ctx)
	if e != nil {
		out.Error = "unavailable"
		return out
	}
	out.Available = true
	out.Backend = s.Backend
	out.Total = s.TotalChunks
	out.Embedded = s.EmbeddedChunks
	out.Awaiting = s.AwaitingEmbedding
	return out
}
