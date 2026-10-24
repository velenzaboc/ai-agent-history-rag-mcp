package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/durable"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
)

func openForwardState(t *testing.T) (*durable.Root, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := durable.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, path
}

func forwardTurn(session, marker string, timestamp time.Time) []byte {
	return forwardTurnWithTimestamps(session, marker, timestamp, timestamp.Add(time.Second))
}

func forwardTurnWithTimestamps(session, marker string, userTimestamp, assistantTimestamp time.Time) []byte {
	records := []map[string]any{
		{
			"type":      "user",
			"uuid":      "user-" + marker,
			"sessionId": session,
			"timestamp": userTimestamp.UTC().Format(time.RFC3339Nano),
			"message": map[string]any{
				"role":    "user",
				"content": "forward user " + marker,
			},
		},
		{
			"type":      "assistant",
			"uuid":      "assistant-" + marker,
			"sessionId": session,
			"timestamp": assistantTimestamp.UTC().Format(time.RFC3339Nano),
			"message": map[string]any{
				"role":  "assistant",
				"model": "fixture",
				"content": []any{
					map[string]any{"type": "text", "text": "forward assistant " + marker},
				},
			},
		},
	}
	var payload []byte
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			panic(err)
		}
		payload = append(payload, encoded...)
		payload = append(payload, '\n')
	}
	return payload
}

func codexForwardSession(session, marker string, headerTimestamp, userTimestamp, assistantTimestamp time.Time, includeUser bool) []byte {
	records := []map[string]any{
		{
			"type":      "session_meta",
			"timestamp": headerTimestamp.UTC().Format(time.RFC3339Nano),
			"payload": map[string]any{
				"id":  session,
				"cwd": "/work/codex-forward",
			},
		},
	}
	if includeUser {
		records = append(records, map[string]any{
			"type":      "response_item",
			"timestamp": userTimestamp.UTC().Format(time.RFC3339Nano),
			"payload": map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "codex user " + marker},
				},
			},
		})
	}
	records = append(records, map[string]any{
		"type":      "response_item",
		"timestamp": assistantTimestamp.UTC().Format(time.RFC3339Nano),
		"payload": map[string]any{
			"type": "message",
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "output_text", "text": "codex assistant " + marker},
			},
		},
	})
	var payload []byte
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			panic(err)
		}
		payload = append(payload, encoded...)
		payload = append(payload, '\n')
	}
	return payload
}

func forwardTurnWithoutTimestamps(session string) []byte {
	return []byte(fmt.Sprintf(
		"{\"type\":\"user\",\"uuid\":\"unknown-user\",\"sessionId\":%q,\"message\":{\"role\":\"user\",\"content\":\"unknown time\"}}\n"+
			"{\"type\":\"assistant\",\"uuid\":\"unknown-assistant\",\"sessionId\":%q,\"message\":{\"role\":\"assistant\",\"model\":\"fixture\",\"content\":[{\"type\":\"text\",\"text\":\"unknown time\"}]}}\n",
		session, session,
	))
}

func writeForwardFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendForwardFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestForwardProducerBaselinesEveryExistingPathBeforeAnyUpload(t *testing.T) {
	sourceRoot := t.TempDir()
	for index := 0; index < MaxFilesPerScan+17; index++ {
		writeForwardFile(t, filepath.Join(sourceRoot, fmt.Sprintf("existing-%04d.jsonl", index)), []byte("not JSON and must never be parsed\n"))
	}
	oversized := filepath.Join(sourceRoot, "existing-oversized.jsonl")
	file, err := os.OpenFile(oversized, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxSourceSnapshotBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	state, _ := openForwardState(t)
	sink := &recordingSink{}
	previousSnapshot := snapshotForwardSource
	snapshotCalls := 0
	snapshotForwardSource = func(*PinnedRoot, string, int64) (*Snapshot, error) {
		snapshotCalls++
		return nil, errors.New("baseline source content was opened")
	}
	t.Cleanup(func() { snapshotForwardSource = previousSnapshot })
	producer, err := NewForwardProducer(Registry{MachineID: "cube", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatalf("NewForwardProducer baseline: %v", err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	if len(sink.chunks) != 0 {
		t.Fatalf("constructor uploaded baseline content: %d batches", len(sink.chunks))
	}
	if snapshotCalls != 0 {
		t.Fatalf("constructor opened %d baseline sources", snapshotCalls)
	}
	if exists, err := state.Exists("forward.json"); err != nil || !exists {
		t.Fatalf("durable baseline state = %t, %v", exists, err)
	}
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan metadata-only baseline: %v", err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("baseline scan uploaded existing content: %d batches", len(sink.chunks))
	}
	if snapshotCalls != 0 {
		t.Fatalf("baseline scan opened %d excluded sources", snapshotCalls)
	}
}

func TestForwardProducerAcceptsVariableHAPIProfileRootSet(t *testing.T) {
	base := t.TempDir()
	roots := make([]string, 0, 15)
	for index := 0; index < 10; index++ {
		root := filepath.Join(base, fmt.Sprintf("codex-profile-%02d", index), "sessions")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
	}
	for index := 0; index < 5; index++ {
		root := filepath.Join(base, fmt.Sprintf("claude-profile-%02d", index), "projects")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
	}
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	producer, err := NewForwardProducer(Registry{MachineID: "hapi-worker", MaxBytes: MaxSourceSnapshotBytes}, sink, roots, time.Second, state)
	if err != nil {
		t.Fatalf("NewForwardProducer HAPI roots: %v", err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan HAPI roots: %v", err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("empty HAPI roots uploaded content: %d batches", len(sink.chunks))
	}
}

func TestForwardProducerPermanentlyExcludesExistingPathAcrossMutationReplacementAndRestart(t *testing.T) {
	sourceRoot := t.TempDir()
	path := filepath.Join(sourceRoot, "existing.jsonl")
	writeForwardFile(t, path, []byte("existing content must not be parsed\n"))
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	registry := Registry{MachineID: "cube", MaxBytes: MaxSourceSnapshotBytes}

	producer, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatal(err)
	}
	writeForwardFile(t, path, forwardTurn("existing-session", "modified", time.Now().Add(24*time.Hour)))
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan modified baseline path: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeForwardFile(t, path, forwardTurn("replacement-session", "replacement", time.Now().Add(25*time.Hour)))
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan replaced baseline path: %v", err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("baseline path became uploadable: %d batches", len(sink.chunks))
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Scan(context.Background()); err != nil {
		t.Fatalf("restart scan: %v", err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("restart forgot excluded path: %d batches", len(sink.chunks))
	}
}

func TestForwardProducerRefusesLateHistoricalAndUnknownTimestampSessions(t *testing.T) {
	for name, initial := range map[string][]byte{
		"mixed old session with new tail": append(
			forwardTurn("copied-session", "old", time.Now().Add(-24*time.Hour)),
			forwardTurn("copied-session", "new-tail", time.Now().Add(24*time.Hour))...,
		),
		"missing timestamps": forwardTurnWithoutTimestamps("unknown-session"),
	} {
		t.Run(name, func(t *testing.T) {
			sourceRoot := t.TempDir()
			state, _ := openForwardState(t)
			sink := &recordingSink{}
			producer, err := NewForwardProducer(Registry{MachineID: "cube", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, time.Second, state)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = producer.Close() })
			path := filepath.Join(sourceRoot, "late.jsonl")
			writeForwardFile(t, path, initial)
			if err := producer.Scan(context.Background()); err != nil {
				t.Fatalf("Scan historical copy: %v", err)
			}
			appendForwardFile(t, path, forwardTurn("late-session", "later-update", time.Now().Add(48*time.Hour)))
			if err := producer.Scan(context.Background()); err != nil {
				t.Fatalf("Scan historical path update: %v", err)
			}
			if len(sink.chunks) != 0 {
				t.Fatalf("historical or unknown source became uploadable: %d batches", len(sink.chunks))
			}
		})
	}
}

func TestForwardProducerRefusesClaudeTurnWhoseRawUserTimestampPredatesCutoff(t *testing.T) {
	sourceRoot := t.TempDir()
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	registry := Registry{MachineID: "cube", MaxBytes: MaxSourceSnapshotBytes}
	producer, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sourceRoot, "cross-cutoff.jsonl")
	writeForwardFile(t, path, forwardTurnWithTimestamps(
		"cross-cutoff-session",
		"cross-cutoff",
		time.Now().Add(-24*time.Hour),
		time.Now().Add(24*time.Hour),
	))
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan cross-cutoff Claude turn: %v", err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("cross-cutoff Claude turn uploaded from projected assistant timestamp: %d batches", len(sink.chunks))
	}
	writeForwardFile(t, path, forwardTurn("cross-cutoff-session", "all-new-replacement", time.Now().Add(48*time.Hour)))
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("Scan replacement of refused Claude path: %v", err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("refused Claude path became uploadable after replacement: %d batches", len(sink.chunks))
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatalf("restart after cross-cutoff refusal: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Scan(context.Background()); err != nil {
		t.Fatalf("restart scan after cross-cutoff refusal: %v", err)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("restart forgot refused Claude path: %d batches", len(sink.chunks))
	}
}

func TestForwardProducerUsesRawCodexSessionMetadataForCutoff(t *testing.T) {
	t.Run("old metadata header refuses new projected chunk permanently", func(t *testing.T) {
		base := t.TempDir()
		sourceRoot := filepath.Join(base, "codex", "sessions")
		if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		state, _ := openForwardState(t)
		sink := &recordingSink{}
		registry := Registry{MachineID: "hapi-codex", MaxBytes: MaxSourceSnapshotBytes}
		producer, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(sourceRoot, "rollout.jsonl")
		writeForwardFile(t, path, codexForwardSession(
			"codex-old-header",
			"old-header",
			time.Now().Add(-24*time.Hour),
			time.Now().Add(24*time.Hour),
			time.Now().Add(24*time.Hour),
			false,
		))
		if err := producer.Scan(context.Background()); err != nil {
			t.Fatalf("Scan Codex old metadata header: %v", err)
		}
		if len(sink.chunks) != 0 {
			t.Fatalf("Codex old metadata header was discarded before cutoff enforcement: %d batches", len(sink.chunks))
		}
		writeForwardFile(t, path, codexForwardSession(
			"codex-old-header",
			"all-new-replacement",
			time.Now().Add(48*time.Hour),
			time.Now().Add(48*time.Hour),
			time.Now().Add(48*time.Hour),
			true,
		))
		if err := producer.Scan(context.Background()); err != nil {
			t.Fatalf("Scan replacement of refused Codex path: %v", err)
		}
		if len(sink.chunks) != 0 {
			t.Fatalf("refused Codex path became uploadable after replacement: %d batches", len(sink.chunks))
		}
		if err := producer.Close(); err != nil {
			t.Fatal(err)
		}
		restarted, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
		if err != nil {
			t.Fatalf("restart after Codex metadata refusal: %v", err)
		}
		t.Cleanup(func() { _ = restarted.Close() })
		if err := restarted.Scan(context.Background()); err != nil {
			t.Fatalf("restart scan after Codex metadata refusal: %v", err)
		}
		if len(sink.chunks) != 0 {
			t.Fatalf("restart forgot refused Codex path: %d batches", len(sink.chunks))
		}
	})

	t.Run("new metadata header and conversation are captured", func(t *testing.T) {
		base := t.TempDir()
		sourceRoot := filepath.Join(base, "codex", "sessions")
		if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		state, _ := openForwardState(t)
		sink := &recordingSink{}
		registry := Registry{MachineID: "hapi-codex", MaxBytes: MaxSourceSnapshotBytes}
		producer, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = producer.Close() })
		future := time.Now().Add(24 * time.Hour)
		writeForwardFile(t, filepath.Join(sourceRoot, "fresh.jsonl"), codexForwardSession(
			"codex-fresh-session",
			"fresh",
			future,
			future.Add(time.Second),
			future.Add(2*time.Second),
			true,
		))
		if err := producer.Scan(context.Background()); err != nil {
			t.Fatalf("Scan fresh Codex session: %v", err)
		}
		if len(sink.chunks) != 1 || len(sink.chunks[0]) == 0 {
			t.Fatalf("fresh Codex session batches = %#v", sink.chunks)
		}
		for _, chunk := range sink.chunks[0] {
			if chunk.SessionID != "codex-fresh-session" || chunk.MachineID != registry.MachineID {
				t.Fatalf("fresh Codex provenance = session:%q machine:%q", chunk.SessionID, chunk.MachineID)
			}
		}
	})
}

func TestForwardProducerCapturesNewUpdatesDeduplicatesAndPersistsProgress(t *testing.T) {
	sourceRoot := t.TempDir()
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	registry := Registry{MachineID: "cube-profile", MaxBytes: MaxSourceSnapshotBytes}
	previousSnapshot := snapshotForwardSource
	snapshotCalls := 0
	snapshotForwardSource = func(root *PinnedRoot, path string, maxBytes int64) (*Snapshot, error) {
		snapshotCalls++
		return root.Snapshot(path, maxBytes)
	}
	t.Cleanup(func() { snapshotForwardSource = previousSnapshot })
	producer, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sourceRoot, "new-session.jsonl")
	writeForwardFile(t, path, forwardTurn("new-session", "canary", time.Now().Add(24*time.Hour)))
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("first new scan: %v", err)
	}
	if len(sink.chunks) != 1 || len(sink.chunks[0]) == 0 {
		t.Fatalf("new session batches = %#v", sink.chunks)
	}
	if snapshotCalls == 0 {
		t.Fatal("new source content bypassed snapshotForwardSource")
	}
	for _, chunk := range sink.chunks[0] {
		if chunk.MachineID != registry.MachineID {
			t.Fatalf("machine provenance = %q, want %q", chunk.MachineID, registry.MachineID)
		}
	}
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("unchanged scan: %v", err)
	}
	if len(sink.chunks) != 1 {
		t.Fatalf("unchanged source resubmitted: %d batches", len(sink.chunks))
	}
	appendForwardFile(t, path, forwardTurn("new-session", "update", time.Now().Add(25*time.Hour)))
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("updated scan: %v", err)
	}
	if len(sink.chunks) != 2 {
		t.Fatalf("updated source batches = %d, want 2", len(sink.chunks))
	}
	updateSeen := false
	for _, chunk := range sink.chunks[1] {
		updateSeen = updateSeen || strings.Contains(chunk.Content, "update")
	}
	if !updateSeen {
		t.Fatalf("updated source batch omitted new content: %#v", sink.chunks[1])
	}
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("second unchanged scan: %v", err)
	}
	if len(sink.chunks) != 2 {
		t.Fatalf("updated source resubmitted unchanged: %d batches", len(sink.chunks))
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatalf("restart after success: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Scan(context.Background()); err != nil {
		t.Fatalf("restart unchanged scan: %v", err)
	}
	if len(sink.chunks) != 2 {
		t.Fatalf("restart resubmitted acknowledged source: %d batches", len(sink.chunks))
	}
}

func TestForwardProducerRetriesFailedSinkWithoutAcknowledgingSource(t *testing.T) {
	sourceRoot := t.TempDir()
	state, _ := openForwardState(t)
	wantErr := errors.New("store unavailable")
	sink := &recordingSink{err: wantErr}
	producer, err := NewForwardProducer(Registry{MachineID: "cube", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	writeForwardFile(t, filepath.Join(sourceRoot, "retry.jsonl"), forwardTurn("retry-session", "retry", time.Now().Add(24*time.Hour)))
	if err := producer.Scan(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("failed sink scan = %v, want %v", err, wantErr)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("failed sink recorded successful batch: %d", len(sink.chunks))
	}
	sink.err = nil
	if err := producer.Scan(context.Background()); err != nil {
		t.Fatalf("retry scan: %v", err)
	}
	if len(sink.chunks) != 1 {
		t.Fatalf("retry did not upload exactly once: %d batches", len(sink.chunks))
	}
	if err := producer.Scan(context.Background()); err != nil || len(sink.chunks) != 1 {
		t.Fatalf("post-retry unchanged scan = %v, %d batches", err, len(sink.chunks))
	}
}

type closingStateSink struct {
	recordingSink
	state       *durable.Root
	closeOnCall bool
}

func (s *closingStateSink) Upsert(ctx context.Context, chunks []store.Chunk) error {
	if err := s.recordingSink.Upsert(ctx, chunks); err != nil {
		return err
	}
	if s.closeOnCall {
		s.closeOnCall = false
		return s.state.Close()
	}
	return nil
}

func TestForwardProducerRetriesAfterUploadSucceededButProgressPersistenceFailed(t *testing.T) {
	sourceRoot := t.TempDir()
	state, statePath := openForwardState(t)
	sink := &closingStateSink{state: state, closeOnCall: true}
	registry := Registry{MachineID: "cube", MaxBytes: MaxSourceSnapshotBytes}
	producer, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, state)
	if err != nil {
		t.Fatal(err)
	}
	writeForwardFile(t, filepath.Join(sourceRoot, "persistence-retry.jsonl"), forwardTurn("persistence-retry", "retry", time.Now().Add(24*time.Hour)))
	if err := producer.Scan(context.Background()); err == nil {
		t.Fatal("Scan acknowledged an upload after durable progress state became unavailable")
	}
	if len(sink.chunks) != 1 {
		t.Fatalf("first upload attempts = %d, want 1", len(sink.chunks))
	}
	_ = producer.Close()

	reopened, err := durable.OpenRoot(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	sink.state = reopened
	restarted, err := NewForwardProducer(registry, sink, []string{sourceRoot}, time.Second, reopened)
	if err != nil {
		t.Fatalf("restart after persistence failure: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Scan(context.Background()); err != nil {
		t.Fatalf("retry after persistence failure: %v", err)
	}
	if len(sink.chunks) != 2 {
		t.Fatalf("unacknowledged upload was not retried: %d attempts", len(sink.chunks))
	}
}

func TestForwardProducerRefusesWhenInitialBaselinePersistenceFails(t *testing.T) {
	sourceRoot := t.TempDir()
	writeForwardFile(t, filepath.Join(sourceRoot, "existing.jsonl"), []byte("baseline content must remain unread\n"))
	state, _ := openForwardState(t)
	sink := &recordingSink{}
	wantErr := errors.New("initial forward state unavailable")
	previousPersist := persistForwardState
	persistCalls := 0
	persistForwardState = func(gotRoot *durable.Root, name string, payload []byte) error {
		persistCalls++
		if gotRoot != state || name != "forward.json" || len(payload) == 0 {
			t.Fatalf("initial persistence inputs = root:%v name:%q bytes:%d", gotRoot, name, len(payload))
		}
		return wantErr
	}
	t.Cleanup(func() { persistForwardState = previousPersist })

	producer, err := NewForwardProducer(Registry{MachineID: "cube", MaxBytes: MaxSourceSnapshotBytes}, sink, []string{sourceRoot}, time.Second, state)
	if producer != nil {
		_ = producer.Close()
		t.Fatal("NewForwardProducer returned a producer after initial state persistence failed")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("NewForwardProducer initial persistence error = %v, want %v", err, wantErr)
	}
	if persistCalls != 1 {
		t.Fatalf("initial persistence calls = %d, want 1", persistCalls)
	}
	if len(sink.chunks) != 0 {
		t.Fatalf("constructor uploaded before baseline persistence: %d batches", len(sink.chunks))
	}
	if exists, existsErr := state.Exists("forward.json"); existsErr != nil || exists {
		t.Fatalf("failed constructor left successful baseline state = %t, %v", exists, existsErr)
	}
}

func TestForwardProducerRefusesMissingCorruptAndRootMismatchedState(t *testing.T) {
	t.Run("missing durable state", func(t *testing.T) {
		if producer, err := NewForwardProducer(Registry{}, &recordingSink{}, []string{t.TempDir()}, time.Second, nil); err == nil {
			_ = producer.Close()
			t.Fatal("NewForwardProducer accepted nil durable state")
		}
	})

	t.Run("corrupt durable state", func(t *testing.T) {
		sourceRoot := t.TempDir()
		state, statePath := openForwardState(t)
		if err := state.WriteAtomic("forward.json", []byte("{not-json")); err != nil {
			t.Fatal(err)
		}
		if producer, err := NewForwardProducer(Registry{}, &recordingSink{}, []string{sourceRoot}, time.Second, state); err == nil {
			_ = producer.Close()
			t.Fatalf("NewForwardProducer accepted corrupt state at %s", statePath)
		}
	})

	t.Run("configured root set changed", func(t *testing.T) {
		firstRoot := t.TempDir()
		secondRoot := t.TempDir()
		state, _ := openForwardState(t)
		producer, err := NewForwardProducer(Registry{}, &recordingSink{}, []string{firstRoot}, time.Second, state)
		if err != nil {
			t.Fatal(err)
		}
		if err := producer.Close(); err != nil {
			t.Fatal(err)
		}
		if changed, err := NewForwardProducer(Registry{}, &recordingSink{}, []string{secondRoot}, time.Second, state); err == nil {
			_ = changed.Close()
			t.Fatal("NewForwardProducer accepted durable state for a different root set")
		}
	})
}

var _ ChunkSink = (*recordingSink)(nil)
