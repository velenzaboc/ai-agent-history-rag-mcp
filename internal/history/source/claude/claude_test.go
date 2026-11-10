package claude

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
)

func TestChunksGoldenClaudeJSONL(t *testing.T) {
	payload, err := os.ReadFile("testdata/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	chunks, issues, err := Chunks(bytes.NewReader(payload), Source{ProjectEncoded: "-work-parser", FilePath: "/history/-work-parser/session.jsonl", MachineID: "test-machine"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0] != (Issue{Line: 2, Reason: "invalid_json"}) {
		t.Fatalf("issues = %#v", issues)
	}
	if len(chunks) != 4 {
		t.Fatalf("chunks = %#v", chunks)
	}
	for index, want := range []struct {
		kind, content string
		line          int64
	}{
		{"turn", "In project parser, user asked:\nPlease update the parser.\n\nAssistant responded:\nDone.\n[Used Write on /work/parser.go]", 1},
		{"file_change", "In project parser, file /work/parser.go was write. Created/overwrote file. This was in response to: Please update the parser.", 3},
		{"summary", "Session summary for parser:\nParser updated.", 4},
		{"file_change", "In project parser, file /work/parser.go was edit. Replaced 'old' with 'new'. This was in response to: [unpaired assistant message]", 5},
	} {
		if chunks[index].ChunkType != want.kind || chunks[index].Content != want.content || chunks[index].SourceLine != want.line {
			t.Fatalf("chunk[%d] = %#v", index, chunks[index])
		}
		if chunks[index].ID == "" || chunks[index].ProjectPath != "/work/parser" || chunks[index].ProjectName != "parser" || chunks[index].SessionID != "session-1" || chunks[index].SourceFile != "/history/-work-parser/session.jsonl" || chunks[index].MachineID != "test-machine" {
			t.Fatalf("chunk[%d] lost source identity: %#v", index, chunks[index])
		}
	}
	if got := chunks[0].ChildChunkIDs; len(got) != 1 || got[0] != chunks[1].ID || chunks[1].ParentChunkID != chunks[0].ID || chunks[3].ParentChunkID != "" {
		t.Fatalf("parent links = turn:%#v file:%#v unpaired:%#v", chunks[0], chunks[1], chunks[3])
	}
	if !chunks[0].Timestamp.Equal(time.Date(2026, time.September, 9, 12, 0, 2, 0, time.UTC)) || chunks[0].UserUUID != "user-1" || chunks[0].AssistantUUID != "assistant-1" || chunks[0].Model != "claude-test" {
		t.Fatalf("turn metadata = %#v", chunks[0])
	}
	first, _, err := Chunks(bytes.NewReader(payload), Source{ProjectEncoded: "-work-parser", FilePath: "/history/-work-parser/session.jsonl", MachineID: "test-machine"})
	if err != nil || strings.Join(chunkIDs(chunks), ",") != strings.Join(chunkIDs(first), ",") {
		t.Fatalf("chunk IDs are not deterministic: %v", err)
	}
}

func TestParseIsolatesMalformedRecordsAndEnforcesBounds(t *testing.T) {
	deep := strings.Repeat("[", MaxJSONDepth+2) + strings.Repeat("]", MaxJSONDepth+2)
	payload := "\n[]\n{\"type\":\"user\",\"message\":\"wrong\"}\n" + deep + "\n{\"type\":\"system\"}\n" + strings.Repeat("x", MaxLineBytes+1) + "\n"
	entries, issues, err := Parse(strings.NewReader(payload), int64(len(payload)+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Line != 3 || entries[0].Entry.Message != nil || entries[1].Line != 5 || entries[1].Entry.Type != "system" {
		t.Fatalf("entries = %#v", entries)
	}
	want := []Issue{{Line: 2, Reason: "entry_not_object"}, {Line: 4, Reason: "json_too_deep"}, {Line: 6, Reason: "line_too_long"}}
	if len(issues) != len(want) {
		t.Fatalf("issues = %#v, want %#v", issues, want)
	}
	for index := range want {
		if issues[index] != want[index] {
			t.Fatalf("issues[%d] = %#v, want %#v", index, issues[index], want[index])
		}
	}
	if _, _, err := Parse(strings.NewReader("abcdef"), 5); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Parse accepted oversized source: %v", err)
	}
	if _, _, err := Parse(nil, 1); err == nil {
		t.Fatal("Parse accepted nil input")
	}
	if _, _, err := Parse(strings.NewReader(""), MaxFileBytes+1); err == nil {
		t.Fatal("Parse accepted invalid byte bound")
	}
}

func TestContentToolOperationsAndSplittingAreStable(t *testing.T) {
	message := &Message{Content: []any{
		map[string]any{"type": "text", "text": "answer"},
		map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "one\ntwo"}},
		map[string]any{"type": "tool_result", "content": strings.Repeat("x", 500)},
	}}
	if got, want := textContent(message), "answer\n[Ran command: one two]\n[Tool result truncated]"; got != want {
		t.Fatalf("textContent() = %q, want %q", got, want)
	}
	parts := splitContent(strings.Repeat("a", MaxChunkContentLength+100))
	if len(parts) != 2 || len(parts[0]) != MaxChunkContentLength || len(parts[1]) != ChunkOverlap+100 {
		t.Fatalf("splitContent() = lengths %d/%d", len(parts[0]), len(parts[1]))
	}
	if got := DecodeProjectPath("-work-parser"); got != "/work/parser" {
		t.Fatalf("DecodeProjectPath() = %q", got)
	}
	if got := DecodeProjectPath("-work-..-parser"); got != "/invalid/path" {
		t.Fatalf("DecodeProjectPath traversal = %q", got)
	}
	if got := entryTimestamp(Entry{}, Entry{}, 7); !got.Equal(time.Unix(0, 7000).UTC()) {
		t.Fatalf("entryTimestamp() = %v", got)
	}
	if got := canonicalTimestamp(time.Date(2026, time.September, 9, 12, 0, 2, 120000000, time.UTC)); got != "2026-09-09 12:00:02.12+00:00" {
		t.Fatalf("canonicalTimestamp() = %q", got)
	}
	if _, ok := summaryChunk(Entry{}, 1, "/work/parser", Source{FilePath: "source"}); ok {
		t.Fatal("summaryChunk accepted empty content")
	}
	if got := fileOperations(&Message{Content: []any{map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": "x"}}}}); len(got) != 0 {
		t.Fatalf("fileOperations accepted incomplete edit: %#v", got)
	}
	if _, _, err := Chunks(strings.NewReader(""), Source{}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("Chunks source identity error = %v", err)
	}
	utf8Parts := splitContent(strings.Repeat("世", 2300) + "a" + strings.Repeat("世", 1000))
	if len(utf8Parts) < 2 {
		t.Fatal("expected utf8 split")
	}
	for index, part := range utf8Parts {
		if !utf8.ValidString(part) || len(part) > MaxChunkContentLength {
			t.Fatalf("utf8Parts[%d] invalid or too long: %d", index, len(part))
		}
	}
	enhancedInput := strings.Join([]string{
		`{"type":"custom-title","customTitle":"Fix Claude Parser"}`,
		`{"type":"agent-name","agentName":"VOLT-ENGINEER"}`,
		`{"type":"user","uuid":"u1","message":{"role":"user","content":"Check PR"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-09-09T15:00:00Z","message":{"role":"assistant","model":"claude-4","content":[{"type":"text","text":"Linked."}]}}`,
		`{"type":"pr-link","prNumber":42,"prUrl":"https://github.com/velenzaboc/repo/pull/42","prRepository":"velenzaboc/repo"}`,
	}, "\n") + "\n"
	subagentSource := Source{
		FilePath:  "/Users/brandon/.claude/projects/-Volumes-1TB_USB-Meridian/98894181-8dac-44cf-b3b5-c9158018b1ce/subagents/agent-1.jsonl",
		MachineID: "macmini",
	}
	enhancedChunks, enhancedIssues, err := Chunks(strings.NewReader(enhancedInput), subagentSource)
	if err != nil || len(enhancedIssues) != 0 || len(enhancedChunks) != 3 {
		t.Fatalf("enhancedChunks=%#v issues=%#v err=%v", enhancedChunks, enhancedIssues, err)
	}
	for index, chunk := range enhancedChunks {
		if chunk.ProjectPath != "/Volumes/1TB_USB/Meridian" || chunk.ProjectName != "Meridian" || chunk.SessionID != "98894181-8dac-44cf-b3b5-c9158018b1ce" {
			t.Fatalf("enhancedChunks[%d] metadata=%#v", index, chunk)
		}
		if !chunk.Timestamp.Equal(time.Date(2026, time.September, 9, 15, 0, 0, 0, time.UTC)) {
			t.Fatalf("enhancedChunks[%d] timestamp=%v", index, chunk.Timestamp)
		}
	}
	if enhancedChunks[0].ChunkType != "summary" || !strings.Contains(enhancedChunks[0].Content, "Fix Claude Parser / VOLT-ENGINEER") {
		t.Fatalf("summary chunk=%#v", enhancedChunks[0])
	}
	if !strings.Contains(enhancedChunks[2].Content, "#42") || !strings.Contains(enhancedChunks[2].Content, "velenzaboc/repo") || !strings.Contains(enhancedChunks[2].Content, "https://github.com/velenzaboc/repo/pull/42") {
		t.Fatalf("pr-link chunk=%#v", enhancedChunks[2])
	}
}

func chunkIDs(chunks []store.Chunk) []string {
	ids := make([]string, len(chunks))
	for index := range chunks {
		ids[index] = chunks[index].ID
	}
	return ids
}
