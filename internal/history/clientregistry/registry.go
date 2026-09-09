// Package clientregistry validates the native history-client component surface.
package clientregistry

import (
	"errors"
	"fmt"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/apiclient"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/cursor"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/uploadqueue"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/watch"
	"path/filepath"
	"sort"
)

var ErrInvalid = errors.New("invalid native client registry")

type Config struct {
	MachineID, StateDir string
	Sources             []watch.SourceKind
	QueueCapacity       int
	API                 apiclient.Config
}
type Registry struct {
	Sources []watch.SourceKind
	Watch   watch.Registry
	Cursor  *cursor.State
	Queue   *uploadqueue.Queue
	API     *apiclient.Client
}

func Open(c Config) (*Registry, error) {
	if c.MachineID == "" || !filepath.IsAbs(c.StateDir) || filepath.Clean(c.StateDir) != c.StateDir || c.QueueCapacity < 1 {
		return nil, ErrInvalid
	}
	seen := map[watch.SourceKind]bool{}
	sources := append([]watch.SourceKind(nil), c.Sources...)
	if len(sources) != 6 {
		return nil, ErrInvalid
	}
	for _, s := range sources {
		if seen[s] || !supported(s) {
			return nil, ErrInvalid
		}
		seen[s] = true
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })
	state, e := cursor.Open(filepath.Join(c.StateDir, "cursor.json"))
	if e != nil {
		return nil, e
	}
	queue, e := uploadqueue.Open(filepath.Join(c.StateDir, "queue.json"), c.QueueCapacity)
	if e != nil {
		return nil, e
	}
	api, e := apiclient.New(c.API)
	if e != nil {
		return nil, e
	}
	return &Registry{sources, watch.Registry{MachineID: c.MachineID}, state, queue, api}, nil
}
func supported(s watch.SourceKind) bool {
	switch s {
	case watch.SourceClaude, watch.SourceCodex, watch.SourceChatGPT, watch.SourceGemini, watch.SourceAntigravity, watch.SourceClaudeApp:
		return true
	}
	return false
}
func (r *Registry) Supports(s watch.SourceKind) bool {
	if r == nil {
		return false
	}
	for _, v := range r.Sources {
		if v == s {
			return true
		}
	}
	return false
}
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	if r.Cursor == nil || r.Queue == nil || r.API == nil {
		return fmt.Errorf("%w: incomplete registry", ErrInvalid)
	}
	return nil
}
