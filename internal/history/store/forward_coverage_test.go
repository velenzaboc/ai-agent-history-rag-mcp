package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDiscoverIndexesFailsClosedAndClearsStaleReadiness(t *testing.T) {
	t.Run("canceled before query", func(t *testing.T) {
		executor := &fakeExecutor{}
		store, err := New(validConfig(EmbeddingRemoteModel), executor)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := store.DiscoverIndexes(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("DiscoverIndexes(canceled) error = %v, want %v", err, context.Canceled)
		}
		if len(executor.queries) != 0 {
			t.Fatalf("canceled discovery issued %d queries", len(executor.queries))
		}
	})

	for _, tc := range []struct {
		name string
		rows []Row
		err  error
	}{
		{name: "executor failure", err: errors.New("index catalog unavailable")},
		{name: "missing row", rows: nil},
		{name: "extra column", rows: []Row{{true, false}}},
		{name: "non-boolean state", rows: []Row{{"READ_WRITE"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &fakeExecutor{queryRows: tc.rows, queryErr: tc.err}
			store, err := New(validConfig(EmbeddingRemoteModel), executor)
			if err != nil {
				t.Fatal(err)
			}
			store.vectorIndexAvailable = true
			if err := store.DiscoverIndexes(context.Background()); err == nil {
				t.Fatal("DiscoverIndexes accepted an untrustworthy catalog response")
			}
			if !store.vectorIndexReady() {
				t.Fatal("failed discovery mutated the previously observed readiness state")
			}
			if len(executor.queries) != 1 || len(executor.ddl)+len(executor.executions)+len(executor.mutations) != 0 {
				t.Fatalf("failed discovery operations = queries:%d ddl:%d execute:%d mutations:%d", len(executor.queries), len(executor.ddl), len(executor.executions), len(executor.mutations))
			}
		})
	}

	t.Run("typed false clears stale state read only", func(t *testing.T) {
		executor := &fakeExecutor{queryRows: []Row{{false}}}
		store, err := New(validConfig(EmbeddingRemoteModel), executor)
		if err != nil {
			t.Fatal(err)
		}
		store.vectorIndexAvailable = true
		if err := store.DiscoverIndexes(context.Background()); err != nil {
			t.Fatal(err)
		}
		if store.vectorIndexReady() {
			t.Fatal("typed false catalog result left stale index readiness enabled")
		}
		if len(executor.queries) != 1 {
			t.Fatalf("typed discovery issued %d queries, want 1", len(executor.queries))
		}
		statement := executor.queries[0]
		if !strings.Contains(statement.SQL, "INDEX_STATE = 'READ_WRITE'") || statement.Params["table"] != TableName || statement.Params["index"] != VectorIndexName {
			t.Fatalf("index discovery query is not bound to the usable canonical index: %#v", statement)
		}
		if len(executor.ddl)+len(executor.executions)+len(executor.mutations) != 0 {
			t.Fatal("read-only discovery attempted a schema or data mutation")
		}
	})
}

func TestSummariesRefusesInvalidReadContractsWithoutWrites(t *testing.T) {
	from := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name      string
		ctx       context.Context
		filter    Filter
		limit     int
		queryErr  error
		wantQuery bool
	}{
		{name: "canceled context", ctx: canceled, limit: 1},
		{name: "invalid limit", ctx: context.Background(), limit: 0},
		{name: "inverted dates", ctx: context.Background(), filter: Filter{DateFrom: from.Add(time.Hour), DateTo: from}, limit: 1},
		{name: "executor failure", ctx: context.Background(), limit: 1, queryErr: errors.New("summaries unavailable"), wantQuery: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &fakeExecutor{queryErr: tc.queryErr}
			store, err := New(validConfig(EmbeddingRemoteModel), executor)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Summaries(tc.ctx, tc.filter, tc.limit); err == nil {
				t.Fatal("Summaries accepted an invalid read contract")
			}
			wantQueries := 0
			if tc.wantQuery {
				wantQueries = 1
			}
			if len(executor.queries) != wantQueries {
				t.Fatalf("Summaries issued %d queries, want %d", len(executor.queries), wantQueries)
			}
			if len(executor.ddl)+len(executor.executions)+len(executor.mutations) != 0 {
				t.Fatal("failed summary retrieval attempted a schema or data mutation")
			}
		})
	}
}
