// Package gemini parses and chunks Gemini CLI session and logs JSON sources.
package gemini

import (
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
	MaxJSONDepth                = 50
	MaxChunkContentLength       = 8000
	ChunkOverlap                = 1500
)

var errSourceTooLarge = errors.New("Gemini source too large")

type Source struct {
	FilePath, MachineID string
	MaxBytes            int64
}
type Issue struct {
	Index  int
	Reason string
}
type Document struct {
	SessionID, ConversationID string
	Messages                  []Message
	Events                    []Event
}
type Message struct {
	ID, Type, Content, Model string
	Timestamp                time.Time
	HasTime                  bool
	Thoughts                 []string
	ToolCalls                []ToolCall
	Raw                      map[string]any
}
type Event struct {
	ID, SessionID, Content string
	Timestamp              time.Time
	HasTime                bool
}
type ToolCall struct {
	ID, Name      string
	Args          any
	ResultDisplay string
}

func Parse(input io.Reader, maxBytes int64) (Document, []Issue, error) {
	if input == nil {
		return Document{}, nil, errors.New("Gemini source input is required")
	}
	if maxBytes == 0 {
		maxBytes = MaxFileBytes
	}
	if maxBytes < 1 || maxBytes > MaxFileBytes {
		return Document{}, nil, errors.New("Gemini source byte limit is invalid")
	}
	data, err := io.ReadAll(&boundedReader{input, maxBytes + 1})
	if err != nil {
		if errors.Is(err, errSourceTooLarge) {
			return Document{}, nil, fmt.Errorf("Gemini source exceeds %d bytes", maxBytes)
		}
		return Document{}, nil, fmt.Errorf("read Gemini source: %w", err)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return Document{}, nil, fmt.Errorf("parse Gemini source: %w", err)
	}
	if jsonDepth(value, 0) > MaxJSONDepth {
		return Document{}, nil, errors.New("Gemini source JSON exceeds maximum depth")
	}
	issues := make([]Issue, 0)
	document := Document{}
	switch typed := value.(type) {
	case []any:
		for index, raw := range typed {
			event, found := parseEvent(raw, index+1)
			if !found {
				issues = append(issues, Issue{index + 1, "event_not_object"})
				continue
			}
			document.Events = append(document.Events, event)
		}
	case map[string]any:
		document.SessionID, document.ConversationID = firstNonEmpty(stringValue(typed["sessionId"]), "gemini-session"), stringValue(typed["conversationId"])
		rawMessages, ok := typed["messages"].([]any)
		if !ok {
			if _, present := typed["messages"]; present {
				issues = append(issues, Issue{0, "messages_not_array"})
			}
			return document, issues, nil
		}
		for index, raw := range rawMessages {
			message, found := parseMessage(raw, index+1)
			if !found {
				issues = append(issues, Issue{index + 1, "message_not_object"})
				continue
			}
			document.Messages = append(document.Messages, message)
		}
	default:
		return Document{}, nil, errors.New("Gemini source must be an object or event array")
	}
	return document, issues, nil
}

func Chunks(input io.Reader, source Source) ([]store.Chunk, []Issue, error) {
	if source.FilePath == "" {
		return nil, nil, errors.New("Gemini source file identity is required")
	}
	document, issues, err := Parse(input, source.MaxBytes)
	if err != nil {
		return nil, issues, err
	}
	projectPath, projectName := projectFromPath(source.FilePath)
	chunks := make([]store.Chunk, 0)
	if len(document.Events) > 0 {
		for index, event := range document.Events {
			session := firstNonEmpty(event.SessionID, "gemini-logs")
			chunks = append(chunks, assistantChunks(event.Content, projectPath, projectName, session, timestamp(event.Timestamp, event.HasTime, index+1), source, index+1, "")...)
		}
		return chunks, issues, nil
	}
	var pending *Message
	fragments := make([]string, 0)
	ops := make([]operation, 0)
	model := ""
	finalize := func() {
		if pending == nil {
			return
		}
		at := timestamp(pending.Timestamp, pending.HasTime, pendingIndex(pending, document.Messages))
		turns := turnChunks(pending.Content, strings.Join(fragments, "\n"), projectPath, projectName, document.SessionID, at, source, pendingIndex(pending, document.Messages), model)
		if len(turns) > 0 {
			files := fileChunks(ops, projectPath, projectName, document.SessionID, at, source, pendingIndex(pending, document.Messages), turns[0].ID)
			if len(files) > 0 {
				turns[0].ChildChunkIDs = make([]string, len(files))
				for i := range files {
					turns[0].ChildChunkIDs[i] = files[i].ID
				}
			}
			chunks = append(chunks, turns...)
			chunks = append(chunks, files...)
		}
		pending = nil
		fragments = nil
		ops = nil
	}
	for index, message := range document.Messages {
		at := timestamp(message.Timestamp, message.HasTime, index+1)
		switch message.Type {
		case "user":
			finalize()
			copy := message
			pending = &copy
		case "gemini":
			if message.Model != "" {
				model = message.Model
			}
			combined := geminiText(message)
			found := toolOperations(message.ToolCalls)
			if pending == nil {
				turns := assistantChunks(combined, projectPath, projectName, document.SessionID, at, source, index+1, model)
				if len(turns) > 0 {
					files := fileChunks(found, projectPath, projectName, document.SessionID, at, source, index+1, turns[0].ID)
					if len(files) > 0 {
						turns[0].ChildChunkIDs = make([]string, len(files))
						for i := range files {
							turns[0].ChildChunkIDs[i] = files[i].ID
						}
					}
					chunks = append(chunks, turns...)
					chunks = append(chunks, files...)
				}
			} else {
				if combined != "" {
					fragments = append(fragments, combined)
				}
				ops = append(ops, found...)
			}
		case "info":
			chunks = append(chunks, assistantChunks(message.Content, projectPath, projectName, document.SessionID, at, source, index+1, model)...)
		default:
			chunks = append(chunks, assistantChunks(jsonValue(message.Raw), projectPath, projectName, document.SessionID, at, source, index+1, model)...)
		}
	}
	finalize()
	return chunks, issues, nil
}

func parseMessage(raw any, index int) (Message, bool) {
	object, ok := raw.(map[string]any)
	if !ok {
		return Message{}, false
	}
	message := Message{ID: firstNonEmpty(stringValue(object["id"]), fmt.Sprint(index)), Type: stringValue(object["type"]), Content: valueText(object["content"]), Model: stringValue(object["model"]), Raw: object}
	message.Timestamp, message.HasTime = parseTimestamp(object["timestamp"])
	if thoughts, ok := object["thoughts"].([]any); ok {
		for _, thought := range thoughts {
			message.Thoughts = append(message.Thoughts, valueText(thought))
		}
	}
	if calls, ok := object["toolCalls"].([]any); ok {
		for callIndex, rawCall := range calls {
			if call, ok := rawCall.(map[string]any); ok {
				message.ToolCalls = append(message.ToolCalls, ToolCall{ID: firstNonEmpty(stringValue(call["id"]), fmt.Sprint(callIndex+1)), Name: stringValue(call["name"]), Args: call["args"], ResultDisplay: stringValue(call["resultDisplay"])})
			}
		}
	}
	return message, true
}
func parseEvent(raw any, index int) (Event, bool) {
	object, ok := raw.(map[string]any)
	if !ok {
		return Event{}, false
	}
	at, has := parseTimestamp(object["timestamp"])
	return Event{ID: firstNonEmpty(stringValue(object["id"]), fmt.Sprint(index)), SessionID: stringValue(object["sessionId"]), Content: jsonValue(object), Timestamp: at, HasTime: has}, true
}
func parseTimestamp(value any) (time.Time, bool) {
	text := stringValue(value)
	if text == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}
func geminiText(message Message) string {
	parts := make([]string, 0, 3)
	if message.Content != "" {
		parts = append(parts, message.Content)
	}
	if len(message.Thoughts) > 0 {
		parts = append(parts, "Thoughts:\n"+strings.Join(message.Thoughts, "\n"))
	}
	if len(message.ToolCalls) > 0 {
		rendered := make([]string, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			rendered = append(rendered, jsonValue(map[string]any{"id": call.ID, "name": call.Name, "args": call.Args, "resultDisplay": call.ResultDisplay}))
		}
		parts = append(parts, "Tool calls:\n"+strings.Join(rendered, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

type operation struct{ path, kind, summary string }

func toolOperations(calls []ToolCall) []operation {
	ops := make([]operation, 0)
	for _, call := range calls {
		if call.Name == "apply_patch" || call.Name == "applyPatch" || call.Name == "patch" {
			if args, ok := call.Args.(map[string]any); ok {
				ops = append(ops, patchOperations(stringValue(args["patch"]))...)
			}
			ops = append(ops, patchOperations(call.ResultDisplay)...)
		}
		if call.Name == "shell" || call.Name == "shell_command" || call.Name == "bash" {
			if args, ok := call.Args.(map[string]any); ok {
				ops = append(ops, shellOperations(stringValue(args["command"]))...)
			}
		}
	}
	return ops
}
func patchOperations(text string) []operation {
	ops := make([]operation, 0)
	for _, line := range strings.Split(text, "\n") {
		for _, rule := range []struct{ prefix, kind, summary string }{{"*** Add File: ", "write", "Added file"}, {"*** Update File: ", "edit", "Updated file"}, {"*** Delete File: ", "delete", "Deleted file"}, {"*** Move to: ", "move", "Moved file"}} {
			if path := strings.TrimSpace(strings.TrimPrefix(line, rule.prefix)); path != line {
				if path != "" {
					ops = append(ops, operation{path, rule.kind, rule.summary})
				}
				break
			}
		}
	}
	return ops
}

type shellRule struct {
	expression    *regexp.Regexp
	kind, summary string
}

var shellRules = []shellRule{{regexp.MustCompile(`\brm\s+(?:-[^\s]+\s+)?([^\s]+)`), "delete", "Deleted file"}, {regexp.MustCompile(`\bmv\s+[^\s]+\s+([^\s]+)`), "move", "Moved file"}, {regexp.MustCompile(`\bcp\s+[^\s]+\s+([^\s]+)`), "write", "Copied file"}, {regexp.MustCompile(`\btouch\s+([^\s]+)`), "write", "Touched file"}, {regexp.MustCompile(`\bmkdir\s+(?:-p\s+)?([^\s]+)`), "write", "Created directory"}, {regexp.MustCompile(`>\s*([^\s]+)`), "write", "Wrote file"}, {regexp.MustCompile(`\btee\s+(?:-a\s+)?([^\s]+)`), "write", "Wrote file"}, {regexp.MustCompile(`\bsed\s+-i.*\s+([^\s]+)$`), "edit", "Edited file"}}

func shellOperations(command string) []operation {
	for _, rule := range shellRules {
		if match := rule.expression.FindStringSubmatch(command); len(match) > 1 {
			return []operation{{match[1], rule.kind, rule.summary}}
		}
	}
	return nil
}
func projectFromPath(path string) (string, string) {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for index, part := range parts {
		if part == "tmp" && index+1 < len(parts) && parts[index+1] != "" {
			return "/gemini/" + parts[index+1], parts[index+1]
		}
	}
	return "/gemini/unknown", "unknown"
}
func turnChunks(user, assistant, projectPath, projectName, session string, at time.Time, source Source, line int, model string) []store.Chunk {
	return makeChunks("In project "+projectName+", user asked:\n"+user+"\n\nAssistant responded:\n"+strings.TrimSpace(assistant), projectPath, projectName, session, at, source, line, model)
}
func assistantChunks(content, projectPath, projectName, session string, at time.Time, source Source, line int, model string) []store.Chunk {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return makeChunks("In project "+projectName+", assistant responded:\n"+content, projectPath, projectName, session, at, source, line, model)
}
func makeChunks(content, projectPath, projectName, session string, at time.Time, source Source, line int, model string) []store.Chunk {
	parts, base := splitContent(content), chunkID(content, session, timestampKey(at))
	chunks := make([]store.Chunk, 0, len(parts))
	for index, part := range parts {
		id, parent := base, ""
		if len(parts) > 1 {
			part = fmt.Sprintf("[Part %d/%d] %s", index+1, len(parts), part)
			id = chunkID(part, session, timestampKey(at)+fmt.Sprintf("_part%d", index+1))
			if index > 0 {
				parent = base
			}
		}
		chunks = append(chunks, store.Chunk{ID: id, Content: part, ChunkType: "turn", SessionID: session, ProjectPath: projectPath, ProjectName: projectName, Timestamp: at, Model: model, SourceFile: source.FilePath, SourceLine: int64(line), ParentChunkID: parent, MachineID: source.MachineID})
	}
	return chunks
}
func fileChunks(ops []operation, projectPath, projectName, session string, at time.Time, source Source, line int, parent string) []store.Chunk {
	chunks := make([]store.Chunk, 0, len(ops))
	for index, op := range ops {
		content := fmt.Sprintf("In project %s, file %s was %s.\nSummary: %s", projectName, op.path, op.kind, op.summary)
		chunks = append(chunks, store.Chunk{ID: chunkID(content, session, timestampKey(at)+op.path+fmt.Sprint(index)), Content: content, ChunkType: "file_change", SessionID: session, ProjectPath: projectPath, ProjectName: projectName, Timestamp: at, FilePath: op.path, Operation: op.kind, SourceFile: source.FilePath, SourceLine: int64(line), ParentChunkID: parent, MachineID: source.MachineID})
	}
	return chunks
}
func pendingIndex(want *Message, messages []Message) int {
	for index := range messages {
		if messages[index].ID == want.ID {
			return index + 1
		}
	}
	return 1
}
func timestamp(at time.Time, has bool, index int) time.Time {
	if has {
		return at
	}
	return time.Unix(0, int64(index)*1000).UTC()
}
func valueText(value any) string {
	if text := stringValue(value); text != "" {
		return text
	}
	return jsonValue(value)
}
func stringValue(value any) string { text, _ := value.(string); return text }
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func jsonValue(value any) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}
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
