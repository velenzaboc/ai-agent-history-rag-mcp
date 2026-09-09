package cursor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func cp(source, path string, g, o int64) Checkpoint {
	return Checkpoint{Source: source, Path: path, Digest: Digest([]byte(source)), Generation: g, Offset: o, Updated: time.Unix(1, 0)}
}
func TestPersistentDeterministicState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "cursor.json")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Put(cp("codex", "/tmp/codex", 2, 4)); e != nil {
		t.Fatal(e)
	}
	if e = s.Put(cp("claude", "/tmp/claude", 1, 2)); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(path)
	if e != nil || string(b) != "{\"version\":1,\"entries\":[{\"Source\":\"claude\",\"Path\":\"/tmp/claude\",\"Digest\":\""+Digest([]byte("claude"))+"\",\"Generation\":1,\"Offset\":2,\"Updated\":\"1970-01-01T00:00:01Z\"},{\"Source\":\"codex\",\"Path\":\"/tmp/codex\",\"Digest\":\""+Digest([]byte("codex"))+"\",\"Generation\":2,\"Offset\":4,\"Updated\":\"1970-01-01T00:00:01Z\"}]}" {
		t.Fatalf("%s %v", b, e)
	}
	again, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if got, ok := again.Get("codex"); !ok || got.Offset != 4 {
		t.Fatal(got, ok)
	}
	if e = again.Put(cp("codex", "/tmp/codex", 1, 5)); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}
func TestRefusesInvalidAndCorrupt(t *testing.T) {
	if _, e := Open("relative"); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "state")
	if e := os.WriteFile(path, []byte("not json"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(path); !errors.Is(e, ErrCorrupt) {
		t.Fatal(e)
	}
	s, e := Open(filepath.Join(t.TempDir(), "a"))
	if e != nil {
		t.Fatal(e)
	}
	bad := cp("x", "/tmp/x", 0, 0)
	bad.Digest = "bad"
	if e = s.Put(bad); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	bad = cp("x", "relative", 0, 0)
	if e = s.Put(bad); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if s.Path() == "" || s.String() == "" {
		t.Fatal("state identity")
	}
	bad = cp("bad/name", "/tmp/x", 0, 0)
	if e = s.Put(bad); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	bad = cp("x", "/tmp/x", -1, 0)
	if e = s.Put(bad); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	duplicate := filepath.Join(t.TempDir(), "duplicate")
	if e = os.WriteFile(duplicate, []byte(`{"version":1,"entries":[{"Source":"x","Path":"/tmp/x","Digest":"`+Digest([]byte("x"))+`","Generation":0,"Offset":0,"Updated":"1970-01-01T00:00:01Z"},{"Source":"x","Path":"/tmp/x","Digest":"`+Digest([]byte("x"))+`","Generation":0,"Offset":0,"Updated":"1970-01-01T00:00:01Z"}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(duplicate); !errors.Is(e, ErrCorrupt) {
		t.Fatal(e)
	}
}
