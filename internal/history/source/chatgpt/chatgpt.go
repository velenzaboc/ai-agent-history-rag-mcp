// Package chatgpt parses and chunks official ChatGPT conversations exports.
package chatgpt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
)

const (
	MaxFileBytes          int64 = 10 << 30
	MaxJSONDepth                = 50
	MaxChunkContentLength       = 8000
	ChunkOverlap                = 1500
)

var errSourceTooLarge = errors.New("ChatGPT export too large")

type Source struct {
	FilePath, MachineID string
	MaxBytes            int64
}
type Issue struct{ Conversation, NodeID, Reason string }

// Conversation preserves the export identity and deterministic message order.
type Conversation struct {
	ID, Title string
	Messages  []Message
}

// Message preserves both the export's message and mapping-node identities.
type Message struct {
	ID, NodeID, ParentID, Role, Author, Content string
	ChildIDs                                    []string
	Timestamp                                   time.Time
	HasTime                                     bool
}

func Parse(input io.Reader, maxBytes int64) ([]Conversation, []Issue, error) {
	if input == nil {
		return nil, nil, errors.New("ChatGPT export input is required")
	}
	if maxBytes == 0 {
		maxBytes = MaxFileBytes
	}
	if maxBytes < 1 || maxBytes > MaxFileBytes {
		return nil, nil, errors.New("ChatGPT export byte limit is invalid")
	}
	data, err := io.ReadAll(&boundedReader{reader: input, remain: maxBytes + 1})
	if err != nil {
		if errors.Is(err, errSourceTooLarge) {
			return nil, nil, fmt.Errorf("ChatGPT export exceeds %d bytes", maxBytes)
		}
		return nil, nil, fmt.Errorf("read ChatGPT export: %w", err)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, nil, fmt.Errorf("parse ChatGPT export: %w", err)
	}
	if jsonDepth(value, 0) > MaxJSONDepth {
		return nil, nil, errors.New("ChatGPT export JSON exceeds maximum depth")
	}
	conversationsRaw, ok := conversationsValue(value)
	if !ok {
		return nil, nil, errors.New("ChatGPT export conversations are invalid")
	}
	conversations, issues := make([]Conversation, 0, len(conversationsRaw)), make([]Issue, 0)
	for index, raw := range conversationsRaw {
		object, ok := raw.(map[string]any)
		if !ok {
			issues = append(issues, Issue{Conversation: fmt.Sprint(index + 1), Reason: "conversation_not_object"})
			continue
		}
		conversation, found := parseConversation(object, index+1, &issues)
		if found {
			conversations = append(conversations, conversation)
		}
	}
	return conversations, issues, nil
}

func Chunks(input io.Reader, source Source) ([]store.Chunk, []Issue, error) {
	if source.FilePath == "" {
		return nil, nil, errors.New("ChatGPT source file identity is required")
	}
	conversations, issues, err := Parse(input, source.MaxBytes)
	if err != nil {
		return nil, issues, err
	}
	chunks := make([]store.Chunk, 0)
	for conversationIndex, conversation := range conversations {
		var pending *Message
		for messageIndex := range conversation.Messages {
			message := conversation.Messages[messageIndex]
			if strings.TrimSpace(message.Content) == "" {
				continue
			}
			switch message.Role {
			case "user":
				copy := message
				pending = &copy
			case "assistant", "tool":
				if pending == nil {
					continue
				}
				timestamp := messageTimestamp(message, (conversationIndex+1)*100000+messageIndex+1)
				chunks = append(chunks, turnChunks(*pending, message, conversation, timestamp, source, (conversationIndex+1)*100000+messageIndex+1)...)
				pending = nil
			}
		}
	}
	return chunks, issues, nil
}

func conversationsValue(value any) ([]any, bool) {
	if list, ok := value.([]any); ok {
		return list, true
	}
	if object, ok := value.(map[string]any); ok {
		list, ok := object["conversations"].([]any)
		return list, ok
	}
	return nil, false
}

func parseConversation(object map[string]any, index int, issues *[]Issue) (Conversation, bool) {
	id := firstNonEmpty(stringValue(object["id"]), stringValue(object["conversation_id"]), fmt.Sprint(index))
	title := firstNonEmpty(stringValue(object["title"]), "Untitled")
	conversation := Conversation{ID: id, Title: title}
	if rawMessages, ok := object["messages"].([]any); ok {
		for messageIndex, raw := range rawMessages {
			message, found := parseMessage(raw, fmt.Sprint(messageIndex+1), "")
			if !found {
				*issues = append(*issues, Issue{Conversation: id, NodeID: fmt.Sprint(messageIndex + 1), Reason: "message_not_object"})
				continue
			}
			conversation.Messages = append(conversation.Messages, message)
		}
		return conversation, true
	}
	mapping, ok := object["mapping"].(map[string]any)
	if !ok {
		*issues = append(*issues, Issue{Conversation: id, Reason: "messages_missing"})
		return conversation, true
	}
	nodeIDs := make([]string, 0, len(mapping))
	for nodeID := range mapping {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	for _, nodeID := range nodeIDs {
		node, ok := mapping[nodeID].(map[string]any)
		if !ok {
			*issues = append(*issues, Issue{Conversation: id, NodeID: nodeID, Reason: "node_not_object"})
			continue
		}
		message, found := parseMessage(node["message"], nodeID, stringValue(node["parent"]))
		if !found {
			continue
		}
		if children, ok := node["children"].([]any); ok {
			for _, child := range children {
				if text := stringValue(child); text != "" {
					message.ChildIDs = append(message.ChildIDs, text)
				}
			}
		}
		conversation.Messages = append(conversation.Messages, message)
	}
	sort.SliceStable(conversation.Messages, func(left, right int) bool {
		a, b := conversation.Messages[left], conversation.Messages[right]
		if a.HasTime != b.HasTime {
			return a.HasTime
		}
		if a.HasTime && !a.Timestamp.Equal(b.Timestamp) {
			return a.Timestamp.Before(b.Timestamp)
		}
		return a.NodeID < b.NodeID
	})
	return conversation, true
}

func parseMessage(raw any, nodeID, parentID string) (Message, bool) {
	object, ok := raw.(map[string]any)
	if !ok {
		return Message{}, false
	}
	role, author := messageRole(object)
	message := Message{ID: firstNonEmpty(stringValue(object["id"]), stringValue(object["message_id"]), nodeID), NodeID: nodeID, ParentID: parentID, Role: role, Author: author, Content: messageText(object)}
	if timestamp, valid := parseTimestamp(object["create_time"]); valid {
		message.Timestamp, message.HasTime = timestamp, true
	} else if timestamp, valid := parseTimestamp(object["created_at"]); valid {
		message.Timestamp, message.HasTime = timestamp, true
	} else if timestamp, valid := parseTimestamp(object["update_time"]); valid {
		message.Timestamp, message.HasTime = timestamp, true
	}
	return message, true
}

func messageRole(message map[string]any) (string, string) {
	if author, ok := message["author"].(map[string]any); ok {
		role, name := strings.ToLower(stringValue(author["role"])), stringValue(author["name"])
		if role != "" {
			return role, name
		}
	}
	role := firstNonEmpty(stringValue(message["role"]), stringValue(message["sender"]))
	return strings.ToLower(role), ""
}
func messageText(message map[string]any) string {
	for _, key := range []string{"content", "text", "message"} {
		if text := textFromContent(message[key]); text != "" {
			return text
		}
	}
	return ""
}
func textFromContent(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, child := range typed {
			if text := textFromContent(child); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if parts, ok := typed["parts"].([]any); ok {
			return textFromContent(parts)
		}
		for _, key := range []string{"text", "value", "content"} {
			if text := textFromContent(typed[key]); text != "" {
				return text
			}
		}
	}
	return ""
}
func parseTimestamp(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case float64:
		return time.Unix(int64(typed), int64((typed-float64(int64(typed)))*1e9)).UTC(), true
	case string:
		if typed == "" {
			return time.Time{}, false
		}
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func turnChunks(user, assistant Message, conversation Conversation, timestamp time.Time, source Source, line int) []store.Chunk {
	content := "In ChatGPT conversation " + conversation.Title + ", user asked:\n" + user.Content + "\n\nChatGPT responded:\n" + assistant.Content
	parts, baseID := splitContent(content), chunkID(content, conversation.ID, timestampKey(timestamp))
	chunks := make([]store.Chunk, 0, len(parts))
	for index, part := range parts {
		id, parent := baseID, ""
		if len(parts) > 1 {
			part = fmt.Sprintf("[Part %d/%d] %s", index+1, len(parts), part)
			id = chunkID(part, conversation.ID, timestampKey(timestamp)+fmt.Sprintf("_part%d", index+1))
			if index > 0 {
				parent = baseID
			}
		}
		chunks = append(chunks, store.Chunk{ID: id, Content: part, ChunkType: "turn", SessionID: conversation.ID, ProjectPath: "/chatgpt/export", ProjectName: "ChatGPT", Timestamp: timestamp, UserUUID: user.ID, AssistantUUID: assistant.ID, SourceFile: source.FilePath, SourceLine: int64(line), ParentChunkID: parent, MachineID: source.MachineID})
	}
	return chunks
}

func messageTimestamp(message Message, fallbackLine int) time.Time {
	if message.HasTime {
		return message.Timestamp
	}
	return time.Unix(0, int64(fallbackLine)*1000).UTC()
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
