package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
)

// Opt-in read-only check against the operator-selected database. It never
// initializes schema, embeds stored chunks, or invokes a write method.
func TestLiveRetrieval(t *testing.T) {
	if os.Getenv("HISTORY_RAG_LIVE_READ_CHECK") != "true" {
		t.Skip("operator opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	configured, err := openProductionStore(ctx, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	s := configured.(*store.SpannerStore)
	if err = s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.DiscoverIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.HybridSearch(ctx, store.Query{Text: "Velenza", Limit: 1, Mode: store.SearchAuto})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("known-history positive control returned no rows")
	}
	t.Logf("conversation search returned %d row; session=%s", len(rows), rows[0].SessionID)
	summaries, err := s.Summaries(ctx, store.Filter{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recent summaries=%d", len(summaries))
	files, err := s.Search(ctx, store.Query{Text: "file changes", Limit: 1, Mode: store.SearchAuto, Filter: store.Filter{ChunkType: "file_change"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("file changes=%d", len(files))
}
