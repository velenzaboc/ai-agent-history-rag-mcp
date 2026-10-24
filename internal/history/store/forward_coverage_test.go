package store

import (
	"context"
	"errors"
	"strings"
	"testing"
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
