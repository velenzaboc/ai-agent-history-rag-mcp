// Package antigravity parses modern Antigravity JSONL transcripts and legacy blobs.
package antigravity

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/store"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	MaxFileBytes          int64 = 2 << 30
	MaxLineBytes                = 512 << 10
	MaxJSONDepth                = 50
	MaxChunkContentLength       = 8000
)

var errLarge = errors.New("Antigravity source too large")

type Source struct {
	FilePath, MachineID string
	MaxBytes            int64
	Binary              bool
}
type Issue struct {
	Line   int
	Reason string
}
type Event struct {
	ID, Source, Type, Status, Content, Thinking string
	ToolCalls                                   []ToolCall
	Timestamp                                   time.Time
	HasTime                                     bool
	Step                                        string
	Line                                        int
}
type ToolCall struct {
	ID, Name string
	Args     any
}

func Parse(input io.Reader, maxBytes int64) ([]Event, []Issue, error) {
	if input == nil {
		return nil, nil, errors.New("Antigravity input is required")
	}
	if maxBytes == 0 {
		maxBytes = MaxFileBytes
	}
	if maxBytes < 1 || maxBytes > MaxFileBytes {
		return nil, nil, errors.New("Antigravity byte limit is invalid")
	}
	r := bufio.NewReaderSize(&bounded{input, maxBytes + 1}, MaxLineBytes+1)
	events, issues := []Event{}, []Issue{}
	for line := 1; ; line++ {
		raw, long, done, err := readLine(r)
		if err != nil {
			if errors.Is(err, errLarge) {
				return nil, nil, fmt.Errorf("Antigravity source exceeds %d bytes", maxBytes)
			}
			return nil, nil, err
		}
		if long {
			issues = append(issues, Issue{line, "line_too_long"})
		} else if text := bytes.TrimSpace(raw); len(text) > 0 {
			event, reason := parseEvent(text)
			if reason != "" {
				issues = append(issues, Issue{line, reason})
			} else {
				event.Line = line
				events = append(events, event)
			}
		}
		if done {
			return events, issues, nil
		}
	}
}
func Chunks(input io.Reader, source Source) ([]store.Chunk, []Issue, error) {
	if source.FilePath == "" {
		return nil, nil, errors.New("Antigravity source identity is required")
	}
	if source.Binary {
		return binaryChunks(input, source)
	}
	events, issues, err := Parse(input, source.MaxBytes)
	if err != nil {
		return nil, issues, err
	}
	session := sessionID(source.FilePath)
	chunks := []store.Chunk{}
	for _, event := range events {
		at := event.Timestamp
		if !event.HasTime {
			at = time.Unix(0, int64(event.Line)*1000).UTC()
		}
		content := eventContent(event)
		base := id(content, session, event.Step)
		turn := chunk(content, "turn", session, "/antigravity/"+session, "Antigravity Session", at, source, event.Line, "gemini-unknown", base, "")
		ops := ops(event.ToolCalls)
		files := fileChunks(ops, session, at, source, event.Line, base)
		if len(files) > 0 {
			turn.ChildChunkIDs = make([]string, len(files))
			for i := range files {
				turn.ChildChunkIDs[i] = files[i].ID
			}
		}
		chunks = append(chunks, turn)
		chunks = append(chunks, files...)
	}
	return chunks, issues, nil
}
func binaryChunks(input io.Reader, source Source) ([]store.Chunk, []Issue, error) {
	data, err := io.ReadAll(&bounded{input, limit(source.MaxBytes) + 1})
	if err != nil {
		return nil, nil, err
	}
	text := strings.Join(strings.FieldsFunc(string(data), func(r rune) bool { return r < 32 && r != '\n' && r != '\t' }), " ")
	if strings.TrimSpace(text) == "" {
		return nil, nil, nil
	}
	session := strings.TrimSuffix(filepath.Base(source.FilePath), filepath.Ext(source.FilePath))
	at := time.Unix(0, 0).UTC()
	content := "Google Antigravity History from " + filepath.Base(source.FilePath) + "\n\n" + text
	return []store.Chunk{chunk(content, "antigravity_history", session, "/antigravity/unknown", "Antigravity Session", at, source, 0, "gemini-unknown", id(content, session, "binary"), "")}, nil, nil
}
func parseEvent(raw []byte) (Event, string) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return Event{}, "invalid_json"
	}
	if depth(v, 0) > MaxJSONDepth {
		return Event{}, "json_too_deep"
	}
	o, ok := v.(map[string]any)
	if !ok {
		return Event{}, "event_not_object"
	}
	e := Event{ID: text(o["id"]), Source: text(o["source"]), Type: text(o["type"]), Status: text(o["status"]), Content: text(o["content"]), Thinking: text(o["thinking"]), Step: text(o["step_index"])}
	if e.Step == "" {
		e.Step = "0"
	}
	if stamp := text(o["created_at"]); stamp != "" {
		p, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return Event{}, "invalid_timestamp"
		}
		e.Timestamp, e.HasTime = p.UTC(), true
	}
	if calls, ok := o["tool_calls"].([]any); ok {
		for i, raw := range calls {
			if call, ok := raw.(map[string]any); ok {
				e.ToolCalls = append(e.ToolCalls, ToolCall{first(text(call["id"]), fmt.Sprint(i+1)), text(call["name"]), call["args"]})
			}
		}
	}
	return e, ""
}
func eventContent(e Event) string {
	parts := []string{"Source: " + first(e.Source, "unknown"), "Type: " + first(e.Type, "unknown"), "Status: " + first(e.Status, "unknown")}
	if e.Content != "" {
		parts = append(parts, "Content:\n"+e.Content)
	}
	if e.Thinking != "" {
		parts = append(parts, "Thinking:\n"+e.Thinking)
	}
	if len(e.ToolCalls) > 0 {
		parts = append(parts, "Tool calls:\n"+jsonText(e.ToolCalls))
	}
	return strings.Join(parts, "\n\n")
}

type op struct{ path, kind, summary string }

func ops(calls []ToolCall) []op {
	out := []op{}
	for _, call := range calls {
		if args, ok := call.Args.(map[string]any); ok {
			if path := first(text(args["file_path"]), text(args["path"])); path != "" {
				kind := "write"
				if strings.Contains(strings.ToLower(call.Name), "edit") {
					kind = "edit"
				}
				out = append(out, op{path, kind, call.Name + " tool call"})
			}
			out = append(out, patch(text(args["patch"]))...)
			out = append(out, shell(text(args["command"]))...)
		} else if raw, ok := call.Args.(string); ok {
			out = append(out, patch(raw)...)
			out = append(out, shell(raw)...)
		}
	}
	return out
}
func patch(s string) []op {
	out := []op{}
	for _, line := range strings.Split(s, "\n") {
		for _, r := range []struct{ p, k, x string }{{"*** Add File: ", "write", "Added file"}, {"*** Update File: ", "edit", "Updated file"}, {"*** Delete File: ", "delete", "Deleted file"}} {
			if path := strings.TrimSpace(strings.TrimPrefix(line, r.p)); path != line && path != "" {
				out = append(out, op{path, r.k, r.x})
			}
		}
	}
	return out
}

var shellRE = regexp.MustCompile(`\b(?:rm|touch)\s+(?:-[^\s]+\s+)?([^\s]+)`)

func shell(s string) []op {
	m := shellRE.FindStringSubmatch(s)
	if len(m) < 2 {
		return nil
	}
	k, x := "write", "Touched file"
	if strings.Contains(s, "rm") {
		k, x = "delete", "Deleted file"
	}
	return []op{{m[1], k, x}}
}
func fileChunks(ops []op, session string, at time.Time, source Source, line int, parent string) []store.Chunk {
	seen := map[string]bool{}
	out := []store.Chunk{}
	for _, o := range ops {
		key := o.path + o.kind
		if seen[key] {
			continue
		}
		seen[key] = true
		content := fmt.Sprintf("In Antigravity session %s, file %s was %s.\nSummary: %s", session, o.path, o.kind, o.summary)
		out = append(out, store.Chunk{ID: id(content, session, fmt.Sprintf("%d:%s", line, key)), Content: content, ChunkType: "file_change", SessionID: session, ProjectPath: "/antigravity/" + session, ProjectName: "Antigravity Session", Timestamp: at, FilePath: o.path, Operation: o.kind, SourceFile: source.FilePath, SourceLine: int64(line), ParentChunkID: parent, MachineID: source.MachineID})
	}
	return out
}
func chunk(content, kind, session, path, name string, at time.Time, source Source, line int, model, ident, parent string) store.Chunk {
	return store.Chunk{ID: ident, Content: content, ChunkType: kind, SessionID: session, ProjectPath: path, ProjectName: name, Timestamp: at, Model: model, SourceFile: source.FilePath, SourceLine: int64(line), ParentChunkID: parent, MachineID: source.MachineID}
}
func sessionID(path string) string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for i, p := range parts {
		if p == "brain" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
func id(a, b, c string) string {
	s := sha256.Sum256([]byte(a + b + c))
	return hex.EncodeToString(s[:])[:16]
}
func text(v any) string { s, _ := v.(string); return s }
func first(v, f string) string {
	if v != "" {
		return v
	}
	return f
}
func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
func depth(v any, n int) int {
	m := n
	switch x := v.(type) {
	case map[string]any:
		for _, v := range x {
			m = max(m, depth(v, n+1))
		}
	case []any:
		for _, v := range x {
			m = max(m, depth(v, n+1))
		}
	}
	return m
}
func limit(n int64) int64 {
	if n == 0 {
		return MaxFileBytes
	}
	return n
}

type bounded struct {
	r io.Reader
	n int64
}

func (b *bounded) Read(p []byte) (int, error) {
	if b.n == 0 {
		return 0, errLarge
	}
	if int64(len(p)) > b.n {
		p = p[:b.n]
	}
	n, e := b.r.Read(p)
	b.n -= int64(n)
	return n, e
}
func readLine(r *bufio.Reader) ([]byte, bool, bool, error) {
	b, e := r.ReadBytes('\n')
	if len(b) > MaxLineBytes {
		return nil, true, e == io.EOF, nil
	}
	if e == nil {
		return bytes.TrimSuffix(b, []byte{'\n'}), false, false, nil
	}
	if e == io.EOF {
		return b, false, true, nil
	}
	return nil, false, false, e
}
