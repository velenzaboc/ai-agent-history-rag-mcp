package watch

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistrySixSources(t *testing.T) {
	r := Registry{}
	cases := map[string]SourceKind{"/x/claude/a.jsonl": SourceClaude, "/x/codex/a.jsonl": SourceCodex, "/x/conversations.json": SourceChatGPT, "/x/chats/a.json": SourceGemini, "/x/transcript_full.jsonl": SourceAntigravity, "/x/claude-app/conversations.json": SourceClaudeApp}
	for p, w := range cases {
		g, e := r.Detect(p)
		if e != nil || g != w {
			t.Fatalf("%s: %s %v", p, g, e)
		}
	}
	if _, e := r.Detect("/x/nope"); !errors.Is(e, ErrUnsupportedSource) {
		t.Fatal(e)
	}
	if _, e := r.Dispatch("nope", bytes.NewReader(nil), "/x"); !errors.Is(e, ErrUnsupportedSource) {
		t.Fatal(e)
	}
	for kind, path := range map[SourceKind]string{SourceClaude: "/x/a.jsonl", SourceCodex: "/x/codex/a.jsonl", SourceChatGPT: "/x/conversations.json", SourceGemini: "/x/logs.json", SourceAntigravity: "/x/transcript_full.jsonl", SourceClaudeApp: "/x/claude-app/conversations.json"} {
		if _, err := r.Dispatch(kind, bytes.NewReader([]byte("[]")), path); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
func TestLoopOrderDebounceCancellation(t *testing.T) {
	events := make(chan Event, 4)
	got := []string{}
	loop := Loop{Debounce: time.Millisecond, Capacity: 2, Dispatch: func(_ context.Context, p string) error { got = append(got, p); return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx, events) }()
	events <- Event{"b", time.Now()}
	events <- Event{"a", time.Now()}
	events <- Event{"a", time.Now()}
	time.Sleep(10 * time.Millisecond)
	close(events)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatal(got)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if !errors.Is(loop.Run(ctx, make(chan Event)), context.Canceled) {
		t.Fatal("cancel")
	}
}
