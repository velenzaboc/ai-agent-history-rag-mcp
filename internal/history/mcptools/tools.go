// Package mcptools exposes the compatibility-declared native MCP tool names.
package mcptools

import (
	"context"
	"errors"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
	"sort"
)

var ErrUnsupported = errors.New("unsupported MCP tool")
var ErrInvalid = errors.New("invalid MCP request")

const MaxQueryBytes = 4096

type Tool string

const (
	SearchConversations Tool = "search_conversations"
	SearchFileChanges   Tool = "search_file_changes"
	GetSessionSummary   Tool = "get_session_summary"
	GetIndexStatus      Tool = "get_index_status"
	GetServerStatus     Tool = "get_server_status"
)

type Service struct {
	Store store.Store
	Ready func(context.Context) error
}

func Names() []Tool {
	return []Tool{GetIndexStatus, GetServerStatus, GetSessionSummary, SearchConversations, SearchFileChanges}
}
func (s Service) Search(ctx context.Context, tool Tool, text string, limit int) ([]store.Result, error) {
	if s.Store == nil || len(text) == 0 || len(text) > MaxQueryBytes || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	if tool != SearchConversations && tool != SearchFileChanges {
		return nil, ErrUnsupported
	}
	q := store.Query{Text: text, Limit: limit}
	if tool == SearchFileChanges {
		q.Filter.ChunkType = "file_change"
	}
	return s.Store.Search(ctx, q)
}
func (s Service) Status(ctx context.Context, tool Tool) (store.Stats, error) {
	if s.Store == nil {
		return store.Stats{}, ErrInvalid
	}
	if tool != GetIndexStatus && tool != GetServerStatus {
		return store.Stats{}, ErrUnsupported
	}
	if s.Ready != nil {
		if e := s.Ready(ctx); e != nil {
			return store.Stats{}, e
		}
	}
	return s.Store.Stats(ctx)
}
func Valid(tool Tool) bool {
	for _, v := range Names() {
		if v == tool {
			return true
		}
	}
	return false
}
func init() { sort.Slice(Names(), func(i, j int) bool { return Names()[i] < Names()[j] }) }
