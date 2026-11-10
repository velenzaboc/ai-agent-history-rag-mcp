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
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
)

const (
	MaxFileBytes          int64 = 2 << 30
	MaxLineBytes                = 32 << 20
	MaxJSONDepth                = 50
	MaxChunkContentLength       = 8000
	ChunkOverlap                = 1500
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
	r := bufio.NewReaderSize(&bounded{input, maxBytes + 1}, 64<<10)
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
	title, projPath, projName := lookupSummaryMetadata(source.FilePath, session)
	var sessionTime time.Time
	for _, event := range events {
		if event.HasTime && event.Timestamp.Unix() > 1 && sessionTime.IsZero() {
			sessionTime = event.Timestamp
		}
	}
	chunks := []store.Chunk{}
	for _, event := range events {
		if (!event.HasTime || event.Timestamp.Unix() <= 1) && !sessionTime.IsZero() {
			event.Timestamp, event.HasTime = sessionTime, true
		} else if event.HasTime && event.Timestamp.Unix() > 1 {
			sessionTime = event.Timestamp
		}
		at := event.Timestamp
		if !event.HasTime {
			at = time.Unix(0, int64(event.Line)*1000).UTC()
		}
		content := eventContent(event)
		if title != "" && title != "Antigravity Session" {
			content = "Title: " + title + "\n\n" + content
		}
		base := id(content, session, event.Step)
		turns := makeTurnChunks(content, "turn", session, projPath, projName, at, source, event.Line, "gemini-unknown", base, event.Step)
		foundOps := ops(event.ToolCalls)
		files := fileChunks(foundOps, session, projPath, projName, at, source, event.Line, turns[0].ID)
		if len(files) > 0 {
			turns[0].ChildChunkIDs = make([]string, len(files))
			for i := range files {
				turns[0].ChildChunkIDs[i] = files[i].ID
			}
		}
		chunks = append(chunks, turns...)
		chunks = append(chunks, files...)
	}
	return chunks, issues, nil
}

func makeTurnChunks(content, kind, session, path, name string, at time.Time, source Source, line int, model, base, step string) []store.Chunk {
	parts := splitContent(content)
	out := make([]store.Chunk, 0, len(parts))
	for i, part := range parts {
		ident, parent := base, ""
		if len(parts) > 1 {
			part = fmt.Sprintf("[Part %d/%d] %s", i+1, len(parts), part)
			ident = id(part, session, fmt.Sprintf("%s_part%d", step, i+1))
			if i > 0 {
				parent = base
			}
		}
		out = append(out, chunk(part, kind, session, path, name, at, source, line, model, ident, parent))
	}
	return out
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
	return makeTurnChunks(content, "antigravity_history", session, "/antigravity/unknown", "Antigravity Session", at, source, 0, "gemini-unknown", id(content, session, "binary"), "binary"), nil, nil
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
	eventType := first(text(o["type"]), text(o["step_type"]))
	e := Event{
		ID:       text(o["id"]),
		Source:   first(text(o["source"]), text(o["role"]), stepSource(eventType)),
		Type:     eventType,
		Status:   first(text(o["status"]), text(o["task_status"])),
		Content:  extractStepContent(o),
		Thinking: first(text(o["thinking"]), text(o["thought"])),
		Step:     stepText(o["step_index"]),
	}
	if e.Step == "" {
		e.Step = "0"
	}
	if stamp := first(text(o["created_at"]), text(o["timestamp"])); stamp != "" {
		p, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return Event{}, "invalid_timestamp"
		}
		e.Timestamp, e.HasTime = p.UTC(), true
	}
	for _, key := range []string{"tool_calls", "toolCalls"} {
		if calls, ok := o[key].([]any); ok {
			for i, raw := range calls {
				if call, ok := raw.(map[string]any); ok {
					e.ToolCalls = append(e.ToolCalls, ToolCall{first(text(call["id"]), fmt.Sprint(i+1)), text(call["name"]), toolCallArgs(call)})
				}
			}
		}
	}
	return e, ""
}

func stepSource(stepType string) string {
	switch strings.ToUpper(stepType) {
	case "USER_INPUT":
		return "user"
	case "PLANNER_RESPONSE", "TASK_BOUNDARY":
		return "agent"
	case "TOOL_RESPONSE":
		return "tool"
	case "GENERIC", "SYSTEM_MESSAGE", "CHECKPOINT", "ERROR_MESSAGE":
		return "system"
	}
	return ""
}

func extractStepContent(o map[string]any) string {
	for _, k := range []string{"content", "text", "message", "user_input", "planner_response", "tool_response", "task_boundary", "generic", "system_message", "error_message", "error", "checkpoint", "summary", "output", "result", "task_name", "payload", "data"} {
		if s := text(o[k]); s != "" {
			return s
		}
		if sub, ok := o[k].(map[string]any); ok {
			for _, sk := range []string{"content", "text", "message", "summary", "error", "output", "status"} {
				if s := text(sub[sk]); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func toolCallArgs(call map[string]any) any {
	for _, k := range []string{"args", "arguments", "input"} {
		if v, ok := call[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func stepText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	}
	return ""
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
		parts = append(parts, "Tool calls:\n"+formatToolCalls(e.ToolCalls))
	}
	return strings.Join(parts, "\n\n")
}

func formatToolCalls(calls []ToolCall) string {
	return jsonText(calls)
}

type op struct{ path, kind, summary string }

func ops(calls []ToolCall) []op {
	out := []op{}
	for _, call := range calls {
		args, ok := call.Args.(map[string]any)
		if !ok {
			if raw, isStr := call.Args.(string); isStr {
				if json.Unmarshal([]byte(raw), &args) != nil {
					out = append(out, patch(raw)...)
					out = append(out, shell(raw)...)
					continue
				}
			} else {
				continue
			}
		}
		path := first(text(args["file_path"]), text(args["path"]), text(args["Path"]), text(args["TargetFile"]), text(args["AbsolutePath"]))
		target := first(text(args["old_string"]), text(args["Target"]), text(args["TargetContent"]))
		replacement := first(text(args["new_string"]), text(args["Replacement"]), text(args["ReplacementContent"]))
		if path != "" {
			kind := "write"
			lowerName := strings.ToLower(call.Name)
			if strings.Contains(lowerName, "edit") || strings.Contains(lowerName, "replace") || target != "" || replacement != "" {
				kind = "edit"
			}
			summary := call.Name + " tool call"
			if target != "" || replacement != "" {
				summary = fmt.Sprintf("%s tool call (replaced %q with %q)", call.Name, target, replacement)
			}
			out = append(out, op{path, kind, summary})
		}
		out = append(out, patch(text(args["patch"]))...)
		out = append(out, shell(first(text(args["command"]), text(args["cmd"]), text(args["CommandLine"])))...)
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

func fileChunks(ops []op, session, projPath, projName string, at time.Time, source Source, line int, parent string) []store.Chunk {
	seen := map[string]bool{}
	out := []store.Chunk{}
	for _, o := range ops {
		key := o.path + o.kind
		if seen[key] {
			continue
		}
		seen[key] = true
		content := fmt.Sprintf("In Antigravity session %s, file %s was %s.\nSummary: %s", session, o.path, o.kind, o.summary)
		out = append(out, store.Chunk{ID: id(content, session, fmt.Sprintf("%d:%s", line, key)), Content: content, ChunkType: "file_change", SessionID: session, ProjectPath: projPath, ProjectName: projName, Timestamp: at, FilePath: o.path, Operation: o.kind, SourceFile: source.FilePath, SourceLine: int64(line), ParentChunkID: parent, MachineID: source.MachineID})
	}
	return out
}

func chunk(content, kind, session, path, name string, at time.Time, source Source, line int, model, ident, parent string) store.Chunk {
	return store.Chunk{ID: ident, Content: content, ChunkType: kind, SessionID: session, ProjectPath: path, ProjectName: name, Timestamp: at, Model: model, SourceFile: source.FilePath, SourceLine: int64(line), ParentChunkID: parent, MachineID: source.MachineID}
}

func splitContent(content string) []string {
	if len(content) <= MaxChunkContentLength {
		return []string{content}
	}
	parts, start := make([]string, 0), 0
	for start < len(content) {
		end := start + MaxChunkContentLength
		if end >= len(content) {
			return append(parts, content[start:])
		}
		regionStart := max(start, end-1000)
		region := content[regionStart:end]
		if at := strings.LastIndex(region, "\n\n"); at > 0 {
			end = regionStart + at + 2
		} else if at := max(strings.LastIndex(region, ". "), strings.LastIndex(region, "! "), strings.LastIndex(region, "? "), strings.LastIndex(region, "\n")); at > 0 {
			end = regionStart + at + 1
		}
		for end > start && (content[end]&0xc0) == 0x80 {
			end--
		}
		parts = append(parts, content[start:end])
		start = max(start+1, end-ChunkOverlap)
		for start < len(content) && (content[start]&0xc0) == 0x80 {
			start++
		}
	}
	return parts
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
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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
	var line []byte
	for {
		fragment, err := r.ReadSlice('\n')
		line = append(line, fragment...)
		if len(line) > MaxLineBytes {
			for err == bufio.ErrBufferFull {
				_, err = r.ReadSlice('\n')
			}
			if err != nil && err != io.EOF {
				return nil, false, false, err
			}
			return nil, true, err == io.EOF, nil
		}
		if err == nil {
			return bytes.TrimSuffix(line, []byte{'\n'}), false, false, nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF {
			return line, false, true, nil
		}
		return nil, false, false, err
	}
}
