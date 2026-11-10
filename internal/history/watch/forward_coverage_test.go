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

func TestForwardSessionIDAndPBRefusalIsolation(t *testing.T) {
	const (
		brainUUID    = "11111111-2222-3333-4444-555555555555"
		subagentUUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	)
	if got := sessionIDFromPath("/root/brain/" + brainUUID + "/.system_generated/logs/transcript_full.jsonl"); got != brainUUID {
		t.Fatalf("brain sessionIDFromPath = %q, want %q", got, brainUUID)
	}
	if got := sessionIDFromPath("/root/" + subagentUUID + "/subagents/agent-123.jsonl"); got != subagentUUID {
		t.Fatalf("subagent sessionIDFromPath = %q, want %q", got, subagentUUID)
	}

	sourceRoot := t.TempDir()
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	producer, err := NewForwardProducer(Registry{MachineID: "ag-host", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })

	pbPath := filepath.Join(sourceRoot, "antigravity", brainUUID+".pb")
	writeForwardFile(t, pbPath, []byte("legacy protobuf"))
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, err := loadForwardState(state)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExcludedSessions[brainUUID] {
		t.Fatalf(".pb refusal poisoned ExcludedSessions[%q]", brainUUID)
	}

	future := time.Now().UTC().Add(24 * time.Hour)
	fullPath := filepath.Join(sourceRoot, "brain", brainUUID, ".system_generated", "logs", "transcript_full.jsonl")
	writeForwardFile(t, fullPath, coverageAntigravity(t, &future))
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.chunks) != 1 {
		t.Fatalf("sibling transcript_full.jsonl batches = %d, want 1", len(sink.chunks))
	}
}

func TestForwardEligibilityMetadataAndMixedExports(t *testing.T) {
	old := time.Now().UTC().Add(-24 * time.Hour)
	future := time.Now().UTC().Add(24 * time.Hour)
	later := future.Add(time.Hour)

	sourceRoot := t.TempDir()
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	producer, err := NewForwardProducer(Registry{MachineID: "meta-host", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })

	// 1. Claude with timestamp-less summary line + post-cutoff turn
	claudeMeta := append(
		[]byte(`{"type":"summary","summary":"compact summary without timestamp"}`+"\n"),
		forwardTurn("claude-meta-session", "hello", future)...,
	)
	writeForwardFile(t, filepath.Join(sourceRoot, "claude", "meta.jsonl"), claudeMeta)

	// 2. Codex with timestamp-less event_msg + post-cutoff turn, and refused timestamp-less response_item message
	codexMeta := append(
		[]byte(`{"type":"event_msg","payload":{"message":"metadata note"}}`+"\n"),
		codexForwardSession("codex-meta-session", "ok", future, future, future, true)...,
	)
	writeForwardFile(t, filepath.Join(sourceRoot, "codex", "meta.jsonl"), codexMeta)
	writeForwardFile(t, filepath.Join(sourceRoot, "codex", "refused.jsonl"), []byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"no time"}]}}`+"\n"))

	// 3. Gemini with timestamp-less info message + post-cutoff turn, and Gemini events array
	geminiMeta := coverageJSON(t, map[string]any{
		"sessionId": "gemini-meta-session",
		"messages": []any{
			map[string]any{"id": "info-1", "type": "info", "content": "session info without timestamp"},
			map[string]any{"id": "u-1", "type": "user", "content": "hi", "timestamp": future.Format(time.RFC3339Nano)},
			map[string]any{"id": "g-1", "type": "gemini", "content": "hello", "timestamp": future.Format(time.RFC3339Nano)},
		},
	})
	writeForwardFile(t, filepath.Join(sourceRoot, "chats", "session-meta.json"), geminiMeta)
	geminiEvents := coverageJSON(t, []any{
		map[string]any{"id": "ev-1", "sessionId": "gemini-events-session", "content": "event ok", "timestamp": future.Format(time.RFC3339Nano)},
	})
	writeForwardFile(t, filepath.Join(sourceRoot, "gemini-events", "logs.json"), geminiEvents)
	geminiRefusedEvents := coverageJSON(t, []any{
		map[string]any{"id": "ev-bad", "sessionId": "gemini-bad-events", "content": "no time"},
	})
	writeForwardFile(t, filepath.Join(sourceRoot, "gemini-bad", "logs.json"), geminiRefusedEvents)

	// 4. Antigravity with timestamp-less system line + post-cutoff agent message, and refused timestamp-less agent message
	agMeta := coverageJSONLines(t,
		map[string]any{"id": "sys", "source": "system", "type": "system", "status": "done", "content": "init"},
		map[string]any{"id": "msg", "source": "agent", "type": "message", "status": "done", "content": "answer", "created_at": future.Format(time.RFC3339Nano)},
	)
	writeForwardFile(t, filepath.Join(sourceRoot, "ag-ok", "transcript_full.jsonl"), agMeta)
	agRefused := coverageJSONLines(t,
		map[string]any{"id": "msg", "source": "agent", "type": "message", "status": "done", "content": "missing time"},
	)
	writeForwardFile(t, filepath.Join(sourceRoot, "ag-bad", "transcript_full.jsonl"), agRefused)

	// 5. Mixed ChatGPT & ClaudeApp exports (1 pre-cutoff conversation + 1 post-cutoff conversation)
	chatgptMixed := coverageJSON(t, []any{
		map[string]any{
			"id": "chatgpt-old-conv", "title": "Old",
			"messages": []any{
				map[string]any{"id": "m1", "role": "user", "content": "old q", "created_at": old.Format(time.RFC3339Nano)},
				map[string]any{"id": "m2", "role": "assistant", "content": "old a", "created_at": old.Format(time.RFC3339Nano)},
			},
		},
		map[string]any{
			"id": "chatgpt-new-conv", "title": "New",
			"messages": []any{
				map[string]any{"id": "m3", "role": "user", "content": "new q", "created_at": future.Format(time.RFC3339Nano)},
				map[string]any{"id": "m4", "role": "assistant", "content": "new a", "created_at": future.Format(time.RFC3339Nano)},
			},
		},
	})
	chatgptPath := filepath.Join(sourceRoot, "chatgpt", "conversations.json")
	writeForwardFile(t, chatgptPath, chatgptMixed)

	claudeAppMixed := coverageJSON(t, []any{
		map[string]any{
			"uuid": "claudeapp-old-conv", "name": "Old",
			"chat_messages": []any{
				map[string]any{"uuid": "m1", "sender": "human", "content": "old q", "created_at": old.Format(time.RFC3339Nano)},
				map[string]any{"uuid": "m2", "sender": "claude", "content": "old a", "created_at": old.Format(time.RFC3339Nano)},
			},
		},
		map[string]any{
			"uuid": "claudeapp-new-conv", "name": "New",
			"chat_messages": []any{
				map[string]any{"uuid": "m3", "sender": "human", "content": "new q", "created_at": future.Format(time.RFC3339Nano)},
				map[string]any{"uuid": "m4", "sender": "claude", "content": "new a", "created_at": future.Format(time.RFC3339Nano)},
			},
		},
	})
	claudeAppPath := filepath.Join(sourceRoot, "claude-app", "conversations.json")
	writeForwardFile(t, claudeAppPath, claudeAppMixed)

	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan metadata and mixed exports: %v", err)
	}
	persisted, err := loadForwardState(state)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.ExcludedSessions["chatgpt-old-conv"] || !persisted.ExcludedSessions["claudeapp-old-conv"] {
		t.Fatalf("old export conversations not recorded in ExcludedSessions: %#v", persisted.ExcludedSessions)
	}
	for _, batch := range sink.chunks {
		for _, chunk := range batch {
			if chunk.SessionID == "chatgpt-old-conv" || chunk.SessionID == "claudeapp-old-conv" {
				t.Fatalf("excluded export conversation uploaded: %#v", chunk)
			}
			if chunk.Timestamp.Before(persisted.Cutoff) {
				t.Fatalf("chunk timestamp %v before cutoff %v", chunk.Timestamp, persisted.Cutoff)
			}
		}
	}

	// Update ChatGPT export with a second new conversation; verify incremental digest filtering skips chatgpt-new-conv
	batchesBefore := len(sink.chunks)
	chatgptUpdated := coverageJSON(t, []any{
		map[string]any{
			"id": "chatgpt-old-conv", "title": "Old",
			"messages": []any{
				map[string]any{"id": "m1", "role": "user", "content": "old q", "created_at": old.Format(time.RFC3339Nano)},
				map[string]any{"id": "m2", "role": "assistant", "content": "old a", "created_at": old.Format(time.RFC3339Nano)},
			},
		},
		map[string]any{
			"id": "chatgpt-new-conv", "title": "New",
			"messages": []any{
				map[string]any{"id": "m3", "role": "user", "content": "new q", "created_at": future.Format(time.RFC3339Nano)},
				map[string]any{"id": "m4", "role": "assistant", "content": "new a", "created_at": future.Format(time.RFC3339Nano)},
			},
		},
		map[string]any{
			"id": "chatgpt-new-conv-2", "title": "New 2",
			"messages": []any{
				map[string]any{"id": "m5", "role": "user", "content": "new 2 q", "created_at": later.Format(time.RFC3339Nano)},
				map[string]any{"id": "m6", "role": "assistant", "content": "new 2 a", "created_at": later.Format(time.RFC3339Nano)},
			},
		},
	})
	writeForwardFile(t, chatgptPath, chatgptUpdated)
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan updated ChatGPT export: %v", err)
	}
	if len(sink.chunks) != batchesBefore+1 || len(sink.chunks[len(sink.chunks)-1]) != 1 || sink.chunks[len(sink.chunks)-1][0].SessionID != "chatgpt-new-conv-2" {
		t.Fatalf("incremental digest filtering failed: got %#v", sink.chunks[len(sink.chunks)-1])
	}
}

func TestForwardReconcilesVolumeDriftSupersetRootsAndUnsafeSources(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	// Create a hard-linked file in root1 before baselining to test baselineForwardPaths ErrUnsafeSource skip
	hardTarget := filepath.Join(root1, "hard-target.jsonl")
	hardLink := filepath.Join(root1, "hard-link.jsonl")
	writeForwardFile(t, hardTarget, []byte("target\n"))
	if err := os.Link(hardTarget, hardLink); err != nil {
		t.Fatal(err)
	}

	state, _ := openForwardState(t)
	sink := &recordingSink{}
	registry := Registry{MachineID: "reconcile-host", MaxBytes: MaxSourceSnapshotBytes}
	producer, err := NewForwardProducer(registry, sink, []string{root1}, time.Second, state)
	if err != nil {
		t.Fatalf("initial NewForwardProducer with hardlink: %v", err)
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate macOS st_dev Volume drift and unsorted roots in forward.json
	loaded, err := loadForwardState(state)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ExcludedPaths[filepath.Clean(hardTarget)] || !loaded.ExcludedPaths[filepath.Clean(hardLink)] {
		t.Fatalf("hard-linked baseline paths not excluded: %#v", loaded.ExcludedPaths)
	}
	oldVol := loaded.Roots[0].Volume
	fakeVol := oldVol + 999
	loaded.Roots[0].Volume = fakeVol
	loaded.ExcludedObjects[forwardIdentityKey(RootIdentity{Volume: fakeVol, Object: 0x1234})] = true
	raw, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.WriteAtomic(forwardStateName, raw); err != nil {
		t.Fatal(err)
	}

	// Add an existing file in root2 before adding root2 as a superset watch root
	existingRoot2 := filepath.Join(root2, "existing-in-root2.jsonl")
	writeForwardFile(t, existingRoot2, forwardTurn("root2-baseline-session", "old", time.Now().UTC().Add(24*time.Hour)))

	restarted, err := NewForwardProducer(registry, sink, []string{root2, root1}, time.Second, state)
	if err != nil {
		t.Fatalf("NewForwardProducer superset + volume drift: %v", err)
	}
	if err := restarted.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("superset root2 uploaded existing file: %d batches", len(sink.chunks))
	}
	reconciled, err := loadForwardState(state)
	if err != nil {
		t.Fatal(err)
	}
	if !reconciled.ExcludedPaths[filepath.Clean(existingRoot2)] {
		t.Fatalf("superset root2 existing file was not baselined: %#v", reconciled.ExcludedPaths)
	}
	if !reconciled.ExcludedObjects[forwardIdentityKey(RootIdentity{Volume: oldVol, Object: 0x1234})] {
		t.Fatalf("st_dev volume drift did not migrate ExcludedObjects: %#v", reconciled.ExcludedObjects)
	}

	// Create a new hard-linked file during runtime scan to test scanForward ErrUnsafeSource refusal
	runtimeTarget := filepath.Join(root1, "runtime-target.jsonl")
	runtimeLink := filepath.Join(root1, "runtime-link.jsonl")
	writeForwardFile(t, runtimeTarget, []byte("runtime\n"))
	if err := os.Link(runtimeTarget, runtimeLink); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Scan(context.Background()); err != nil {
		t.Fatalf("Scan runtime hard-linked file: %v", err)
	}
	reconciled, err = loadForwardState(state)
	if err != nil {
		t.Fatal(err)
	}
	if !reconciled.ExcludedPaths[filepath.Clean(runtimeTarget)] {
		t.Fatalf("runtime hard-linked file was not excluded: %#v", reconciled.ExcludedPaths)
	}

	// Test batchFull sentinel and transient ErrSourceChanged / fs.ErrNotExist in processForwardPath
	freshPath := filepath.Join(root2, "fresh.jsonl")
	writeForwardFile(t, freshPath, forwardTurn("fresh-session", "hi", time.Now().UTC().Add(24*time.Hour)))
	root2Pinned := restarted.roots[0]
	if root2Pinned.Path() != filepath.Clean(root2) {
		root2Pinned = restarted.roots[1]
	}
	ident, err := root2Pinned.forwardSourceIdentity(filepath.Clean(freshPath))
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := restarted.processForwardPath(context.Background(), root2Pinned, filepath.Clean(freshPath), SourceClaude, ident, restarted.forward, true); changed || !errors.Is(err, errForwardScanBatchFull) {
		t.Fatalf("processForwardPath(batchFull=true) = %t, %v", changed, err)
	}
	prevSnap := snapshotForwardSource
	snapshotForwardSource = func(*PinnedRoot, string, int64) (*Snapshot, error) {
		return nil, ErrSourceChanged
	}
	if changed, err := restarted.processForwardPath(context.Background(), root2Pinned, filepath.Clean(freshPath), SourceClaude, ident, restarted.forward, false); changed || err != nil {
		t.Fatalf("processForwardPath(ErrSourceChanged) = %t, %v", changed, err)
	}
	snapshotForwardSource = prevSnap
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}

	// Replace root1 directory so its inode changes and verify NewForwardProducer refuses it
	replacementRoot1 := root1 + "-replacement"
	if err := os.MkdirAll(replacementRoot1, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root1); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementRoot1, root1); err != nil {
		t.Fatal(err)
	}
	if p, err := NewForwardProducer(registry, sink, []string{root1, root2}, time.Second, state); err == nil {
		_ = p.Close()
		t.Fatal("NewForwardProducer accepted replaced root directory with changed inode")
	}
}
