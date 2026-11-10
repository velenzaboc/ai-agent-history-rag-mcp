// Package claude parses and chunks Claude Code JSONL histories for the native daemon.
package claude

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
	"strings"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
)

const (
	MaxFileBytes          int64 = 10 << 30
	MaxLineBytes                = 32 << 20
	MaxJSONDepth                = 50
	MaxChunkContentLength       = 8000
	ChunkOverlap                = 1500
	userContentTruncate         = 200
)

type Source struct {
	ProjectEncoded string
	FilePath       string
	MachineID      string
	MaxBytes       int64
}

type Issue struct {
	Line   int
	Reason string
}

type Message struct {
	Role    string
	Model   string
	Content any
}

type Entry struct {
	Type           string
	Message        *Message
	Summary        string
	Title          string
	PRUrl          string
	PRNumber       string
	PRRepository   string
	CompactSummary bool
	UUID           string
	Timestamp      time.Time
	HasTimestamp   bool
	SessionID      string
	VisibleOnly    bool
	MetadataOnly   bool
}

type LocatedEntry struct {
	Entry Entry
	Line  int
}

func Parse(input io.Reader, maxBytes int64) ([]LocatedEntry, []Issue, error) {
	if input == nil {
		return nil, nil, errors.New("Claude source input is required")
	}
	if maxBytes == 0 {
		maxBytes = MaxFileBytes
	}
	if maxBytes < 1 || maxBytes > MaxFileBytes {
		return nil, nil, errors.New("Claude source byte limit is invalid")
	}
	reader := bufio.NewReaderSize(&boundedReader{reader: input, remain: maxBytes + 1}, 64<<10)
	entries := make([]LocatedEntry, 0)
	issues := make([]Issue, 0)
	for lineNumber := 1; ; lineNumber++ {
		line, tooLong, done, err := readLine(reader)
		if err != nil {
			if errors.Is(err, errSourceTooLarge) {
				return nil, nil, fmt.Errorf("Claude source exceeds %d bytes", maxBytes)
			}
			return nil, nil, fmt.Errorf("read Claude source: %w", err)
		}
		if tooLong {
			issues = append(issues, Issue{Line: lineNumber, Reason: "line_too_long"})
		} else if text := bytes.TrimSpace(line); len(text) != 0 {
			entry, reason := parseEntry(text)
			if reason != "" {
				issues = append(issues, Issue{Line: lineNumber, Reason: reason})
			} else {
				entries = append(entries, LocatedEntry{Entry: entry, Line: lineNumber})
			}
		}
		if done {
			return entries, issues, nil
		}
	}
}

func Chunks(input io.Reader, source Source) ([]store.Chunk, []Issue, error) {
	if source.FilePath == "" {
		return nil, nil, errors.New("Claude source file identity is required")
	}
	projectEncoded := strings.TrimSpace(source.ProjectEncoded)
	if projectEncoded == "" {
		projectEncoded = projectEncodedFromPath(source.FilePath)
	}
	projectPath := DecodeProjectPath(projectEncoded)
	entries, issues, err := Parse(input, source.MaxBytes)
	if err != nil {
		return nil, issues, err
	}
	defaultSession := sessionIDFromPath(source.FilePath)
	var sessionTime time.Time
	var titles []string
	hasSummaryEntry := false
	for _, located := range entries {
		e := located.Entry
		if e.SessionID != "" && defaultSession == sessionIDFromPath(source.FilePath) {
			defaultSession = e.SessionID
		}
		if e.HasTimestamp && e.Timestamp.Unix() > 1 && sessionTime.IsZero() {
			sessionTime = e.Timestamp
		}
		if t := strings.TrimSpace(e.Title); t != "" {
			titles = append(titles, t)
		}
		if e.CompactSummary || e.Type == "summary" {
			hasSummaryEntry = true
		}
	}
	sessionTitle := strings.Join(titles, " / ")
	chunks := make([]store.Chunk, 0)
	var pending *LocatedEntry
	emittedTitleSummary := false
	for _, located := range entries {
		entry := located.Entry
		if entry.SessionID == "" {
			entry.SessionID = defaultSession
		}
		if (!entry.HasTimestamp || entry.Timestamp.Unix() <= 1) && !sessionTime.IsZero() {
			entry.Timestamp, entry.HasTimestamp = sessionTime, true
		} else if entry.HasTimestamp && entry.Timestamp.Unix() > 1 {
			sessionTime = entry.Timestamp
		}
		located.Entry = entry
		if entry.CompactSummary || entry.Type == "summary" {
			if sessionTitle != "" && entry.Title == "" {
				entry.Title = sessionTitle
			}
			if chunk, ok := summaryChunk(entry, located.Line, projectPath, source); ok {
				chunks = append(chunks, chunk)
			}
			continue
		}
		switch entry.Type {
		case "custom-title", "agent-name":
			if !hasSummaryEntry && !emittedTitleSummary && sessionTitle != "" {
				titleEntry := entry
				titleEntry.Summary = sessionTitle
				if chunk, ok := summaryChunk(titleEntry, located.Line, projectPath, source); ok {
					chunks = append(chunks, chunk)
					emittedTitleSummary = true
				}
			}
		case "pr-link":
			if chunk, ok := prLinkChunk(entry, located.Line, projectPath, source); ok {
				chunks = append(chunks, chunk)
			}
		case "user":
			copy := located
			pending = &copy
		case "assistant":
			if pending == nil {
				chunks = append(chunks, fileChunks(entry, "[unpaired assistant message]", located.Line, "", projectPath, source)...)
				continue
			}
			turns := turnChunks(pending.Entry, entry, pending.Line, located.Line, projectPath, source)
			if len(turns) > 0 {
				files := fileChunks(entry, textContent(pending.Entry.Message), located.Line, turns[0].ID, projectPath, source)
				if len(files) > 0 {
					turns[0].ChildChunkIDs = make([]string, len(files))
					for index := range files {
						turns[0].ChildChunkIDs[index] = files[index].ID
					}
				}
				chunks = append(chunks, turns...)
				chunks = append(chunks, files...)
			}
			pending = nil
		}
	}
	return chunks, issues, nil
}

func projectEncodedFromPath(filePath string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(filePath)), "/")
	for i, part := range parts {
		if part == "projects" && i+1 < len(parts)-1 && parts[i+1] != "" {
			return parts[i+1]
		}
	}
	return ""
}

func sessionIDFromPath(filePath string) string {
	clean := filepath.ToSlash(filepath.Clean(filePath))
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		if part == "subagents" && i > 0 && parts[i-1] != "" && parts[i-1] != "." {
			return parts[i-1]
		}
	}
	stem := strings.TrimSuffix(filepath.Base(clean), filepath.Ext(clean))
	if stem == "" || stem == "." || stem == "/" {
		return "unknown"
	}
	return stem
}

func DecodeProjectPath(encoded string) string {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return "/unknown"
	}
	if strings.Contains(encoded, "..") {
		return "/invalid/path"
	}
	encoded = strings.TrimPrefix(encoded, "-")
	decoded := "/" + strings.ReplaceAll(encoded, "-", "/")
	if strings.Contains(decoded, "..") || !strings.HasPrefix(decoded, "/") {
		return "/invalid/path"
	}
	return decoded
}

func parseEntry(raw []byte) (Entry, string) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return Entry{}, "invalid_json"
	}
	if jsonDepth(value, 0) > MaxJSONDepth {
		return Entry{}, "json_too_deep"
	}
	object, ok := value.(map[string]any)
	if !ok {
		return Entry{}, "entry_not_object"
	}
	typeValue, ok := object["type"].(string)
	if !ok || typeValue == "" {
		return Entry{}, "missing_type"
	}
	entry := Entry{
		Type:           typeValue,
		Summary:        stringValue(object["summary"]),
		Title:          firstNonEmpty(stringValue(object["customTitle"]), stringValue(object["agentName"]), stringValue(object["title"])),
		PRUrl:          stringValue(object["prUrl"]),
		PRNumber:       numberOrString(object["prNumber"]),
		PRRepository:   stringValue(object["prRepository"]),
		UUID:           stringValue(object["uuid"]),
		SessionID:      stringValue(object["sessionId"]),
		CompactSummary: boolValue(object["isCompactSummary"]),
		VisibleOnly:    boolValue(object["isVisibleInTranscriptOnly"]),
		MetadataOnly:   boolValue(object["isMeta"]),
	}
	if timestamp := stringValue(object["timestamp"]); timestamp != "" {
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return Entry{}, "invalid_timestamp"
		}
		entry.Timestamp, entry.HasTimestamp = parsed.UTC(), true
	}
	if rawMessage, present := object["message"]; present && rawMessage != nil {
		message, ok := parseMessage(rawMessage, typeValue)
		if ok {
			entry.Message = message
		}
	}
	return entry, ""
}

func parseMessage(value any, entryType string) (*Message, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	content, exists := object["content"]
	if !exists {
		return nil, false
	}
	if entryType == "user" {
		switch content.(type) {
		case string, []any:
		default:
			return nil, false
		}
	}
	if entryType == "assistant" {
		if _, ok := content.([]any); !ok {
			return nil, false
		}
	}
	return &Message{Role: stringValue(object["role"]), Model: stringValue(object["model"]), Content: content}, true
}

func turnChunks(user, assistant Entry, sourceLine, consumedLine int, projectPath string, source Source) []store.Chunk {
	userText := strings.TrimSpace(textContent(user.Message))
	assistantText := strings.TrimSpace(textContent(assistant.Message))
	if userText == "" && assistantText == "" {
		return nil
	}
	projectName := filepath.Base(projectPath)
	content := ""
	switch {
	case userText != "" && assistantText != "":
		content = "In project " + projectName + ", user asked:\n" + userText + "\n\nAssistant responded:\n" + assistantText
	case userText != "":
		content = "In project " + projectName + ", user asked:\n" + userText + "\n\n[No assistant response]"
	default:
		content = "In project " + projectName + ", assistant response:\n" + assistantText
	}
	timestamp := entryTimestamp(assistant, user, consumedLine)
	session := firstNonEmpty(user.SessionID, assistant.SessionID, "unknown")
	parts := splitContent(content)
	baseID := chunkID(content, session, canonicalTimestamp(timestamp))
	chunks := make([]store.Chunk, 0, len(parts))
	for index, part := range parts {
		id, parent := baseID, ""
		if len(parts) > 1 {
			part = fmt.Sprintf("[Part %d/%d] %s", index+1, len(parts), part)
			id = chunkID(part, session, canonicalTimestamp(timestamp)+fmt.Sprintf("_part%d", index+1))
			if index > 0 {
				parent = baseID
			}
		}
		chunks = append(chunks, store.Chunk{ID: id, Content: part, ChunkType: "turn", SessionID: session, ProjectPath: projectPath, ProjectName: projectName, Timestamp: timestamp, UserUUID: user.UUID, AssistantUUID: assistant.UUID, Model: messageModel(assistant.Message), SourceFile: source.FilePath, SourceLine: int64(sourceLine), ParentChunkID: parent, MachineID: source.MachineID})
	}
	return chunks
}

func summaryChunk(entry Entry, sourceLine int, projectPath string, source Source) (store.Chunk, bool) {
	text := strings.TrimSpace(firstNonEmpty(entry.Summary, textContent(entry.Message)))
	if text == "" {
		return store.Chunk{}, false
	}
	if entry.Title != "" && entry.Title != text {
		text = "Title: " + entry.Title + "\n" + text
	}
	timestamp := entryTimestamp(entry, Entry{}, sourceLine)
	session, projectName := firstNonEmpty(entry.SessionID, sessionIDFromPath(source.FilePath), "unknown"), filepath.Base(projectPath)
	content := "Session summary for " + projectName + ":\n" + text
	return store.Chunk{ID: chunkID(content, session, canonicalTimestamp(timestamp)), Content: content, ChunkType: "summary", SessionID: session, ProjectPath: projectPath, ProjectName: projectName, Timestamp: timestamp, SourceFile: source.FilePath, SourceLine: int64(sourceLine), MachineID: source.MachineID}, true
}

func prLinkChunk(entry Entry, sourceLine int, projectPath string, source Source) (store.Chunk, bool) {
	if entry.PRUrl == "" && entry.PRNumber == "" && entry.PRRepository == "" {
		return store.Chunk{}, false
	}
	timestamp := entryTimestamp(entry, Entry{}, sourceLine)
	session, projectName := firstNonEmpty(entry.SessionID, sessionIDFromPath(source.FilePath), "unknown"), filepath.Base(projectPath)
	content := fmt.Sprintf("In project %s, linked pull request", projectName)
	if entry.PRRepository != "" {
		content += " in " + entry.PRRepository
	}
	if entry.PRNumber != "" {
		content += " #" + entry.PRNumber
	}
	if entry.PRUrl != "" {
		content += ": " + entry.PRUrl
	}
	return store.Chunk{ID: chunkID(content, session, canonicalTimestamp(timestamp)), Content: content, ChunkType: "turn", SessionID: session, ProjectPath: projectPath, ProjectName: projectName, Timestamp: timestamp, SourceFile: source.FilePath, SourceLine: int64(sourceLine), MachineID: source.MachineID}, true
}

func fileChunks(entry Entry, userText string, sourceLine int, parent, projectPath string, source Source) []store.Chunk {
	if entry.Message == nil {
		return nil
	}
	operations := fileOperations(entry.Message)
	if len(operations) == 0 {
		return nil
	}
	timestamp, projectName, session := entryTimestamp(entry, Entry{}, sourceLine), filepath.Base(projectPath), firstNonEmpty(entry.SessionID, "unknown")
	chunks := make([]store.Chunk, 0, len(operations))
	for index, operation := range operations {
		prompt := userText
		if len(prompt) > userContentTruncate {
			prompt = prompt[:userContentTruncate] + "..."
		}
		content := fmt.Sprintf("In project %s, file %s was %s. %s. This was in response to: %s", projectName, operation.path, operation.kind, operation.summary, prompt)
		toolID := operation.id
		if toolID == "" {
			toolID = fmt.Sprintf("idx%d", index)
		}
		chunks = append(chunks, store.Chunk{ID: chunkID(content, session, canonicalTimestamp(timestamp)+operation.path+toolID), Content: content, ChunkType: "file_change", SessionID: session, ProjectPath: projectPath, ProjectName: projectName, Timestamp: timestamp, AssistantUUID: entry.UUID, FilePath: operation.path, Operation: operation.kind, Model: messageModel(entry.Message), SourceFile: source.FilePath, SourceLine: int64(sourceLine), ParentChunkID: parent, MachineID: source.MachineID})
	}
	return chunks
}

type operation struct{ path, kind, summary, id string }

func fileOperations(message *Message) []operation {
	blocks, ok := message.Content.([]any)
	if !ok {
		return nil
	}
	operations := make([]operation, 0)
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok || stringValue(block["type"]) != "tool_use" {
			continue
		}
		input, _ := block["input"].(map[string]any)
		path, name := stringValue(input["file_path"]), stringValue(block["name"])
		if path == "" {
			path = "unknown"
		}
		switch name {
		case "Write":
			operations = append(operations, operation{path, "write", "Created/overwrote file", stringValue(block["id"])})
		case "Edit":
			old, oldOK := input["old_string"].(string)
			new, newOK := input["new_string"].(string)
			if !oldOK || !newOK {
				continue
			}
			operations = append(operations, operation{path, "edit", fmt.Sprintf("Replaced '%s%s' with '%s%s'", truncate(old, 100), ellipsis(old, 100), truncate(new, 100), ellipsis(new, 100)), stringValue(block["id"])})
		}
	}
	return operations
}

func textContent(message *Message) string {
	if message == nil {
		return ""
	}
	if text, ok := message.Content.(string); ok {
		return text
	}
	blocks, ok := message.Content.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(block["type"]) {
		case "text":
			parts = append(parts, stringValue(block["text"]))
		case "tool_use":
			input, _ := block["input"].(map[string]any)
			name := stringValue(block["name"])
			switch name {
			case "Read", "Edit", "Write":
				parts = append(parts, "[Used "+name+" on "+firstNonEmpty(stringValue(input["file_path"]), "unknown")+"]")
			case "Bash":
				command := strings.NewReplacer("\n", " ", "\r", " ").Replace(truncate(stringValue(input["command"]), 100))
				parts = append(parts, "[Ran command: "+command+"]")
			default:
				parts = append(parts, "[Used "+name+"]")
			}
		case "tool_result":
			if result, ok := block["content"].(string); ok && len(result) < 500 {
				parts = append(parts, "[Result: "+result+"]")
			} else {
				parts = append(parts, "[Tool result truncated]")
			}
		}
	}
	return strings.Join(parts, "\n")
}

func messageModel(message *Message) string {
	if message == nil {
		return ""
	}
	return message.Model
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
		searchStart, region := max(start, end-1000), content[max(start, end-1000):end]
		breakAt := strings.LastIndex(region, "\n\n")
		if breakAt > 0 {
			end = searchStart + breakAt + 2
		} else if candidate := max(strings.LastIndex(region, ". "), strings.LastIndex(region, "! "), strings.LastIndex(region, "? "), strings.LastIndex(region, "\n")); candidate > 0 {
			end = searchStart + candidate + 1
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

func entryTimestamp(first, second Entry, sourceLine int) time.Time {
	if first.HasTimestamp {
		return first.Timestamp
	}
	if second.HasTimestamp {
		return second.Timestamp
	}
	return time.Unix(0, int64(sourceLine)*1000).UTC()
}
func canonicalTimestamp(value time.Time) string {
	value = value.UTC()
	result := value.Format("2006-01-02 15:04:05")
	if nanos := value.Nanosecond(); nanos != 0 {
		result += "." + strings.TrimRight(fmt.Sprintf("%09d", nanos), "0")
	}
	return result + "+00:00"
}
func chunkID(content, session, timestamp string) string {
	sum := sha256.Sum256([]byte(content + session + timestamp))
	return hex.EncodeToString(sum[:])[:16]
}
func stringValue(value any) string { text, _ := value.(string); return text }
func numberOrString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return fmt.Sprintf("%.0f", typed)
	}
	return ""
}
func boolValue(value any) bool { result, _ := value.(bool); return result }
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func truncate(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
func ellipsis(value string, limit int) string {
	if len(value) > limit {
		return "..."
	}
	return ""
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

var errSourceTooLarge = errors.New("Claude source too large")

type boundedReader struct {
	reader io.Reader
	remain int64
}

func (r *boundedReader) Read(payload []byte) (int, error) {
	if r.remain == 0 {
		return 0, errSourceTooLarge
	}
	if int64(len(payload)) > r.remain {
		payload = payload[:r.remain]
	}
	n, err := r.reader.Read(payload)
	r.remain -= int64(n)
	return n, err
}

func readLine(reader *bufio.Reader) ([]byte, bool, bool, error) {
	var line []byte
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
