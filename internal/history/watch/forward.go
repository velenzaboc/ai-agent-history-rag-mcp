package watch

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/durable"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/antigravity"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/chatgpt"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/claude"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/claudeapp"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/codex"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/gemini"
)

const (
	forwardStateName     = "forward.json"
	forwardStateVersion  = 1
	maxForwardStateBytes = 128 << 20
)

var (
	snapshotForwardSource = func(root *PinnedRoot, path string, maxBytes int64) (*Snapshot, error) {
		return root.Snapshot(path, maxBytes)
	}
	persistForwardState = func(root *durable.Root, name string, payload []byte) error {
		return root.WriteAtomic(name, payload)
	}
	forwardSessionID = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	forwardObjectID  = regexp.MustCompile(`^[0-9a-f]{16}:[0-9a-f]{16}$`)
)

type forwardRootState struct {
	Path   string `json:"path"`
	Volume uint64 `json:"volume"`
	Object uint64 `json:"object"`
}

type forwardState struct {
	Version          int                `json:"version"`
	Cutoff           time.Time          `json:"cutoff"`
	Roots            []forwardRootState `json:"roots"`
	ExcludedPaths    map[string]bool    `json:"excluded_paths"`
	ExcludedObjects  map[string]bool    `json:"excluded_objects"`
	ExcludedSessions map[string]bool    `json:"excluded_sessions"`
	Progress         map[string]string  `json:"progress"`
}

type forwardTracker struct {
	root  *durable.Root
	state forwardState
}

type forwardEligibility int

const (
	forwardWait forwardEligibility = iota
	forwardAccept
	forwardRefuse
)

// NewForwardProducer establishes a durable metadata-only cutoff before the
// producer can scan or upload source content. Existing paths are exclusions,
// not cursors: replacement, mutation, and restart can never make them current.
func NewForwardProducer(registry Registry, sink ChunkSink, paths []string, interval time.Duration, stateRoot *durable.Root) (*Producer, error) {
	if stateRoot == nil {
		return nil, errors.New("forward watch producer requires durable state")
	}
	producer, err := NewProducer(registry, sink, paths, interval)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Producer, error) {
		_ = producer.Close()
		return nil, err
	}

	roots, err := currentForwardRoots(producer.roots)
	if err != nil {
		return fail(err)
	}
	exists, err := stateRoot.Exists(forwardStateName)
	if err != nil {
		return fail(fmt.Errorf("inspect forward state: %w", err))
	}
	if exists {
		state, err := loadForwardState(stateRoot)
		if err != nil {
			return fail(err)
		}
		if !slices.Equal(state.Roots, roots) {
			return fail(errors.New("forward watch roots do not match durable state"))
		}
		producer.forward = &forwardTracker{root: stateRoot, state: state}
		return producer, nil
	}

	state := forwardState{
		Version:          forwardStateVersion,
		Cutoff:           time.Now().UTC(),
		Roots:            roots,
		ExcludedPaths:    make(map[string]bool),
		ExcludedObjects:  make(map[string]bool),
		ExcludedSessions: make(map[string]bool),
		Progress:         make(map[string]string),
	}
	if err := baselineForwardPaths(producer.registry, producer.roots, &state); err != nil {
		return fail(err)
	}
	if err := writeForwardState(stateRoot, state); err != nil {
		return fail(fmt.Errorf("persist initial forward state: %w", err))
	}
	producer.forward = &forwardTracker{root: stateRoot, state: state}
	return producer, nil
}

func currentForwardRoots(roots []*PinnedRoot) ([]forwardRootState, error) {
	result := make([]forwardRootState, 0, len(roots))
	for _, root := range roots {
		identity, ok := root.Identity()
		if !ok {
			return nil, ErrRootUnbound
		}
		result = append(result, forwardRootState{Path: root.Path(), Volume: identity.Volume, Object: identity.Object})
	}
	return result, nil
}

func baselineForwardPaths(registry Registry, roots []*PinnedRoot, state *forwardState) error {
	for _, root := range roots {
		if err := root.Verify(); err != nil {
			return err
		}
		if err := filepath.WalkDir(root.Path(), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || !entry.Type().IsRegular() {
				return nil
			}
			if _, err := registry.Detect(path); errors.Is(err, ErrUnsupportedSource) {
				return nil
			} else if err != nil {
				return err
			}
			clean := filepath.Clean(path)
			identity, err := root.forwardSourceIdentity(clean)
			if err != nil {
				return err
			}
			state.ExcludedPaths[clean] = true
			state.ExcludedObjects[forwardIdentityKey(identity)] = true
			if session := sessionIDFromPath(clean); session != "" {
				state.ExcludedSessions[session] = true
			}
			return nil
		}); err != nil {
			return err
		}
		if err := root.Verify(); err != nil {
			return err
		}
	}
	return nil
}

func sessionIDFromPath(path string) string {
	match := forwardSessionID.FindString(filepath.Base(path))
	return strings.ToLower(match)
}

func forwardIdentityKey(identity RootIdentity) string {
	return fmt.Sprintf("%016x:%016x", identity.Volume, identity.Object)
}

func loadForwardState(root *durable.Root) (forwardState, error) {
	payload, err := root.Read(forwardStateName, maxForwardStateBytes)
	if err != nil {
		return forwardState{}, fmt.Errorf("read forward state: %w", err)
	}
	if err := rejectDuplicateForwardKeys(payload); err != nil {
		return forwardState{}, fmt.Errorf("decode forward state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var state forwardState
	if err := decoder.Decode(&state); err != nil {
		return forwardState{}, fmt.Errorf("decode forward state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return forwardState{}, errors.New("decode forward state: trailing data")
	}
	if err := validateForwardState(state); err != nil {
		return forwardState{}, err
	}
	return state, nil
}

func rejectDuplicateForwardKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := walkForwardJSON(decoder); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

func walkForwardJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("duplicate or invalid object key")
			}
			seen[key] = true
			if err := walkForwardJSON(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := walkForwardJSON(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("unterminated array")
		}
	default:
		return errors.New("unexpected delimiter")
	}
	return nil
}

func validateForwardState(state forwardState) error {
	if state.Version != forwardStateVersion || state.Cutoff.IsZero() || len(state.Roots) == 0 || state.ExcludedPaths == nil || state.ExcludedObjects == nil || state.ExcludedSessions == nil || state.Progress == nil {
		return errors.New("forward state is invalid")
	}
	for index, root := range state.Roots {
		if !filepath.IsAbs(root.Path) || filepath.Clean(root.Path) != root.Path || (index > 0 && state.Roots[index-1].Path >= root.Path) {
			return errors.New("forward state roots are invalid")
		}
	}
	for path, excluded := range state.ExcludedPaths {
		if !excluded || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("forward state exclusions are invalid")
		}
	}
	for identity, excluded := range state.ExcludedObjects {
		if !excluded || !forwardObjectID.MatchString(identity) {
			return errors.New("forward state object exclusions are invalid")
		}
	}
	for session, excluded := range state.ExcludedSessions {
		if !excluded || strings.TrimSpace(session) == "" {
			return errors.New("forward state session exclusions are invalid")
		}
	}
	for path, digest := range state.Progress {
		decoded, err := hex.DecodeString(digest)
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || err != nil || len(decoded) != 32 {
			return errors.New("forward state progress is invalid")
		}
	}
	return nil
}

func writeForwardState(root *durable.Root, state forwardState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(payload) > maxForwardStateBytes {
		return errors.New("forward state exceeds size limit")
	}
	return persistForwardState(root, forwardStateName, payload)
}

func cloneForwardState(state forwardState) forwardState {
	clone := state
	clone.Roots = append([]forwardRootState(nil), state.Roots...)
	clone.ExcludedPaths = cloneBoolMap(state.ExcludedPaths)
	clone.ExcludedObjects = cloneBoolMap(state.ExcludedObjects)
	clone.ExcludedSessions = cloneBoolMap(state.ExcludedSessions)
	clone.Progress = make(map[string]string, len(state.Progress))
	for key, value := range state.Progress {
		clone.Progress[key] = value
	}
	return clone
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func (p *Producer) scanForward(ctx context.Context, roots []*PinnedRoot, tracker *forwardTracker) error {
	seen := 0
	for _, root := range roots {
		if err := filepath.WalkDir(root.Path(), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || !entry.Type().IsRegular() {
				return nil
			}
			kind, err := p.registry.Detect(path)
			if errors.Is(err, ErrUnsupportedSource) {
				return nil
			}
			if err != nil {
				return err
			}
			path = filepath.Clean(path)
			if tracker.state.ExcludedPaths[path] {
				return nil
			}
			identity, err := root.forwardSourceIdentity(path)
			if err != nil {
				return err
			}
			if tracker.state.ExcludedObjects[forwardIdentityKey(identity)] {
				return nil
			}
			seen++
			if seen > MaxFilesPerScan {
				return ErrScanLimit
			}
			return p.processForwardPath(ctx, root, path, kind, identity, tracker)
		}); err != nil {
			p.setReady(false)
			return err
		}
	}
	p.setReady(true)
	return nil
}

func (p *Producer) processForwardPath(ctx context.Context, root *PinnedRoot, path string, kind SourceKind, identity RootIdentity, tracker *forwardTracker) error {
	snapshot, err := snapshotForwardSource(root, path, MaxSourceSnapshotBytes)
	if err != nil {
		return err
	}
	digestBytes := snapshot.SHA256()
	digest := hex.EncodeToString(digestBytes[:])
	if tracker.state.Progress[path] == digest {
		return snapshot.Close()
	}

	eligibility, sessions := inspectForwardEligibility(kind, snapshot, tracker.state.Cutoff)
	if eligibility == forwardWait {
		return snapshot.Close()
	}
	if eligibility == forwardRefuse || containsExcludedSession(tracker.state.ExcludedSessions, sessions) {
		if closeErr := snapshot.Close(); closeErr != nil {
			return closeErr
		}
		return refuseForwardPath(tracker, path, identity, sessions)
	}

	chunks, dispatchErr := p.registry.Dispatch(kind, snapshot.Reader(), path)
	closeErr := snapshot.Close()
	if dispatchErr != nil {
		return dispatchErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(chunks) == 0 {
		return nil
	}
	for _, chunk := range chunks {
		if chunk.Timestamp.IsZero() || chunk.Timestamp.Before(tracker.state.Cutoff) || tracker.state.ExcludedSessions[strings.ToLower(chunk.SessionID)] {
			return refuseForwardPath(tracker, path, identity, append(sessions, chunk.SessionID))
		}
	}
	if err := p.sink.Upsert(ctx, chunks); err != nil {
		return err
	}
	next := cloneForwardState(tracker.state)
	next.Progress[path] = digest
	if err := writeForwardState(tracker.root, next); err != nil {
		return fmt.Errorf("persist forward progress: %w", err)
	}
	tracker.state = next
	return nil
}

func refuseForwardPath(tracker *forwardTracker, path string, identity RootIdentity, sessions []string) error {
	next := cloneForwardState(tracker.state)
	next.ExcludedPaths[path] = true
	next.ExcludedObjects[forwardIdentityKey(identity)] = true
	for _, session := range sessions {
		if session = strings.ToLower(strings.TrimSpace(session)); reliableForwardSession(session) {
			next.ExcludedSessions[session] = true
		}
	}
	if err := writeForwardState(tracker.root, next); err != nil {
		return fmt.Errorf("persist forward refusal: %w", err)
	}
	tracker.state = next
	return nil
}

func reliableForwardSession(session string) bool {
	if session == "" || session == "unknown" || session == "gemini-session" || session == "gemini-logs" {
		return false
	}
	for _, character := range session {
		if character < '0' || character > '9' {
			return true
		}
	}
	return false
}

func containsExcludedSession(excluded map[string]bool, sessions []string) bool {
	for _, session := range sessions {
		if excluded[strings.ToLower(strings.TrimSpace(session))] {
			return true
		}
	}
	return false
}

func inspectForwardEligibility(kind SourceKind, snapshot *Snapshot, cutoff time.Time) (forwardEligibility, []string) {
	switch kind {
	case SourceClaude:
		entries, issues, err := claude.Parse(snapshot.Reader(), snapshot.Size())
		if err != nil {
			return forwardWait, nil
		}
		for _, issue := range issues {
			if issue.Reason == "invalid_timestamp" {
				return forwardRefuse, nil
			}
		}
		if len(issues) != 0 {
			return forwardWait, nil
		}
		times, sessions := make([]time.Time, 0, len(entries)), make([]string, 0, len(entries))
		for _, located := range entries {
			entry := located.Entry
			if entry.SessionID != "" {
				sessions = append(sessions, entry.SessionID)
			}
			meaningful := entry.Message != nil || strings.TrimSpace(entry.Summary) != "" || entry.CompactSummary
			if !meaningful {
				continue
			}
			if !entry.HasTimestamp {
				return forwardRefuse, sessions
			}
			times = append(times, entry.Timestamp)
		}
		return classifyForwardTimes(times, sessions, cutoff)
	case SourceCodex:
		events, issues, err := codex.Parse(snapshot.Reader(), snapshot.Size())
		if err != nil {
			return forwardWait, nil
		}
		for _, issue := range issues {
			if issue.Reason == "invalid_timestamp" {
				return forwardRefuse, nil
			}
		}
		if len(issues) != 0 {
			return forwardWait, nil
		}
		times, sessions := make([]time.Time, 0, len(events)), make([]string, 0, 1)
		if session := sessionIDFromPath(snapshot.SourcePath()); session != "" {
			sessions = append(sessions, session)
		}
		for _, event := range events {
			if event.Type == "session_meta" {
				if session, _ := event.Payload["id"].(string); session != "" {
					sessions = append(sessions, session)
				}
			}
			if !event.HasTime {
				return forwardRefuse, sessions
			}
			times = append(times, event.Timestamp)
		}
		return classifyForwardTimes(times, sessions, cutoff)
	case SourceGemini:
		document, issues, err := gemini.Parse(snapshot.Reader(), snapshot.Size())
		if err != nil || len(issues) != 0 {
			return forwardWait, nil
		}
		times := make([]time.Time, 0, len(document.Messages)+len(document.Events))
		for _, message := range document.Messages {
			if !message.HasTime {
				return forwardRefuse, []string{document.SessionID}
			}
			times = append(times, message.Timestamp)
		}
		for _, event := range document.Events {
			if !event.HasTime {
				return forwardRefuse, []string{event.SessionID}
			}
			times = append(times, event.Timestamp)
		}
		return classifyForwardTimes(times, []string{document.SessionID}, cutoff)
	case SourceAntigravity:
		if strings.EqualFold(filepath.Ext(snapshot.SourcePath()), ".pb") {
			if snapshot.Size() == 0 {
				return forwardWait, nil
			}
			return forwardRefuse, []string{sessionIDFromPath(snapshot.SourcePath())}
		}
		events, issues, err := antigravity.Parse(snapshot.Reader(), snapshot.Size())
		if err != nil {
			return forwardWait, nil
		}
		for _, issue := range issues {
			if issue.Reason == "invalid_timestamp" {
				return forwardRefuse, nil
			}
		}
		if len(issues) != 0 {
			return forwardWait, nil
		}
		times := make([]time.Time, 0, len(events))
		for _, event := range events {
			if !event.HasTime {
				return forwardRefuse, nil
			}
			times = append(times, event.Timestamp)
		}
		return classifyForwardTimes(times, nil, cutoff)
	case SourceChatGPT:
		conversations, issues, err := chatgpt.Parse(snapshot.Reader(), snapshot.Size())
		if err != nil || len(issues) != 0 {
			return forwardWait, nil
		}
		var times []time.Time
		var sessions []string
		for _, conversation := range conversations {
			sessions = append(sessions, conversation.ID)
			for _, message := range conversation.Messages {
				if strings.TrimSpace(message.Content) == "" {
					continue
				}
				if !message.HasTime {
					return forwardRefuse, sessions
				}
				times = append(times, message.Timestamp)
			}
		}
		return classifyForwardTimes(times, sessions, cutoff)
	case SourceClaudeApp:
		conversations, issues, err := claudeapp.Parse(snapshot.Reader(), snapshot.Size())
		if err != nil || len(issues) != 0 {
			return forwardWait, nil
		}
		var times []time.Time
		var sessions []string
		for _, conversation := range conversations {
			sessions = append(sessions, conversation.ID)
			for _, message := range conversation.Messages {
				if strings.TrimSpace(message.Content) == "" {
					continue
				}
				if !message.HasTime {
					return forwardRefuse, sessions
				}
				times = append(times, message.Timestamp)
			}
		}
		return classifyForwardTimes(times, sessions, cutoff)
	default:
		return forwardWait, nil
	}
}

func classifyForwardTimes(times []time.Time, sessions []string, cutoff time.Time) (forwardEligibility, []string) {
	if len(times) == 0 {
		return forwardWait, sessions
	}
	for _, timestamp := range times {
		if timestamp.IsZero() || timestamp.Before(cutoff) {
			return forwardRefuse, sessions
		}
	}
	sort.Strings(sessions)
	sessions = slices.Compact(sessions)
	return forwardAccept, sessions
}
