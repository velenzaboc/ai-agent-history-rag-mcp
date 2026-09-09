package uploadqueue

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func item(id string) Item {
	return Item{ID: id, Source: "source", Payload: "payload", Next: time.Unix(1, 0)}
}
func TestDurableIdempotentQueue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "q", "state")
	q, e := Open(p, 2)
	if e != nil {
		t.Fatal(e)
	}
	if e = q.Enqueue(item("b")); e != nil {
		t.Fatal(e)
	}
	if e = q.Enqueue(item("a")); e != nil {
		t.Fatal(e)
	}
	if e = q.Enqueue(item("a")); e != nil {
		t.Fatal(e)
	}
	if got := q.Ready(time.Now(), 1); len(got) != 1 || got[0].ID != "a" {
		t.Fatal(got)
	}
	if e = q.Enqueue(item("c")); !errors.Is(e, ErrFull) {
		t.Fatal(e)
	}
	if e = q.Retry("a", time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if got := q.Ready(time.Now(), 0); len(got) != 1 || got[0].ID != "b" {
		t.Fatal(got)
	}
	if e = q.Ack("b"); e != nil {
		t.Fatal(e)
	}
	q, e = Open(p, 2)
	if e != nil {
		t.Fatal(e)
	}
	if got := q.Ready(time.Now().Add(2*time.Hour), 0); len(got) != 1 || got[0].Attempts != 1 {
		t.Fatal(got)
	}
}
func TestRefusal(t *testing.T) {
	if _, e := Open("relative", 1); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	p := filepath.Join(t.TempDir(), "q")
	if e := os.WriteFile(p, []byte("bad"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(p, 1); !errors.Is(e, ErrCorrupt) {
		t.Fatal(e)
	}
	q, e := Open(filepath.Join(t.TempDir(), "x"), 1)
	if e != nil {
		t.Fatal(e)
	}
	bad := item("")
	if e = q.Enqueue(bad); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if e = q.Retry("none", time.Time{}); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if e = q.Ack("none"); e != nil {
		t.Fatal(e)
	}
}
