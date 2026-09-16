package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
)

const maxRetrievalBody = 64 << 10
const maxRetrievalResponse = 8 << 20
const retrievalTimeout = 60 * time.Second

type Retrieval interface {
	Search(context.Context, store.Query) ([]store.Result, error)
	HybridSearch(context.Context, store.Query) ([]store.Result, error)
	Summaries(context.Context, store.Filter, int) ([]store.Result, error)
}

type searchRequest struct {
	Query     string  `json:"query"`
	Limit     *int    `json:"limit"`
	Project   *string `json:"project_filter"`
	From      *string `json:"date_from"`
	To        *string `json:"date_to"`
	Hybrid    *bool   `json:"use_hybrid"`
	Analysis  *bool   `json:"enable_analysis"`
	Synthesis *bool   `json:"enable_synthesis"`
	Debug     *bool   `json:"include_debug"`
}
type fileRequest struct {
	Query     *string `json:"query"`
	Path      *string `json:"file_path"`
	Limit     *int    `json:"limit"`
	Project   *string `json:"project_filter"`
	From      *string `json:"date_from"`
	To        *string `json:"date_to"`
	Operation *string `json:"operation_filter"`
}
type sessionRequest struct {
	Session *string `json:"session_id"`
	Project *string `json:"project_filter"`
	Count   *int    `json:"count"`
}

func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func limit(value *int, fallback, maximum int) (int, bool) {
	if value == nil {
		return fallback, true
	}
	return *value, *value >= 1 && *value <= maximum
}
func validQuery(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 10000 && len(s) <= 16384
}
func requestFilter(project, from, to *string) (store.Filter, bool) {
	f := store.Filter{ProjectPath: value(project)}
	if len(f.ProjectPath) > 8192 {
		return f, false
	}
	for _, item := range []struct {
		source *string
		target *time.Time
		end    bool
	}{{from, &f.DateFrom, false}, {to, &f.DateTo, true}} {
		if item.source == nil || *item.source == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, *item.source)
		if err != nil {
			parsed, err = time.Parse("2006-01-02", *item.source)
			if err == nil && item.end {
				parsed = parsed.Add(24*time.Hour - time.Nanosecond)
			}
		}
		if err != nil {
			return f, false
		}
		*item.target = parsed
	}
	return f, f.DateFrom.IsZero() || f.DateTo.IsZero() || !f.DateFrom.After(f.DateTo)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var b searchRequest
	if !decodeRetrieval(w, r, &b) {
		return
	}
	n, ok := limit(b.Limit, 5, 50)
	f, valid := requestFilter(b.Project, b.From, b.To)
	if !ok || !valid || !validQuery(b.Query) {
		invalidRetrieval(w)
		return
	}
	q := store.Query{Text: b.Query, Limit: n, Mode: store.SearchAuto, Filter: f}
	s.runRetrieval(w, r, func(ctx context.Context) (any, error) {
		var rows []store.Result
		var err error
		if b.Hybrid == nil || *b.Hybrid {
			rows, err = s.config.Retrieval.HybridSearch(ctx, q)
		} else {
			rows, err = s.config.Retrieval.Search(ctx, q)
		}
		kind := "empty"
		if len(rows) > 0 {
			kind = string(rows[0].SearchType)
		}
		return map[string]any{"results": wireResults(rows), "count": len(rows), "query": b.Query, "search_type": kind, "error": nil}, err
	})
}
func (s *Server) handleFileSearch(w http.ResponseWriter, r *http.Request) {
	var b fileRequest
	if !decodeRetrieval(w, r, &b) {
		return
	}
	n, ok := limit(b.Limit, 10, 50)
	f, valid := requestFilter(b.Project, b.From, b.To)
	f.ChunkType = "file_change"
	f.FilePath = value(b.Path)
	f.Operation = value(b.Operation)
	query := value(b.Query)
	if query == "" && f.FilePath != "" {
		query = "file changes to " + f.FilePath
	}
	if !ok || !valid || !validQuery(query) || len(f.FilePath) > 8192 || (f.Operation != "" && f.Operation != "edit" && f.Operation != "write") {
		invalidRetrieval(w)
		return
	}
	s.runRetrieval(w, r, func(ctx context.Context) (any, error) {
		rows, err := s.config.Retrieval.Search(ctx, store.Query{Text: query, Limit: n, Mode: store.SearchAuto, Filter: f})
		return map[string]any{"results": wireResults(rows), "count": len(rows), "file_path_filter": b.Path, "operation_filter": b.Operation, "error": nil}, err
	})
}
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	var b sessionRequest
	if !decodeRetrieval(w, r, &b) {
		return
	}
	n, ok := limit(b.Count, 1, 20)
	f, valid := requestFilter(b.Project, nil, nil)
	f.SessionID = value(b.Session)
	if !ok || !valid || len(f.SessionID) > 128 {
		invalidRetrieval(w)
		return
	}
	s.runRetrieval(w, r, func(ctx context.Context) (any, error) {
		rows, err := s.config.Retrieval.Summaries(ctx, f, n)
		return map[string]any{"summaries": wireResults(rows), "count": len(rows), "error": nil}, err
	})
}

func wireResults(rows []store.Result) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		result = append(result, map[string]any{"id": row.ID, "content": row.Content, "chunk_type": row.ChunkType, "session_id": row.SessionID, "project_path": row.ProjectPath, "project_name": row.ProjectName, "timestamp": row.Timestamp.Format(time.RFC3339Nano), "file_path": row.FilePath, "operation": row.Operation, "machine_id": row.MachineID, "distance": row.Distance, "search_type": row.SearchType})
	}
	return result
}

func (s *Server) runRetrieval(w http.ResponseWriter, r *http.Request, run func(context.Context) (any, error)) {
	if s.config.Retrieval == nil {
		writeJSON(w, 503, map[string]string{"error": "retrieval_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), retrievalTimeout)
	defer cancel()
	result, err := run(ctx)
	if err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, context.DeadlineExceeded) {
			code = http.StatusGatewayTimeout
		}
		writeJSON(w, code, map[string]string{"error": "retrieval_failed"})
		return
	}
	payload, err := json.Marshal(result)
	if err != nil || len(payload) > maxRetrievalResponse {
		writeJSON(w, 503, map[string]string{"error": "retrieval_response_too_large"})
		return
	}
	w.WriteHeader(200)
	_, _ = w.Write(append(payload, '\n'))
}

func invalidRetrieval(w http.ResponseWriter) {
	writeJSON(w, 400, map[string]string{"error": "invalid_request"})
}
func decodeRetrieval(w http.ResponseWriter, r *http.Request, target any) bool {
	reader := http.MaxBytesReader(w, r.Body, maxRetrievalBody)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		writeJSON(w, 413, map[string]string{"error": "request_too_large"})
		return false
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		invalidRetrieval(w)
		return false
	}
	// Every request field is scalar; reject duplicate keys and nested values before decoding.
	scan := json.NewDecoder(bytes.NewReader(data))
	if _, err = scan.Token(); err != nil {
		invalidRetrieval(w)
		return false
	}
	seen := map[string]bool{}
	for scan.More() {
		key, e := scan.Token()
		if e != nil {
			invalidRetrieval(w)
			return false
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			invalidRetrieval(w)
			return false
		}
		seen[name] = true
		var v any
		if e = scan.Decode(&v); e != nil {
			invalidRetrieval(w)
			return false
		}
		switch v.(type) {
		case map[string]any, []any:
			invalidRetrieval(w)
			return false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		invalidRetrieval(w)
		return false
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		invalidRetrieval(w)
		return false
	}
	return true
}
