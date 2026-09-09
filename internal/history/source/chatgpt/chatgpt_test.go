package chatgpt

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAndChunksGoldenExport(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("testdata", "conversations.json"))
	if err != nil {
		t.Fatal(err)
	}
	conversations, issues, err := Parse(bytes.NewReader(payload), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 || len(conversations) != 2 {
		t.Fatalf("parse = %#v %#v", conversations, issues)
	}
	first := conversations[0]
	if first.ID != "conversation-1" || first.Title != "Parser work" || len(first.Messages) != 2 {
		t.Fatalf("conversation = %#v", first)
	}
	if first.Messages[0].NodeID != "node-user" || first.Messages[0].Author != "Test User" || first.Messages[0].Content != "Please fix\nthe parser" || first.Messages[1].ParentID != "node-user" {
		t.Fatalf("tree metadata = %#v", first.Messages)
	}
	chunks, issues, err := Chunks(bytes.NewReader(payload), Source{FilePath: "/exports/conversations.json", MachineID: "mac"})
	if err != nil || len(issues) != 0 || len(chunks) != 2 {
		t.Fatalf("chunks = %#v issues=%#v err=%v", chunks, issues, err)
	}
	if chunks[0].Content != "In ChatGPT conversation Parser work, user asked:\nPlease fix\nthe parser\n\nChatGPT responded:\nIt is fixed." || chunks[0].SessionID != "conversation-1" || chunks[0].UserUUID != "message-user" || chunks[0].AssistantUUID != "message-assistant" || chunks[0].MachineID != "mac" {
		t.Fatalf("first chunk = %#v", chunks[0])
	}
	if chunks[1].SessionID != "conversation-2" || !strings.Contains(chunks[1].Content, "A file changed.") {
		t.Fatalf("second chunk = %#v", chunks[1])
	}
	again, _, err := Chunks(bytes.NewReader(payload), Source{FilePath: "/exports/conversations.json", MachineID: "mac"})
	if err != nil || chunks[0].ID != again[0].ID {
		t.Fatalf("IDs not deterministic: %v %#v", err, again)
	}
}

func TestParseIsolationAndBounds(t *testing.T) {
	deep := strings.Repeat("{\"x\":", MaxJSONDepth+2) + "0" + strings.Repeat("}", MaxJSONDepth+2)
	payload := "[{\"id\":\"good\",\"messages\":[]}, 4, {\"id\":\"bad\",\"mapping\":{\"x\":4}}]"
	conversations, issues, err := Parse(strings.NewReader(payload), int64(len(payload)+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 2 || len(issues) != 2 || issues[0].Reason != "conversation_not_object" || issues[1].Reason != "node_not_object" {
		t.Fatalf("isolation = %#v %#v", conversations, issues)
	}
	if _, _, err := Parse(strings.NewReader(deep), int64(len(deep)+1)); err == nil {
		t.Fatal("expected depth error")
	}
	if _, _, err := Parse(strings.NewReader("["), 10); err == nil {
		t.Fatal("expected JSON error")
	}
	if _, _, err := Parse(strings.NewReader("{}"), 10); err == nil {
		t.Fatal("expected conversations error")
	}
	if _, _, err := Parse(strings.NewReader("[]"), 1); err == nil {
		t.Fatal("expected size error")
	}
	if _, _, err := Parse(nil, 1); err == nil {
		t.Fatal("expected nil reader")
	}
	if _, _, err := Parse(strings.NewReader("[]"), MaxFileBytes+1); err == nil {
		t.Fatal("expected invalid bound")
	}
}

func TestHelpersAndChunkParts(t *testing.T) {
	if role, name := messageRole(map[string]any{"author": map[string]any{"role": "Assistant", "name": "A"}}); role != "assistant" || name != "A" {
		t.Fatalf("author = %q %q", role, name)
	}
	if role, _ := messageRole(map[string]any{"sender": "Tool"}); role != "tool" {
		t.Fatalf("role = %q", role)
	}
	if textFromContent(map[string]any{"content": []any{"a", map[string]any{"value": "b"}}}) != "a\nb" {
		t.Fatal("nested content")
	}
	if _, ok := parseTimestamp("bad"); ok {
		t.Fatal("bad timestamp")
	}
	if got, ok := parseTimestamp(1.5); !ok || !got.Equal(time.Unix(1, 500000000).UTC()) {
		t.Fatalf("unix timestamp = %v %v", got, ok)
	}
	if !messageTimestamp(Message{}, 3).Equal(time.Unix(0, 3000).UTC()) {
		t.Fatal("fallback timestamp")
	}
	if _, _, err := Chunks(strings.NewReader("[]"), Source{}); err == nil {
		t.Fatal("missing provenance")
	}
	long := strings.Repeat("x", MaxChunkContentLength+20)
	if len(splitContent(long)) < 2 {
		t.Fatal("content should split")
	}
	if chunkID("a", "b", "c") != chunkID("a", "b", "c") || chunkID("a", "b", "c") == chunkID("a", "b", "d") {
		t.Fatal("chunk id")
	}
}

func TestReadError(t *testing.T) {
	if _, _, err := Parse(failingReader{}, 10); err == nil || !strings.Contains(err.Error(), "read ChatGPT export") {
		t.Fatalf("read error = %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }
