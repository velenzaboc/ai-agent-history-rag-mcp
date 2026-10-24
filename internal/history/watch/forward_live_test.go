package watch

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type forwardLiveCase struct {
	name         string
	relativePath string
	session      string
	partial      []byte
	complete     func(*testing.T, time.Time) []byte
}

func TestForwardProducerWaitsForLivePartialWritesAcrossAllGrammars(t *testing.T) {
	cases := []forwardLiveCase{
		{
			name:         "claude",
			relativePath: filepath.Join("claude", "conversation.jsonl"),
			session:      "claude-live",
			partial:      []byte(`{"type":"user"`),
			complete: func(_ *testing.T, at time.Time) []byte {
				return forwardTurn("claude-live", "completed", at)
			},
		},
		{
			name:         "codex",
			relativePath: filepath.Join("codex", "sessions", "conversation.jsonl"),
			session:      "codex-live",
			partial:      []byte(`{"type":"session_meta"`),
			complete: func(_ *testing.T, at time.Time) []byte {
				return codexForwardSession("codex-live", "completed", at, at.Add(time.Second), at.Add(2*time.Second), true)
			},
		},
		{
			name:         "gemini",
			relativePath: filepath.Join("gemini", "logs.json"),
			session:      "gemini-live",
			partial:      []byte(`{"sessionId":"gemini-live","messages":[`),
			complete: func(t *testing.T, at time.Time) []byte {
				return coverageGemini(t, "gemini-live", &at, &at)
			},
		},
		{
			name:         "antigravity",
			relativePath: filepath.Join("antigravity", "transcript_full.jsonl"),
			session:      "transcript_full",
			partial:      []byte(`{"id":"partial"`),
			complete: func(t *testing.T, at time.Time) []byte {
				return coverageAntigravity(t, &at)
			},
		},
		{
			name:         "chatgpt",
			relativePath: filepath.Join("chatgpt", "conversations.json"),
			session:      "chatgpt-live",
			partial:      []byte(`[{"id":"chatgpt-live"`),
			complete: func(t *testing.T, at time.Time) []byte {
				return coverageChatGPT(t, "chatgpt-live", &at, &at)
			},
		},
		{
			name:         "claude app",
			relativePath: filepath.Join("claude-app", "conversations.json"),
			session:      "claude-app-live",
			partial:      []byte(`[{"uuid":"claude-app-live"`),
			complete: func(t *testing.T, at time.Time) []byte {
				return coverageClaudeApp(t, "claude-app-live", &at, &at)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceRoot := t.TempDir()
			state, _ := openForwardState(t)
			sink := &recordingSink{}
			registry := Registry{MachineID: "live-partial-host", MaxBytes: MaxSourceSnapshotBytes}
			producer, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = producer.Close() })

			path := filepath.Join(sourceRoot, tc.relativePath)
			writeForwardFile(t, path, tc.partial)
			if err := producer.Scan(context.Background()); err != nil {
				t.Fatalf("Scan() incomplete %s source = %v", tc.name, err)
			}
			if len(sink.chunks) != 0 {
				t.Fatalf("incomplete %s source uploaded %d batches", tc.name, len(sink.chunks))
			}
			persisted, err := loadForwardState(state)
			if err != nil {
				t.Fatal(err)
			}
			cleanPath := filepath.Clean(path)
			if persisted.ExcludedPaths[cleanPath] {
				t.Fatalf("incomplete %s source was durably excluded", tc.name)
			}
			if _, acknowledged := persisted.Progress[cleanPath]; acknowledged {
				t.Fatalf("incomplete %s source was acknowledged as ingested", tc.name)
			}

			fresh := time.Now().UTC().Add(24 * time.Hour)
			writeForwardFile(t, path, tc.complete(t, fresh))
			if err := producer.Scan(context.Background()); err != nil {
				t.Fatalf("Scan() completed %s source = %v", tc.name, err)
			}
			if len(sink.chunks) != 1 || len(sink.chunks[0]) == 0 {
				t.Fatalf("completed %s source batches = %#v", tc.name, sink.chunks)
			}
			for _, chunk := range sink.chunks[0] {
				if chunk.SessionID != tc.session || chunk.MachineID != registry.MachineID || chunk.Timestamp.Before(fresh) {
					t.Fatalf("completed %s provenance = session:%q machine:%q time:%v", tc.name, chunk.SessionID, chunk.MachineID, chunk.Timestamp)
				}
			}
		})
	}
}

func invalidateForwardTimestamp(t *testing.T, payload []byte, timestamp time.Time) []byte {
	t.Helper()
	valid := []byte(timestamp.UTC().Format(time.RFC3339Nano))
	invalid := bytes.Replace(payload, valid, []byte("invalid-forward-timestamp"), 1)
	if bytes.Equal(invalid, payload) {
		t.Fatalf("fixture did not contain timestamp %q", valid)
	}
	return invalid
}

func TestForwardProducerPermanentlyRefusesInvalidSourceTimestamps(t *testing.T) {
	cases := []forwardLiveCase{
		{
			name:         "claude",
			relativePath: filepath.Join("claude", "invalid.jsonl"),
			complete: func(_ *testing.T, at time.Time) []byte {
				return forwardTurn("claude-invalid-time", "timestamp", at)
			},
		},
		{
			name:         "codex",
			relativePath: filepath.Join("codex", "sessions", "invalid.jsonl"),
			complete: func(_ *testing.T, at time.Time) []byte {
				return codexForwardSession("codex-invalid-time", "timestamp", at, at.Add(time.Second), at.Add(2*time.Second), true)
			},
		},
		{
			name:         "antigravity",
			relativePath: filepath.Join("antigravity", "transcript_full.jsonl"),
			complete: func(t *testing.T, at time.Time) []byte {
				return coverageAntigravity(t, &at)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceRoot := t.TempDir()
			state, _ := openForwardState(t)
			sink := &recordingSink{}
			registry := Registry{MachineID: "invalid-time-host", MaxBytes: MaxSourceSnapshotBytes}
			producer, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = producer.Close() })

			path := filepath.Join(sourceRoot, tc.relativePath)
			future := time.Now().UTC().Add(24 * time.Hour)
			valid := tc.complete(t, future)
			writeForwardFile(t, path, invalidateForwardTimestamp(t, valid, future))
			if err := producer.Scan(context.Background()); err != nil {
				t.Fatalf("Scan() invalid %s timestamp = %v", tc.name, err)
			}
			if len(sink.chunks) != 0 {
				t.Fatalf("invalid %s timestamp uploaded %d batches", tc.name, len(sink.chunks))
			}
			persisted, err := loadForwardState(state)
			if err != nil {
				t.Fatal(err)
			}
			cleanPath := filepath.Clean(path)
			if !persisted.ExcludedPaths[cleanPath] || len(persisted.ExcludedObjects) == 0 {
				t.Fatalf("invalid %s timestamp was not durably refused: %#v", tc.name, persisted)
			}

			replacement := path + ".replacement"
			writeForwardFile(t, replacement, tc.complete(t, future.Add(time.Hour)))
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
			if err := producer.Scan(context.Background()); err != nil {
				t.Fatalf("Scan() replacement after invalid %s timestamp = %v", tc.name, err)
			}
			if len(sink.chunks) != 0 {
				t.Fatalf("replacement after invalid %s timestamp uploaded %d batches", tc.name, len(sink.chunks))
			}
			if err := producer.Close(); err != nil {
				t.Fatal(err)
			}

			restarted, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
			if err != nil {
				t.Fatalf("restart after invalid %s timestamp = %v", tc.name, err)
			}
			t.Cleanup(func() { _ = restarted.Close() })
			if err := restarted.Scan(context.Background()); err != nil {
				t.Fatalf("restart Scan() after invalid %s timestamp = %v", tc.name, err)
			}
			if len(sink.chunks) != 0 {
				t.Fatalf("restart forgot invalid %s timestamp refusal: %d batches", tc.name, len(sink.chunks))
			}
		})
	}
}
