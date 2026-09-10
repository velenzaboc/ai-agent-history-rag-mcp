// Package codex parses and chunks Codex JSONL session histories.
package codex

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
	MaxFileBytes          int64 = 10 << 30
	MaxLineBytes                = 512 << 10
	MaxJSONDepth                = 50
	MaxChunkContentLength       = 8000
	ChunkOverlap                = 1500
)

var errSourceTooLarge = errors.New("Codex source too large")

// Source supplies immutable provenance for a Codex source snapshot.
type Source struct {
	FilePath  string
	MachineID string
	MaxBytes  int64
}

type Issue struct {
	Line   int
	Reason string
}

// Event preserves the source event and its line number for callers that need
// session/thread and project metadata before chunks are materialized.
type Event struct {
	Type      string
	Payload   map[string]any
	Timestamp time.Time
	HasTime   bool
	Line      int
}

func Parse(input io.Reader, maxBytes int64) ([]Event, []Issue, error) {
	if input == nil {
		return nil, nil, errors.New("Codex source input is required")
	}
	if maxBytes == 0 {
		maxBytes = MaxFileBytes
	}
	if maxBytes < 1 || maxBytes > MaxFileBytes {
		return nil, nil, errors.New("Codex source byte limit is invalid")
	}
	reader := bufio.NewReaderSize(&boundedReader{reader: input, remain: maxBytes + 1}, MaxLineBytes+1)
	events, issues := make([]Event, 0), make([]Issue, 0)
	for lineNumber := 1; ; lineNumber++ {
		line, tooLong, done, err := readLine(reader)
		if err != nil {
			if errors.Is(err, errSourceTooLarge) {
				return nil, nil, fmt.Errorf("Codex source exceeds %d bytes", maxBytes)
			}
			return nil, nil, fmt.Errorf("read Codex source: %w", err)
		}
		if tooLong {
			issues = append(issues, Issue{Line: lineNumber, Reason: "line_too_long"})
		} else if text := bytes.TrimSpace(line); len(text) != 0 {
			event, reason := parseEvent(text)
			if reason != "" {
				issues = append(issues, Issue{Line: lineNumber, Reason: reason})
			} else {
				event.Line = lineNumber
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
		return nil, nil, errors.New("Codex source file identity is required")
	}
	events, issues, err := Parse(input, source.MaxBytes)
	if err != nil {
		return nil, issues, err
	}
	sessionID := sessionIDFromFilename(source.FilePath)
	if sessionID == "" {
		sessionID = "unknown"
	}
	var sessionCWD, lastCWD, model string
	var pending *pendingUser
	fragments := make([]string, 0)
	ops := make([]fileOperation, 0)
	chunks := make([]store.Chunk, 0)

	finalize := func() {
		if pending == nil {
			return
		}
		projectPath := projectPath(lastCWD, sessionCWD)
		timestamp := pending.timestamp
		turns := turnChunks(pending.content, strings.Join(fragments, "\n"), projectPath, sessionID, timestamp, source, pending.line, model)
		if len(turns) > 0 {
			files := fileChunks(ops, projectPath, sessionID, timestamp, source, pending.line, turns[0].ID)
			if len(files) > 0 {
				turns[0].ChildChunkIDs = make([]string, len(files))
				for index := range files {
					turns[0].ChildChunkIDs[index] = files[index].ID
				}
			}
			chunks = append(chunks, turns...)
			chunks = append(chunks, files...)
		}
		pending, fragments, ops = nil, nil, nil
	}

	for _, event := range events {
		payload := event.Payload
		switch event.Type {
		case "session_meta":
			if value := stringValue(payload["id"]); value != "" {
				sessionID = value
			}
			if value := stringValue(payload["cwd"]); value != "" {
				sessionCWD = value
			}
		case "turn_context":
			if value := stringValue(payload["cwd"]); value != "" {
				lastCWD = value
			}
			if value := stringValue(payload["model"]); value != "" {
				model = value
			}
		case "event_msg":
			text := stringValue(payload["message"])
			if text == "" {
				text = jsonValue(payload)
			}
			if text == "" {
				continue
			}
			timestamp := eventTimestamp(event)
			if stringValue(payload["type"]) == "user_message" {
				chunks = append(chunks, userChunks(text, projectPath(lastCWD, sessionCWD), sessionID, timestamp, source, event.Line, model)...)
			} else {
				chunks = append(chunks, assistantChunks(text, projectPath(lastCWD, sessionCWD), sessionID, timestamp, source, event.Line, model)...)
			}
		case "response_item":
			kind := stringValue(payload["type"])
			if kind == "message" {
				text := strings.TrimSpace(messageText(payload))
				if text == "" {
					continue
				}
				switch stringValue(payload["role"]) {
				case "user":
					finalize()
					pending = &pendingUser{content: text, timestamp: eventTimestamp(event), line: event.Line}
				case "assistant":
					if pending == nil {
						chunks = append(chunks, assistantChunks(text, projectPath(lastCWD, sessionCWD), sessionID, eventTimestamp(event), source, event.Line, model)...)
					} else {
						fragments = append(fragments, text)
					}
				default:
					chunks = append(chunks, assistantChunks(text, projectPath(lastCWD, sessionCWD), sessionID, eventTimestamp(event), source, event.Line, model)...)
				}
				continue
			}
			summary, foundOps := summarizeTool(payload)
			if pending != nil {
				if summary != "" {
					fragments = append(fragments, summary)
				}
				ops = append(ops, foundOps...)
			} else if summary != "" {
				project := projectPath(lastCWD, sessionCWD)
				timestamp := eventTimestamp(event)
				turns := assistantChunks(summary, project, sessionID, timestamp, source, event.Line, model)
				if len(turns) > 0 {
					files := fileChunks(foundOps, project, sessionID, timestamp, source, event.Line, turns[0].ID)
					if len(files) > 0 {
						turns[0].ChildChunkIDs = make([]string, len(files))
						for index := range files {
							turns[0].ChildChunkIDs[index] = files[index].ID
						}
					}
					chunks = append(chunks, turns...)
					chunks = append(chunks, files...)
				}
			}
		default:
			chunks = append(chunks, assistantChunks(jsonValue(map[string]any{"type": event.Type, "payload": payload}), projectPath(lastCWD, sessionCWD), sessionID, eventTimestamp(event), source, event.Line, model)...)
		}
	}
	finalize()
	return chunks, issues, nil
}

type pendingUser struct {
	content   string
	timestamp time.Time
	line      int
}
type fileOperation struct{ path, kind, summary string }

func parseEvent(raw []byte) (Event, string) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return Event{}, "invalid_json"
	}
	if jsonDepth(value, 0) > MaxJSONDepth {
		return Event{}, "json_too_deep"
	}
	object, ok := value.(map[string]any)
	if !ok {
		return Event{}, "entry_not_object"
	}
	kind := stringValue(object["type"])
	if kind == "" {
		return Event{}, "missing_type"
	}
	payload, _ := object["payload"].(map[string]any)
	event := Event{Type: kind, Payload: payload}
	if rawTimestamp := stringValue(object["timestamp"]); rawTimestamp != "" {
		parsed, err := time.Parse(time.RFC3339Nano, rawTimestamp)
		if err != nil {
			return Event{}, "invalid_timestamp"
		}
		event.Timestamp, event.HasTime = parsed.UTC(), true
	}
	return event, ""
}

func summarizeTool(payload map[string]any) (string, []fileOperation) {
	switch stringValue(payload["type"]) {
	case "function_call":
		name := stringValue(payload["name"])
		if name == "" {
			name = "unknown"
		}
		args := payload["arguments"]
		text := stringValue(args)
		if text == "" {
			text = jsonValue(args)
		}
		ops := shellOperations(name, args)
		if name == "apply_patch" && text != "" {
			ops = patchOperations(text)
		}
		return strings.TrimSpace("[Tool call] " + name + " " + text), ops
	case "function_call_output":
		return strings.TrimSpace("[Tool output] " + valueText(payload["output"])), nil
	case "web_search_call":
		action, _ := payload["action"].(map[string]any)
		return strings.TrimSpace("[Web search] " + stringValue(action["query"])), nil
	case "reasoning":
		items, _ := payload["summary"].([]any)
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if text := valueText(item); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) == 0 {
			return "[Reasoning summary]", nil
		}
		return "[Reasoning summary] " + strings.Join(parts, " "), nil
	case "ghost_snapshot":
		return "[Ghost snapshot captured]", nil
	}
	return "", nil
}

func shellOperations(name string, args any) []fileOperation {
	if name != "shell" && name != "shell_command" {
		return nil
	}
	command := ""
	if object, ok := args.(map[string]any); ok {
		command = stringValue(object["command"])
	}
	if raw, ok := args.(string); ok {
		var object map[string]any
		if json.Unmarshal([]byte(raw), &object) == nil {
			command = stringValue(object["command"])
		}
	}
	for _, rule := range shellRules {
		if match := rule.expression.FindStringSubmatch(command); len(match) > 1 {
			return []fileOperation{{path: match[1], kind: rule.kind, summary: rule.summary}}
		}
	}
	return nil
}

type shellRule struct {
	expression    *regexp.Regexp
	kind, summary string
}

var shellRules = []shellRule{
	{regexp.MustCompile(`\brm\s+(?:-[^\s]+\s+)?([^\s]+)`), "delete", "Deleted file"},
	{regexp.MustCompile(`\bmv\s+[^\s]+\s+([^\s]+)`), "move", "Moved file"},
	{regexp.MustCompile(`\bcp\s+[^\s]+\s+([^\s]+)`), "write", "Copied file"},
	{regexp.MustCompile(`\btouch\s+([^\s]+)`), "write", "Touched file"},
	{regexp.MustCompile(`\bmkdir\s+(?:-p\s+)?([^\s]+)`), "write", "Created directory"},
	{regexp.MustCompile(`>\s*([^\s]+)`), "write", "Wrote file"},
	{regexp.MustCompile(`\btee\s+(?:-a\s+)?([^\s]+)`), "write", "Wrote file"},
	{regexp.MustCompile(`\bsed\s+-i.*\s+([^\s]+)$`), "edit", "Edited file"},
}

func patchOperations(arguments string) []fileOperation {
	var object map[string]any
	if json.Unmarshal([]byte(arguments), &object) != nil {
		return nil
	}
	patch := stringValue(object["patch"])
	operations := make([]fileOperation, 0)
	for _, line := range strings.Split(patch, "\n") {
		for _, prefix := range []struct{ prefix, kind, summary string }{{"*** Add File: ", "write", "Added file"}, {"*** Update File: ", "edit", "Updated file"}, {"*** Delete File: ", "delete", "Deleted file"}, {"*** Move to: ", "move", "Moved file"}} {
			if path := strings.TrimSpace(strings.TrimPrefix(line, prefix.prefix)); path != line {
				if path != "" {
					operations = append(operations, fileOperation{path, prefix.kind, prefix.summary})
				}
				break
			}
		}
	}
	return operations
}

func turnChunks(user, assistant, project, session string, timestamp time.Time, source Source, line int, model string) []store.Chunk {
	return makeChunks("In project "+projectName(project)+", user asked:\n"+user+"\n\nAssistant responded:\n"+strings.TrimSpace(assistant), "turn", project, session, timestamp, source, line, model)
}
func assistantChunks(text, project, session string, timestamp time.Time, source Source, line int, model string) []store.Chunk {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return makeChunks("In project "+projectName(project)+", assistant responded:\n"+text, "turn", project, session, timestamp, source, line, model)
}
func userChunks(text, project, session string, timestamp time.Time, source Source, line int, model string) []store.Chunk {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return makeChunks("In project "+projectName(project)+", user said:\n"+text, "turn", project, session, timestamp, source, line, model)
}
func makeChunks(content, kind, project, session string, timestamp time.Time, source Source, line int, model string) []store.Chunk {
	parts, baseID := splitContent(content), chunkID(content, session, timestampKey(timestamp))
	chunks := make([]store.Chunk, 0, len(parts))
	for index, part := range parts {
		id, parent := baseID, ""
		if len(parts) > 1 {
			part = fmt.Sprintf("[Part %d/%d] %s", index+1, len(parts), part)
			id = chunkID(part, session, timestampKey(timestamp)+fmt.Sprintf("_part%d", index+1))
			if index > 0 {
				parent = baseID
			}
		}
		chunks = append(chunks, store.Chunk{ID: id, Content: part, ChunkType: kind, SessionID: session, ProjectPath: project, ProjectName: projectName(project), Timestamp: timestamp, Model: model, SourceFile: source.FilePath, SourceLine: int64(line), ParentChunkID: parent, MachineID: source.MachineID})
	}
	return chunks
}
func fileChunks(ops []fileOperation, project, session string, timestamp time.Time, source Source, line int, parent string) []store.Chunk {
	chunks := make([]store.Chunk, 0, len(ops))
	name := projectName(project)
	for index, op := range ops {
		content := fmt.Sprintf("In project %s, file %s was %s.\nSummary: %s", name, op.path, op.kind, op.summary)
		chunks = append(chunks, store.Chunk{ID: chunkID(content, session, timestampKey(timestamp)+op.path+fmt.Sprint(index)), Content: content, ChunkType: "file_change", SessionID: session, ProjectPath: project, ProjectName: name, Timestamp: timestamp, FilePath: op.path, Operation: op.kind, SourceFile: source.FilePath, SourceLine: int64(line), ParentChunkID: parent, MachineID: source.MachineID})
	}
	return chunks
}

func messageText(payload map[string]any) string {
	items, _ := payload["content"].([]any)
	parts := make([]string, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if ok && (stringValue(item["type"]) == "input_text" || stringValue(item["type"]) == "output_text" || stringValue(item["type"]) == "text") {
			if text := stringValue(item["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
func projectPath(last, session string) string {
	if last != "" {
		return last
	}
	if session != "" {
		return session
	}
	return "/unknown"
}
func projectName(project string) string {
	if name := filepath.Base(project); name != "." && name != "/" && name != "" {
		return name
	}
	return "unknown"
}
func eventTimestamp(event Event) time.Time {
	if event.HasTime {
		return event.Timestamp
	}
	return time.Unix(0, int64(event.Line)*1000).UTC()
}
func sessionIDFromFilename(path string) string {
	stem := filepath.Base(strings.TrimSuffix(path, filepath.Ext(path)))
	if len(stem) < 36 {
		return ""
	}
	candidate := stem[len(stem)-36:]
	if regexp.MustCompile(`^[0-9a-fA-F-]{36}$`).MatchString(candidate) {
		return strings.ToLower(candidate)
	}
	return ""
}
func valueText(value any) string {
	if text := stringValue(value); text != "" {
		return text
	}
	return jsonValue(value)
}
func jsonValue(value any) string {
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}
func stringValue(value any) string { text, _ := value.(string); return text }
func timestampKey(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04:05.999999999+00:00")
}
func chunkID(content, session, timestamp string) string {
	sum := sha256.Sum256([]byte(content + session + timestamp))
	return hex.EncodeToString(sum[:])[:16]
}
func splitContent(content string) []string {
	if len(content) <= MaxChunkContentLength {
		return []string{content}
	}
	parts, start := make([]string, 0), 0
	for start < len(content) {
		end := min(start+MaxChunkContentLength, len(content))
		if end < len(content) {
			regionStart := max(start, end-1000)
			region := content[regionStart:end]
			if at := strings.LastIndex(region, "\n\n"); at > 0 {
				end = regionStart + at + 2
			} else if at := max(strings.LastIndex(region, ". "), strings.LastIndex(region, "! "), strings.LastIndex(region, "? "), strings.LastIndex(region, "\n")); at > 0 {
				end = regionStart + at + 1
			}
		}
		parts = append(parts, content[start:end])
		start = max(start+1, end-ChunkOverlap)
	}
	return parts
}
func jsonDepth(value any, depth int) int {
	maximum := depth
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			maximum = max(maximum, jsonDepth(child, depth+1))
		}
	case []any:
		for _, child := range typed {
			maximum = max(maximum, jsonDepth(child, depth+1))
		}
	}
	return maximum
}

type boundedReader struct {
	reader io.Reader
	remain int64
}

func (reader *boundedReader) Read(payload []byte) (int, error) {
	if reader.remain == 0 {
		return 0, errSourceTooLarge
	}
	if int64(len(payload)) > reader.remain {
		payload = payload[:reader.remain]
	}
	count, err := reader.reader.Read(payload)
	reader.remain -= int64(count)
	return count, err
}
func readLine(reader *bufio.Reader) ([]byte, bool, bool, error) {
	line := make([]byte, 0, MaxLineBytes)
	for {
		fragment, err := reader.ReadSlice('\n')
		line = append(line, fragment...)
		if len(line) > MaxLineBytes {
			for err == bufio.ErrBufferFull {
				_, err = reader.ReadSlice('\n')
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
