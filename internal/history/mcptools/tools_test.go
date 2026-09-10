package mcptools

import (
	"context"
	"errors"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
	"testing"
)

type fake struct{}

func (fake) Initialize(context.Context) error            { return nil }
func (fake) Upsert(context.Context, []store.Chunk) error { return nil }
func (fake) Search(context.Context, store.Query) ([]store.Result, error) {
	return []store.Result{{ID: "x"}}, nil
}
func (fake) HybridSearch(context.Context, store.Query) ([]store.Result, error) { return nil, nil }
func (fake) Stats(context.Context) (store.Stats, error)                        { return store.Stats{Backend: "x"}, nil }
func (fake) ChunkExists(context.Context, string) (bool, error)                 { return false, nil }
func (fake) DeleteMachine(context.Context, string) (int64, error)              { return 0, nil }
func (fake) Clear(context.Context) (int64, error)                              { return 0, nil }
func (fake) Optimize(context.Context) error                                    { return nil }
func (fake) Close() error                                                      { return nil }
func TestTools(t *testing.T) {
	s := Service{Store: fake{}}
	if !Valid(SearchConversations) || Valid("x") || len(Names()) != 5 {
		t.Fatal(Names())
	}
	r, e := s.Search(context.Background(), SearchFileChanges, "q", 1)
	if e != nil || len(r) != 1 {
		t.Fatal(r, e)
	}
	if _, e = s.Search(context.Background(), "x", "q", 1); !errors.Is(e, ErrUnsupported) {
		t.Fatal(e)
	}
	if _, e = s.Search(context.Background(), SearchConversations, "", 1); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if _, e = s.Status(context.Background(), GetIndexStatus); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Status(context.Background(), "x"); !errors.Is(e, ErrUnsupported) {
		t.Fatal(e)
	}
}
