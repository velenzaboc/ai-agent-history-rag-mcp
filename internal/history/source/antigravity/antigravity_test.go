package antigravity

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChunksGolden(t *testing.T) {
	p, e := os.ReadFile(filepath.Join("testdata", "transcript_full.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	events, issues, e := Parse(bytes.NewReader(p), 0)
	if e != nil || len(events) != 2 || len(issues) != 1 || issues[0].Line != 2 {
		t.Fatalf("%#v %#v %v", events, issues, e)
	}
	if events[0].ID != "event-1" || events[0].ToolCalls[0].ID != "call-1" {
		t.Fatal(events[0])
	}
	chunks, issues, e := Chunks(bytes.NewReader(p), Source{FilePath: "/x/brain/conversation-1/.system_generated/logs/transcript_full.jsonl", MachineID: "mac"})
	if e != nil || len(issues) != 1 || len(chunks) != 5 {
		t.Fatalf("%#v %#v %v", chunks, issues, e)
	}
	if chunks[0].SessionID != "conversation-1" || chunks[0].ProjectPath != "/antigravity/conversation-1" || !strings.Contains(chunks[0].Content, "Thinking:\nplan") || len(chunks[0].ChildChunkIDs) != 2 {
		t.Fatal(chunks[0])
	}
	if chunks[1].FilePath != "main.go" || chunks[2].FilePath != "main_test.go" || chunks[4].FilePath != "old.go" || chunks[4].Operation != "delete" {
		t.Fatalf("%#v %#v %#v", chunks[1], chunks[2], chunks[4])
	}
	again, _, e := Chunks(bytes.NewReader(p), Source{FilePath: "/x/brain/conversation-1/.system_generated/logs/transcript_full.jsonl", MachineID: "mac"})
	if e != nil || again[0].ID != chunks[0].ID {
		t.Fatal(e)
	}
}
func TestBoundsBinaryAndHelpers(t *testing.T) {
	if _, _, e := Parse(nil, 1); e == nil {
		t.Fatal("nil")
	}
	if _, _, e := Parse(strings.NewReader("[]"), 1); e == nil {
		t.Fatal("size")
	}
	if _, _, e := Parse(strings.NewReader("[]"), MaxFileBytes+1); e == nil {
		t.Fatal("limit")
	}
	deep := strings.Repeat("{\"x\":", MaxJSONDepth+2) + "0" + strings.Repeat("}", MaxJSONDepth+2)
	if _, issues, e := Parse(strings.NewReader(deep), int64(len(deep)+1)); e != nil || len(issues) != 1 || issues[0].Reason != "json_too_deep" {
		t.Fatalf("depth: %#v %v", issues, e)
	}
	chunks, _, e := Chunks(strings.NewReader("\x00hello\x01world"), Source{FilePath: "/old.pb", Binary: true})
	if e != nil || len(chunks) != 1 || chunks[0].ChunkType != "antigravity_history" {
		t.Fatal(chunks, e)
	}
	if sessionID("/a/brain/x/log") != "x" || sessionID("/a/f.pb") != "f" {
		t.Fatal("session")
	}
	if len(patch("*** Delete File: x")) != 1 || len(shell("touch x")) != 1 {
		t.Fatal("ops")
	}
	if !time.Unix(0, 1000).UTC().Equal(time.Unix(0, 1000).UTC()) {
		t.Fatal("time")
	}
	if _, _, e := Parse(failing{}, 10); e == nil {
		t.Fatal("read")
	}
}

type failing struct{}

func (failing) Read([]byte) (int, error) { return 0, errors.New("x") }
