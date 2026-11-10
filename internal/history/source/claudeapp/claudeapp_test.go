package claudeapp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestGolden(t *testing.T) {
	b, e := os.ReadFile(filepath.Join("testdata", "conversations.json"))
	if e != nil {
		t.Fatal(e)
	}
	cs, is, e := Parse(bytes.NewReader(b), 0)
	if e != nil || len(cs) != 2 || len(is) != 1 || cs[0].Messages[0].Artifacts[0] != "{\"id\":\"a1\"}" {
		t.Fatalf("%#v %#v %v", cs, is, e)
	}
	chunks, is, e := Chunks(bytes.NewReader(b), Source{FilePath: "/x/conversations.json", MachineID: "mac"})
	if e != nil || len(is) != 1 || len(chunks) != 1 {
		t.Fatalf("%#v %#v %v", chunks, is, e)
	}
	if !strings.Contains(chunks[0].Content, "Fix\n this") || chunks[0].SessionID != "c1" || chunks[0].UserUUID != "u1" || chunks[0].AssistantUUID != "a1" || chunks[0].MachineID != "mac" {
		t.Fatal(chunks[0])
	}
	again, _, _ := Chunks(bytes.NewReader(b), Source{FilePath: "/x/conversations.json"})
	if again[0].ID != chunks[0].ID {
		t.Fatal("ID")
	}
}
func TestBounds(t *testing.T) {
	if _, _, e := Parse(nil, 1); e == nil {
		t.Fatal("nil")
	}
	if _, _, e := Parse(strings.NewReader("[]"), 1); e == nil {
		t.Fatal("bound")
	}
	if _, _, e := Parse(strings.NewReader("null"), 10); e == nil {
		t.Fatal("scalar")
	}
	deep := strings.Repeat("{\"x\":", MaxJSONDepth+2) + "0" + strings.Repeat("}", MaxJSONDepth+2)
	if _, _, e := Parse(strings.NewReader(deep), int64(len(deep)+1)); e == nil {
		t.Fatal("depth")
	}
	if _, _, e := Chunks(strings.NewReader("[]"), Source{}); e == nil {
		t.Fatal("source")
	}
	if _, ok := stamp("bad"); ok {
		t.Fatal("stamp")
	}
	if !time.Unix(0, 0).Equal(time.Unix(0, 0)) {
		t.Fatal("time")
	}
	exportWithNotes := `{"conversations":[{"uuid":"c-notes","name":"Notes Convo","postTurnSummary":"convo summary","lastTurnReport":"convo report","sideSessionNotes":"convo side notes","chat_messages":[{"uuid":"u1","sender":"human","text":"hello"},{"uuid":"a1","sender":"claude","text":"world","postTurnSummary":"turn summary","lastTurnReport":"turn report","sideSessionNotes":"turn notes"},{"uuid":"s1","sender":"system","postTurnSummary":"standalone note"}]}]}`
	chunks, issues, err := Chunks(strings.NewReader(exportWithNotes), Source{FilePath: "/x/conversations.json", MachineID: "mac"})
	if err != nil || len(issues) != 0 || len(chunks) != 3 {
		t.Fatalf("notes chunks=%#v issues=%#v err=%v", chunks, issues, err)
	}
	if chunks[0].ChunkType != "summary" || !strings.Contains(chunks[0].Content, "convo summary") || !strings.Contains(chunks[0].Content, "convo report") || !strings.Contains(chunks[0].Content, "convo side notes") {
		t.Fatalf("convo summary chunk=%#v", chunks[0])
	}
	if chunks[1].ChunkType != "turn" || !strings.Contains(chunks[1].Content, "turn summary") || !strings.Contains(chunks[1].Content, "turn report") || !strings.Contains(chunks[1].Content, "turn notes") {
		t.Fatalf("turn notes chunk=%#v", chunks[1])
	}
	if chunks[2].ChunkType != "summary" || !strings.Contains(chunks[2].Content, "standalone note") {
		t.Fatalf("standalone note chunk=%#v", chunks[2])
	}
	longUTF8 := strings.Repeat("世", 2300) + "a" + strings.Repeat("世", 1000)
	parts := splitContent(longUTF8)
	if len(parts) < 2 {
		t.Fatal("expected split parts")
	}
	for i, part := range parts {
		if !utf8.ValidString(part) || len(part) > MaxChunkContentLength {
			t.Fatalf("part[%d] invalid utf8 or length=%d", i, len(part))
		}
	}
	splitExport := `[{"uuid":"c-long","name":"Long","chat_messages":[{"uuid":"u1","sender":"human","created_at":"2026-09-09T12:00:00Z","text":"q"},{"uuid":"a1","sender":"claude","text":"` + longUTF8 + `"}]}]`
	longChunks, _, err := Chunks(strings.NewReader(splitExport), Source{FilePath: "/x/conversations.json"})
	if err != nil || len(longChunks) != 2 || longChunks[1].ParentChunkID == "" {
		t.Fatalf("longChunks=%#v err=%v", longChunks, err)
	}
}
