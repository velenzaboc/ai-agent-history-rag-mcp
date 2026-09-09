package claudeapp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
}
