package store

import (
	"context"
	"strings"
	"testing"
)

func TestSummariesFilterBeforeLimitAndKeepLatestPerSession(t *testing.T) {
	e := &fakeExecutor{queryRows: []Row{validResultRow()}}
	s, _ := New(validConfig(EmbeddingRemoteModel), e)
	rows, err := s.Summaries(context.Background(), Filter{SessionID: "target' OR TRUE --", ProjectPath: "/project"}, 2)
	if err != nil || len(rows) != 1 {
		t.Fatalf("Summaries=%#v %v", rows, err)
	}
	q := e.queries[0]
	for _, part := range []string{"SessionId = @session_id", "ChunkType = @chunk_type", "GROUP BY SessionId", "ORDER BY Timestamp DESC, Id LIMIT 1", "LIMIT 2"} {
		if !strings.Contains(q.SQL, part) {
			t.Errorf("missing %s: %s", part, q.SQL)
		}
	}
	if strings.Contains(q.SQL, "target'") || q.Params["session_id"] != "target' OR TRUE --" || q.Params["chunk_type"] != "summary" {
		t.Fatal("unbound or incorrect filters")
	}
	if len(e.executions)+len(e.mutations)+len(e.ddl) != 0 {
		t.Fatal("read changed corpus")
	}
}

func TestReadinessUsesBoundedReadAndIndexDiscoveryIsReadOnly(t *testing.T) {
	e := &fakeExecutor{queryRows: []Row{{true}}}
	s, _ := New(validConfig(EmbeddingRemoteModel), e)
	if err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.queries[0].SQL, "COUNT") || !strings.Contains(e.queries[0].SQL, "LIMIT 1") {
		t.Fatal("readiness scans counts")
	}
	if err := s.DiscoverIndexes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.vectorIndexReady() || len(e.ddl) != 0 {
		t.Fatal("index discovery did not adopt existing index read-only")
	}
}
