package gemini

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChunksGoldenSession(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("testdata", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, issues, err := Parse(bytes.NewReader(payload), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 || document.SessionID != "gemini-session-1" || document.ConversationID != "conversation-1" || len(document.Messages) != 4 {
		t.Fatalf("document = %#v issues=%#v", document, issues)
	}
	if document.Messages[1].ID != "g1" || document.Messages[1].ToolCalls[0].ID != "call1" || document.Messages[1].Thoughts[0] != "Inspect source" {
		t.Fatalf("message metadata = %#v", document.Messages[1])
	}
	chunks, issues, err := Chunks(bytes.NewReader(payload), Source{FilePath: "/tmp/project-hash/chats/session.json", MachineID: "mac"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 || len(chunks) != 5 {
		t.Fatalf("chunks=%#v issues=%#v", chunks, issues)
	}
	if !strings.Contains(chunks[0].Content, "Session saved") || !strings.Contains(chunks[1].Content, "\"type\":\"custom\"") {
		t.Fatalf("events=%#v %#v", chunks[0], chunks[1])
	}
	turn := chunks[2]
	want := "In project project-hash, user asked:\nFix the loader\n\nAssistant responded:\nI updated it.\n\nThoughts:\nInspect source\n\nTool calls:\n{\"args\":{\"patch\":\"*** Update File: loader.go\\n*** Add File: loader_test.go\"},\"id\":\"call1\",\"name\":\"apply_patch\",\"resultDisplay\":\"\"}"
	if turn.Content != want || turn.SessionID != "gemini-session-1" || turn.ProjectPath != "/gemini/project-hash" || turn.Model != "gemini-2.5" || turn.SourceLine != 1 || turn.MachineID != "mac" {
		t.Fatalf("turn=%#v", turn)
	}
	if len(turn.ChildChunkIDs) != 2 || chunks[3].FilePath != "loader.go" || chunks[3].Operation != "edit" || chunks[4].FilePath != "loader_test.go" || chunks[4].Operation != "write" || chunks[3].ParentChunkID != turn.ID {
		t.Fatalf("files=%#v %#v", chunks[3], chunks[4])
	}
	again, _, err := Chunks(bytes.NewReader(payload), Source{FilePath: "/tmp/project-hash/chats/session.json", MachineID: "mac"})
	if err != nil || again[2].ID != turn.ID {
		t.Fatalf("non deterministic: %v %#v", err, again)
	}
}

func TestLogsIsolationAndBounds(t *testing.T) {
	logs := "[{\"id\":\"one\",\"sessionId\":\"s\",\"timestamp\":\"2026-09-09T00:00:00Z\"}, 8, {\"id\":\"two\"}]"
	document, issues, err := Parse(strings.NewReader(logs), int64(len(logs)+1))
	if err != nil || len(document.Events) != 2 || len(issues) != 1 || issues[0].Reason != "event_not_object" {
		t.Fatalf("logs=%#v issues=%#v err=%v", document, issues, err)
	}
	chunks, _, err := Chunks(strings.NewReader(logs), Source{FilePath: "/logs.json"})
	if err != nil || len(chunks) != 2 || chunks[0].SessionID != "s" || chunks[1].SessionID != "gemini-logs" {
		t.Fatalf("log chunks=%#v err=%v", chunks, err)
	}
	deep := strings.Repeat("{\"x\":", MaxJSONDepth+2) + "0" + strings.Repeat("}", MaxJSONDepth+2)
	if _, _, err := Parse(strings.NewReader(deep), int64(len(deep)+1)); err == nil {
		t.Fatal("depth should fail")
	}
	if _, _, err := Parse(strings.NewReader("[]"), 1); err == nil {
		t.Fatal("size should fail")
	}
	if _, _, err := Parse(nil, 1); err == nil {
		t.Fatal("nil should fail")
	}
	if _, _, err := Parse(strings.NewReader("null"), 10); err == nil {
		t.Fatal("scalar should fail")
	}
}

func TestHelpers(t *testing.T) {
	if path, name := projectFromPath("/x/tmp/abc/chats/a.json"); path != "/gemini/abc" || name != "abc" {
		t.Fatalf("project=%q %q", path, name)
	}
	if path, name := projectFromPath("relative.json"); path != "/gemini/unknown" || name != "unknown" {
		t.Fatal("project fallback")
	}
	if ops := shellOperations("rm -f old.go"); len(ops) != 1 || ops[0].kind != "delete" {
		t.Fatalf("shell=%#v", ops)
	}
	if ops := patchOperations("*** Delete File: old.go"); len(ops) != 1 || ops[0].kind != "delete" {
		t.Fatalf("patch=%#v", ops)
	}
	if text := geminiText(Message{Content: "x", Thoughts: []string{"t"}, ToolCalls: []ToolCall{{ID: "1", Name: "shell", Args: map[string]any{"command": "touch x"}}}}); !strings.Contains(text, "Thoughts") || !strings.Contains(text, "Tool calls") {
		t.Fatalf("text=%q", text)
	}
	if _, ok := parseTimestamp("bad"); ok {
		t.Fatal("timestamp")
	}
	if !timestamp(time.Time{}, false, 2).Equal(time.Unix(0, 2000).UTC()) {
		t.Fatal("fallback")
	}
	if _, _, err := Chunks(strings.NewReader("[]"), Source{}); err == nil {
		t.Fatal("provenance")
	}
	if len(splitContent(strings.Repeat("x", MaxChunkContentLength+20))) < 2 {
		t.Fatal("split")
	}
	if _, _, err := Parse(failingReader{}, 10); err == nil || !strings.Contains(err.Error(), "read Gemini source") {
		t.Fatalf("read=%v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }
