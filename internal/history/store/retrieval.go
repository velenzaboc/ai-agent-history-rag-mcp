package store

import (
	"context"
	"fmt"
	"strings"
)

// Ready checks access without counting or embedding the corpus.
func (s *SpannerStore) Ready(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	_, err := s.executor.Query(ctx, Statement{SQL: "SELECT Id FROM ConversationChunks LIMIT 1", Params: map[string]any{}})
	return err
}

// DiscoverIndexes adopts an already usable index. It never creates schema.
func (s *SpannerStore) DiscoverIndexes(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	rows, err := s.executor.Query(ctx, Statement{SQL: `SELECT EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.INDEXES WHERE TABLE_NAME = @table AND INDEX_NAME = @index AND INDEX_STATE = 'READ_WRITE')`, Params: map[string]any{"table": TableName, "index": VectorIndexName}})
	if err != nil {
		return err
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		return fmt.Errorf("invalid index discovery response")
	}
	ready, ok := rows[0][0].(bool)
	if !ok {
		return fmt.Errorf("invalid index discovery type")
	}
	s.stateMu.Lock()
	s.vectorIndexAvailable = ready
	s.stateMu.Unlock()
	return nil
}

// Summaries selects the latest stored summary per session, applying exact
// session/project predicates before ranking and limiting. No embedding is needed.
func (s *SpannerStore) Summaries(ctx context.Context, filter Filter, limit int) ([]Result, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := validateLimit(limit, 20); err != nil {
		return nil, err
	}
	filter.ChunkType = "summary"
	clauses, params, err := buildFilters(filter, false)
	if err != nil {
		return nil, err
	}
	statement := Statement{SQL: fmt.Sprintf(`SELECT Id, Content, ChunkType, SessionId, ProjectPath, ProjectName,
 Timestamp, FilePath, Operation, MachineId, CAST(0 AS FLOAT64) AS Distance
 FROM (SELECT Id, Content, ChunkType, SessionId, ProjectPath, ProjectName,
 Timestamp, FilePath, Operation, MachineId,
 ROW_NUMBER() OVER (PARTITION BY SessionId ORDER BY Timestamp DESC, Id) AS position
 FROM ConversationChunks WHERE %s)
 WHERE position = 1 ORDER BY Timestamp DESC, Id LIMIT %d`, strings.Join(clauses, " AND "), limit), Params: params}
	rows, err := s.executor.Query(ctx, statement)
	if err != nil {
		return nil, err
	}
	return parseResultRows(rows, SearchType("recent"))
}
