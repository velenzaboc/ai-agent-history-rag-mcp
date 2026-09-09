package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/store"
)

const MaxFilesPerScan = 1024

var ErrScanLimit = errors.New("watch scan exceeds file limit")

type ChunkSink interface {
	Upsert(context.Context, []store.Chunk) error
}

// Producer is a bounded polling observer. Every candidate is reopened through
// its pinned root before parsing, so a path replacement cannot escape a root.
type Producer struct {
	registry Registry
	sink     ChunkSink
	roots    []*PinnedRoot
	interval time.Duration
	mu       sync.RWMutex
	ready    bool
	closed   bool
}

func NewProducer(registry Registry, sink ChunkSink, paths []string, interval time.Duration) (*Producer, error) {
	if sink == nil || len(paths) == 0 {
		return nil, errors.New("watch producer requires sink and roots")
	}
	if interval <= 0 {
		return nil, errors.New("watch producer interval is required")
	}
	ordered := append([]string(nil), paths...)
	sort.Strings(ordered)
	producer := &Producer{registry: registry, sink: sink, interval: interval}
	for index, path := range ordered {
		if index > 0 && path == ordered[index-1] {
			_ = producer.Close()
			return nil, errors.New("watch producer roots must be unique")
		}
		root, err := NewPinnedRoot(path)
		if err != nil {
			_ = producer.Close()
			return nil, err
		}
		bound, err := root.Bind()
		if err != nil || !bound {
			_ = root.Close()
			_ = producer.Close()
			if err != nil {
				return nil, fmt.Errorf("bind watch root: %w", err)
			}
			return nil, fmt.Errorf("bind watch root: %w", ErrRootUnbound)
		}
		producer.roots = append(producer.roots, root)
	}
	return producer, nil
}

func (p *Producer) Ready(context.Context) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed || !p.ready {
		return ErrRootUnbound
	}
	for _, root := range p.roots {
		if err := root.Verify(); err != nil {
			return err
		}
	}
	return nil
}

func (p *Producer) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("watch producer context is required")
	}
	if err := p.Scan(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := p.Scan(ctx); err != nil {
				return err
			}
		}
	}
}

func (p *Producer) Scan(ctx context.Context) error {
	if ctx == nil {
		return errors.New("watch producer context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return fs.ErrClosed
	}
	roots := append([]*PinnedRoot(nil), p.roots...)
	p.mu.RUnlock()
	seen := 0
	for _, root := range roots {
		if err := filepath.WalkDir(root.Path(), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || !entry.Type().IsRegular() {
				return nil
			}
			kind, err := p.registry.Detect(path)
			if errors.Is(err, ErrUnsupportedSource) {
				return nil
			}
			if err != nil {
				return err
			}
			seen++
			if seen > MaxFilesPerScan {
				return ErrScanLimit
			}
			snapshot, err := root.Snapshot(path, MaxSourceSnapshotBytes)
			if err != nil {
				return err
			}
			chunks, dispatchErr := p.registry.Dispatch(kind, snapshot.Reader(), path)
			closeErr := snapshot.Close()
			if dispatchErr != nil {
				return dispatchErr
			}
			if closeErr != nil {
				return closeErr
			}
			if len(chunks) != 0 {
				return p.sink.Upsert(ctx, chunks)
			}
			return nil
		}); err != nil {
			p.setReady(false)
			return err
		}
	}
	p.setReady(true)
	return nil
}

func (p *Producer) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.ready = false
	roots := append([]*PinnedRoot(nil), p.roots...)
	p.mu.Unlock()
	var failures []error
	for _, root := range roots {
		failures = append(failures, root.Close())
	}
	return errors.Join(failures...)
}

func (p *Producer) setReady(value bool) {
	p.mu.Lock()
	p.ready = value
	p.mu.Unlock()
}
