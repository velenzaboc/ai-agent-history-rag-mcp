package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/durable"
)

func coverageJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func coverageJSONLines(t *testing.T, values ...any) []byte {
	t.Helper()
	var payload []byte
	for _, value := range values {
		payload = append(payload, coverageJSON(t, value)...)
		payload = append(payload, '\n')
	}
	return payload
}

func coverageGemini(t *testing.T, session string, userTime, assistantTime *time.Time) []byte {
	t.Helper()
	message := func(id, kind, content string, timestamp *time.Time) map[string]any {
		value := map[string]any{"id": id, "type": kind, "content": content}
		if timestamp != nil {
			value["timestamp"] = timestamp.UTC().Format(time.RFC3339Nano)
		}
		return value
	}
	return coverageJSON(t, map[string]any{
		"sessionId": session,
		"messages": []any{
			message("user", "user", "gemini question", userTime),
			message("assistant", "gemini", "gemini answer", assistantTime),
		},
	})
}

func coverageAntigravity(t *testing.T, timestamp *time.Time) []byte {
	t.Helper()
	event := map[string]any{"id": "event", "source": "agent", "type": "message", "status": "done", "step_index": 1, "content": "antigravity answer"}
	if timestamp != nil {
		event["created_at"] = timestamp.UTC().Format(time.RFC3339Nano)
	}
	return coverageJSONLines(t, event)
}

func coverageChatGPT(t *testing.T, session string, userTime, assistantTime *time.Time) []byte {
	t.Helper()
	message := func(id, role, content string, timestamp *time.Time) map[string]any {
		value := map[string]any{"id": id, "role": role, "content": content}
		if timestamp != nil {
			value["created_at"] = timestamp.UTC().Format(time.RFC3339Nano)
		}
		return value
	}
	return coverageJSON(t, []any{map[string]any{
		"id":    session,
		"title": "Forward coverage",
		"messages": []any{
			message("user", "user", "chatgpt question", userTime),
			message("assistant", "assistant", "chatgpt answer", assistantTime),
		},
	}})
}

func coverageClaudeApp(t *testing.T, session string, userTime, assistantTime *time.Time) []byte {
	t.Helper()
	message := func(id, sender, content string, timestamp *time.Time) map[string]any {
		value := map[string]any{"uuid": id, "sender": sender, "content": content}
		if timestamp != nil {
			value["created_at"] = timestamp.UTC().Format(time.RFC3339Nano)
		}
		return value
	}
	return coverageJSON(t, []any{map[string]any{
		"uuid": session,
		"name": "Forward coverage",
		"chat_messages": []any{
			message("user", "human", "claude app question", userTime),
			message("assistant", "claude", "claude app answer", assistantTime),
		},
	}})
}

func TestForwardProducerExcludesRotatedNonUUIDBaselineIdentity(t *testing.T) {
	const sessionID = "legacy-session-alpha"
	sourceRoot := t.TempDir()
	originalPath := filepath.Join(sourceRoot, "legacy-session.jsonl")
	writeForwardFile(t, originalPath, []byte("baseline bytes must remain unread\n"))
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	registry := Registry{MachineID: "cube", MaxBytes: MaxSourceSnapshotBytes}

	previousSnapshot := snapshotForwardSource
	baselineReads := 0
	snapshotForwardSource = func(*PinnedRoot, string, int64) (*Snapshot, error) {
		baselineReads++
		return nil, errors.New("non-UUID baseline content was opened")
	}
	t.Cleanup(func() { snapshotForwardSource = previousSnapshot })
	producer, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatalf("NewForwardProducer non-UUID baseline: %v", err)
	}
	if baselineReads != 0 {
		t.Fatalf("constructor opened non-UUID baseline %d times", baselineReads)
	}
	snapshotForwardSource = previousSnapshot

	before, err := os.Stat(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	rotatedPath := filepath.Join(sourceRoot, "archive", "current.jsonl")
	if err := os.MkdirAll(filepath.Dir(rotatedPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(originalPath, rotatedPath); err != nil {
		t.Fatal(err)
	}
	writeForwardFile(t, rotatedPath, forwardTurn(sessionID, "new-looking", time.Now().Add(24*time.Hour)))
	after, err := os.Stat(rotatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("rotation fixture replaced the baseline file object")
	}
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan rotated non-UUID baseline: %v", err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("rotated non-UUID baseline uploaded as new content: %d batches", len(sink.chunks))
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatalf("restart after non-UUID rotation: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Scan(context.Background()); err != nil {
		t.Fatalf("restart scan after non-UUID rotation: %v", err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("restart forgot rotated non-UUID baseline: %d batches", len(sink.chunks))
	}
}

func TestForwardProducerCapturesFreshOtherSourceGrammars(t *testing.T) {
	future := time.Now().UTC().Add(24 * time.Hour)
	cases := []struct {
		name, relativePath, session string
		payload                     []byte
	}{
		{"gemini", filepath.Join("gemini", "logs.json"), "gemini-fresh", coverageGemini(t, "gemini-fresh", &future, &future)},
		{"antigravity", filepath.Join("antigravity", "transcript_full.jsonl"), "transcript_full", coverageAntigravity(t, &future)},
		{"chatgpt", filepath.Join("chatgpt", "conversations.json"), "chatgpt-fresh", coverageChatGPT(t, "chatgpt-fresh", &future, &future)},
		{"claude app", filepath.Join("claude-app", "conversations.json"), "claude-app-fresh", coverageClaudeApp(t, "claude-app-fresh", &future, &future)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceRoot := t.TempDir()
			state, _ := openForwardState(t)
			sink := &recordingSink{}
			producer, err := NewForwardProducer(Registry{MachineID: "grammar-host", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, time.Second, state)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = producer.Close() })
			writeForwardFile(t, filepath.Join(sourceRoot, tc.relativePath), tc.payload)
			if err := producer.Scan(context.Background()); err != nil {
				t.Fatalf("Scan fresh %s source: %v", tc.name, err)
			}
			if len(sink.chunks) != 1 || len(sink.chunks[0]) == 0 {
				t.Fatalf("fresh %s batches = %#v", tc.name, sink.chunks)
			}
			for _, chunk := range sink.chunks[0] {
				if chunk.MachineID != "grammar-host" || chunk.SessionID != tc.session || chunk.Timestamp.Before(future) {
					t.Fatalf("fresh %s provenance = session:%q machine:%q time:%v", tc.name, chunk.SessionID, chunk.MachineID, chunk.Timestamp)
				}
			}
		})
	}
}

func TestForwardProducerRefusesOldOrUnknownOtherSourceTimes(t *testing.T) {
	old := time.Now().UTC().Add(-24 * time.Hour)
	future := time.Now().UTC().Add(24 * time.Hour)
	cases := []struct {
		name, relativePath string
		payload            []byte
	}{
		{"gemini missing time", filepath.Join("gemini", "logs.json"), coverageGemini(t, "gemini-unknown", nil, &future)},
		{"antigravity old time", filepath.Join("antigravity", "transcript_full.jsonl"), coverageAntigravity(t, &old)},
		{"antigravity binary without time", filepath.Join("antigravity", "legacy.pb"), []byte("legacy binary history")},
		{"chatgpt partial old conversation", filepath.Join("chatgpt", "conversations.json"), coverageChatGPT(t, "chatgpt-old", &old, &future)},
		{"claude app missing time", filepath.Join("claude-app", "conversations.json"), coverageClaudeApp(t, "claude-app-unknown", &future, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceRoot := t.TempDir()
			state, _ := openForwardState(t)
			sink := &recordingSink{}
			producer, err := NewForwardProducer(Registry{MachineID: "grammar-host", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, time.Second, state)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = producer.Close() })
			path := filepath.Join(sourceRoot, tc.relativePath)
			writeForwardFile(t, path, tc.payload)
			if err := producer.Scan(context.Background()); err != nil {
				t.Fatalf("Scan refused %s source: %v", tc.name, err)
			}
			if len(sink.chunks) != 0 {
				t.Fatalf("%s source uploaded: %d batches", tc.name, len(sink.chunks))
			}
			persisted, err := loadForwardState(state)
			if err != nil {
				t.Fatal(err)
			}
			if !persisted.ExcludedPaths[filepath.Clean(path)] {
				t.Fatalf("%s source was not durably refused", tc.name)
			}
		})
	}
}

func TestForwardProducerWaitsForPartialSourceThenCapturesCompletedFile(t *testing.T) {
	sourceRoot := t.TempDir()
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	producer, err := NewForwardProducer(Registry{MachineID: "partial-host", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	path := filepath.Join(sourceRoot, "chatgpt", "conversations.json")
	writeForwardFile(t, path, []byte(`[{"id":"partial"`))
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan partial source: %v", err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("partial source uploaded: %d batches", len(sink.chunks))
	}
	persisted, err := loadForwardState(state)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExcludedPaths[filepath.Clean(path)] {
		t.Fatal("partial source was permanently refused instead of waiting")
	}
	future := time.Now().UTC().Add(24 * time.Hour)
	writeForwardFile(t, path, coverageChatGPT(t, "completed", &future, &future))
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan completed source: %v", err)
	}
	if len(sink.chunks) != 1 || len(sink.chunks[0]) == 0 {
		t.Fatalf("completed source batches = %#v", sink.chunks)
	}
}

func TestForwardProducerRefusesCorruptDurableStateShapes(t *testing.T) {
	type corruptCase struct {
		name, want string
		mutate     func(forwardState) []byte
	}
	cases := []corruptCase{
		{"duplicate key", "duplicate", func(state forwardState) []byte {
			payload, _ := json.Marshal(state)
			return bytes.Replace(payload, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
		}},
		{"unknown field", "unknown field", func(state forwardState) []byte {
			payload, _ := json.Marshal(state)
			return append(append([]byte(nil), payload[:len(payload)-1]...), []byte(`,"unexpected":true}`)...)
		}},
		{"trailing value", "trailing data", func(state forwardState) []byte {
			payload, _ := json.Marshal(state)
			return append(payload, []byte(`{}`)...)
		}},
		{"relative root", "roots are invalid", func(state forwardState) []byte {
			state.Roots[0].Path = "relative"
			payload, _ := json.Marshal(state)
			return payload
		}},
		{"false path exclusion", "exclusions are invalid", func(state forwardState) []byte {
			state.ExcludedPaths[filepath.Join(state.Roots[0].Path, "old.jsonl")] = false
			payload, _ := json.Marshal(state)
			return payload
		}},
		{"empty session exclusion", "session exclusions are invalid", func(state forwardState) []byte {
			state.ExcludedSessions[""] = true
			payload, _ := json.Marshal(state)
			return payload
		}},
		{"invalid progress digest", "progress is invalid", func(state forwardState) []byte {
			state.Progress[filepath.Join(state.Roots[0].Path, "new.jsonl")] = "not-a-digest"
			payload, _ := json.Marshal(state)
			return payload
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceRoot := t.TempDir()
			state, _ := openForwardState(t)
			producer, err := NewForwardProducer(Registry{}, &recordingSink{}, []string{sourceRoot}, time.Second, state)
			if err != nil {
				t.Fatalf("create valid durable state: %v", err)
			}
			if err := producer.Close(); err != nil {
				t.Fatal(err)
			}
			base, err := loadForwardState(state)
			if err != nil {
				t.Fatalf("load valid durable state: %v", err)
			}
			if err := state.WriteAtomic(forwardStateName, tc.mutate(base)); err != nil {
				t.Fatal(err)
			}
			sink := &recordingSink{}
			producer, err = NewForwardProducer(Registry{}, sink, []string{sourceRoot}, time.Second, state)
			if producer != nil {
				_ = producer.Close()
				t.Fatal("constructor returned a producer for corrupt durable state")
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("corrupt state error = %v, want %q", err, tc.want)
			}
			if len(sink.chunks) != 0 {
				t.Fatalf("corrupt state uploaded %d batches", len(sink.chunks))
			}
		})
	}

	t.Run("unavailable durable root", func(t *testing.T) {
		sourceRoot := t.TempDir()
		state, _ := openForwardState(t)
		if err := state.Close(); err != nil {
			t.Fatal(err)
		}
		if producer, err := NewForwardProducer(Registry{}, &recordingSink{}, []string{sourceRoot}, time.Second, state); err == nil {
			_ = producer.Close()
			t.Fatal("constructor accepted unavailable durable state")
		}
	})
}

func TestForwardProducerRetriesRefusalAfterStateWriteFailure(t *testing.T) {
	sourceRoot := t.TempDir()
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	producer, err := NewForwardProducer(Registry{MachineID: "refusal-host", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	path := filepath.Join(sourceRoot, "historical.jsonl")
	writeForwardFile(t, path, forwardTurn("historical-session", "old", time.Now().Add(-24*time.Hour)))
	wantErr := errors.New("refusal state unavailable")
	previousPersist := persistForwardState
	persistForwardState = func(*durable.Root, string, []byte) error { return wantErr }
	t.Cleanup(func() { persistForwardState = previousPersist })
	if err := producer.Scan(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("refusal persistence error = %v, want %v", err, wantErr)
	}
	persistForwardState = previousPersist
	if len(sink.chunks) != 0 {
		t.Fatalf("historical source uploaded during failed refusal: %d batches", len(sink.chunks))
	}
	persisted, err := loadForwardState(state)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExcludedPaths[filepath.Clean(path)] {
		t.Fatal("failed refusal persistence was acknowledged")
	}
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("retry refusal persistence: %v", err)
	}
	persisted, err = loadForwardState(state)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.ExcludedPaths[filepath.Clean(path)] {
		t.Fatal("historical source was not durably refused on retry")
	}
}

func TestCurrentForwardRootsRefusesUnboundRoot(t *testing.T) {
	root, err := NewPinnedRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := currentForwardRoots([]*PinnedRoot{root}); !errors.Is(err, ErrRootUnbound) {
		t.Fatalf("currentForwardRoots unbound error = %v", err)
	}
	_ = root.Close()
}

func TestInspectForwardEligibilityUnknownKindWaits(t *testing.T) {
	if eligibility, sessions := inspectForwardEligibility(SourceKind("unknown"), nil, time.Now()); eligibility != forwardWait || sessions != nil {
		t.Fatalf("unknown eligibility = %v, %#v", eligibility, sessions)
	}
}

func TestReliableForwardSessionRejectsSyntheticIdentifiers(t *testing.T) {
	for _, session := range []string{"", "unknown", "gemini-session", "gemini-logs", "123456"} {
		if reliableForwardSession(session) {
			t.Fatalf("synthetic session %q accepted", session)
		}
	}
	for _, session := range []string{"team-123", "conversation-alpha"} {
		if !reliableForwardSession(session) {
			t.Fatalf("stable session %q refused", session)
		}
	}
}

func TestRejectDuplicateForwardKeysRejectsMalformedContainers(t *testing.T) {
	for _, payload := range [][]byte{[]byte(`{"a":[1,2}`), []byte(`{"a":1`), []byte(`[1,2}`)} {
		if err := rejectDuplicateForwardKeys(payload); err == nil {
			t.Fatalf("malformed forward JSON accepted: %s", payload)
		}
	}
}
