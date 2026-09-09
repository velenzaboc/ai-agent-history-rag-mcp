package codex

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChunksGoldenSession(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("testdata", "session-123e4567-e89b-12d3-a456-426614174000.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	chunks, issues, err := Chunks(bytes.NewReader(payload), Source{FilePath: "/snapshots/session-123e4567-e89b-12d3-a456-426614174000.jsonl", MachineID: "mac"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Line != 4 || issues[0].Reason != "invalid_json" {
		t.Fatalf("issues = %#v", issues)
	}
	if len(chunks) != 4 {
		t.Fatalf("chunk count = %d: %#v", len(chunks), chunks)
	}
	if chunks[0].Content != "In project codex-project, user said:\nA standalone event" {
		t.Fatalf("event = %#v", chunks[0])
	}
	turn := chunks[1]
	if turn.ChunkType != "turn" || turn.SessionID != "thread-42" || turn.ProjectPath != "/work/codex-project" || turn.ProjectName != "codex-project" || turn.Model != "gpt-5" || turn.SourceLine != 3 || turn.MachineID != "mac" {
		t.Fatalf("turn metadata = %#v", turn)
	}
	want := "In project codex-project, user asked:\nFix the parser\n\nAssistant responded:\n[Reasoning summary] Inspect input Plan change\n[Tool call] apply_patch {\"patch\":\"*** Update File: parser.go\\n*** Add File: test.go\"}\nParser updated."
	if turn.Content != want {
		t.Fatalf("turn content = %q", turn.Content)
	}
	if len(turn.ChildChunkIDs) != 2 || chunks[2].ParentChunkID != turn.ID || chunks[3].ParentChunkID != turn.ID {
		t.Fatalf("parent/children = %#v %#v %#v", turn, chunks[2], chunks[3])
	}
	if chunks[2].FilePath != "parser.go" || chunks[2].Operation != "edit" || chunks[3].FilePath != "test.go" || chunks[3].Operation != "write" {
		t.Fatalf("file chunks = %#v %#v", chunks[2], chunks[3])
	}
	if chunks[0].ID != "" && chunks[0].ID != chunks[0].ID {
		t.Fatal("impossible")
	}
	second, _, err := Chunks(bytes.NewReader(payload), Source{FilePath: "/snapshots/session-123e4567-e89b-12d3-a456-426614174000.jsonl", MachineID: "mac"})
	if err != nil || second[1].ID != chunks[1].ID {
		t.Fatalf("non-deterministic IDs: %v %#v", err, second)
	}
}

func TestParseIsolationAndLimits(t *testing.T) {
	deep := strings.Repeat("{\"x\":", MaxJSONDepth+2) + "0" + strings.Repeat("}", MaxJSONDepth+2)
	payload := "[]\n{\"type\":\"ok\"}\n" + deep + "\n" + strings.Repeat("x", MaxLineBytes+1) + "\n"
	events, issues, err := Parse(strings.NewReader(payload), int64(len(payload)+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "ok" {
		t.Fatalf("events = %#v", events)
	}
	if got := []string{issues[0].Reason, issues[1].Reason, issues[2].Reason}; strings.Join(got, ",") != "entry_not_object,json_too_deep,line_too_long" {
		t.Fatalf("issues = %#v", issues)
	}
	if _, _, err := Parse(strings.NewReader("{}"), 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Parse(strings.NewReader("{}"), MaxFileBytes+1); err == nil {
		t.Fatal("expected invalid limit")
	}
	if _, _, err := Parse(nil, 1); err == nil {
		t.Fatal("expected nil reader error")
	}
	if _, _, err := Parse(strings.NewReader("{\"type\":\"ok\"}"), 1); err == nil {
		t.Fatal("expected source limit error")
	}
}

func TestHelpersAndUnpairedEvents(t *testing.T) {
	if got := sessionIDFromFilename("prefix-123E4567-E89B-12D3-A456-426614174000.jsonl"); got != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("session = %q", got)
	}
	if sessionIDFromFilename("short.jsonl") != "" {
		t.Fatal("unexpected session")
	}
	if projectPath("", "") != "/unknown" || projectName("/") != "unknown" {
		t.Fatal("project fallback")
	}
	if got, ops := summarizeTool(map[string]any{"type": "web_search_call", "action": map[string]any{"query": "docs"}}); got != "[Web search] docs" || len(ops) != 0 {
		t.Fatalf("web = %q %#v", got, ops)
	}
	if got, _ := summarizeTool(map[string]any{"type": "reasoning"}); got != "[Reasoning summary]" {
		t.Fatalf("reasoning = %q", got)
	}
	if got, _ := summarizeTool(map[string]any{"type": "ghost_snapshot"}); got != "[Ghost snapshot captured]" {
		t.Fatalf("ghost = %q", got)
	}
	if ops := shellOperations("shell", `{"command":"rm -f old.txt"}`); len(ops) != 1 || ops[0].kind != "delete" {
		t.Fatalf("shell ops = %#v", ops)
	}
	if ops := patchOperations("invalid"); ops != nil {
		t.Fatalf("invalid patch = %#v", ops)
	}
	input := "{\"type\":\"response_item\",\"payload\":{\"type\":\"function_call\",\"name\":\"shell\",\"arguments\":{\"command\":\"touch created.txt\"}}}\n{\"type\":\"other\",\"payload\":{\"x\":1}}\n"
	chunks, _, err := Chunks(strings.NewReader(input), Source{FilePath: "source.jsonl"})
	if err != nil || len(chunks) != 3 {
		t.Fatalf("unpaired chunks: %v %#v", err, chunks)
	}
	if chunks[1].ChunkType != "file_change" || chunks[1].FilePath != "created.txt" || !strings.Contains(chunks[2].Content, "\"type\":\"other\"") {
		t.Fatalf("unpaired = %#v", chunks)
	}
	long := strings.Repeat("z", MaxChunkContentLength+20)
	parts := splitContent(long)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("parts = %d", len(parts))
	}
	if !eventTimestamp(Event{Line: 2}).Equal(time.Unix(0, 2000).UTC()) {
		t.Fatal("fallback timestamp")
	}
}

func TestReadFailures(t *testing.T) {
	if _, _, err := Parse(failingReader{}, 10); err == nil || !strings.Contains(err.Error(), "read Codex source") {
		t.Fatalf("read error = %v", err)
	}
	if _, _, err := Chunks(strings.NewReader("{}"), Source{}); err == nil {
		t.Fatal("missing provenance")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

var _ io.Reader = failingReader{}
