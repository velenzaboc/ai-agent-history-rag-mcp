// Package claudeapp parses official Claude App conversations exports.
package claudeapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
	"io"
	"strings"
	"time"
)

const (
	MaxFileBytes          int64 = 10 << 30
	MaxJSONDepth                = 50
	MaxChunkContentLength       = 8000
	ChunkOverlap                = 1500
)

var errLarge = errors.New("Claude App export too large")

type Source struct {
	FilePath, MachineID string
	MaxBytes            int64
}
type Issue struct{ Conversation, Message, Reason string }
type Conversation struct {
	ID, Title                                         string
	PostTurnSummary, LastTurnReport, SideSessionNotes string
	Messages                                          []Message
}
type Message struct {
	ID, Role, Author, Content                         string
	PostTurnSummary, LastTurnReport, SideSessionNotes string
	Artifacts, Tools                                  []string
	Timestamp                                         time.Time
	HasTime                                           bool
}

func Parse(input io.Reader, max int64) ([]Conversation, []Issue, error) {
	if input == nil {
		return nil, nil, errors.New("Claude App input is required")
	}
	if max == 0 {
		max = MaxFileBytes
	}
	if max < 1 || max > MaxFileBytes {
		return nil, nil, errors.New("Claude App byte limit is invalid")
	}
	b, e := io.ReadAll(&bound{input, max + 1})
	if e != nil {
		if errors.Is(e, errLarge) {
			return nil, nil, fmt.Errorf("Claude App export exceeds %d bytes", max)
		}
		return nil, nil, e
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil, nil, errors.New("invalid Claude App JSON")
	}
	if depth(v, 0) > MaxJSONDepth {
		return nil, nil, errors.New("Claude App JSON too deep")
	}
	raw, ok := convos(v)
	if !ok {
		return nil, nil, errors.New("Claude App conversations invalid")
	}
	out, issues := []Conversation{}, []Issue{}
	for i, x := range raw {
		o, ok := x.(map[string]any)
		if !ok {
			issues = append(issues, Issue{fmt.Sprint(i + 1), "", "conversation_not_object"})
			continue
		}
		c := Conversation{
			ID:               first(text(o["uuid"]), text(o["id"]), text(o["conversation_id"]), fmt.Sprint(i+1)),
			Title:            first(text(o["name"]), text(o["title"]), "Untitled"),
			PostTurnSummary:  first(value(o["postTurnSummary"]), value(o["post_turn_summary"])),
			LastTurnReport:   first(value(o["lastTurnReport"]), value(o["last_turn_report"])),
			SideSessionNotes: first(value(o["sideSessionNotes"]), value(o["side_session_notes"])),
		}
		messages, _ := o["chat_messages"].([]any)
		if messages == nil {
			messages, _ = o["messages"].([]any)
		}
		for j, x := range messages {
			m, ok := message(x, j+1)
			if !ok {
				issues = append(issues, Issue{c.ID, fmt.Sprint(j + 1), "message_not_object"})
				continue
			}
			c.Messages = append(c.Messages, m)
		}
		out = append(out, c)
	}
	return out, issues, nil
}
func Chunks(input io.Reader, source Source) ([]store.Chunk, []Issue, error) {
	if source.FilePath == "" {
		return nil, nil, errors.New("Claude App source identity is required")
	}
	cs, is, e := Parse(input, source.MaxBytes)
	if e != nil {
		return nil, is, e
	}
	out := []store.Chunk{}
	for ci, c := range cs {
		var convTime time.Time
		for _, m := range c.Messages {
			if m.HasTime && m.Timestamp.Unix() > 1 && convTime.IsZero() {
				convTime = m.Timestamp
			}
		}
		if notes := extraNotes(c.PostTurnSummary, c.LastTurnReport, c.SideSessionNotes); notes != "" {
			at := convTime
			if at.IsZero() {
				at = time.Unix(0, int64((ci+1)*100000)*1000).UTC()
			}
			out = append(out, makeChunks("Session summary for Claude app conversation "+c.Title+":\n"+notes, "summary", c.ID, at, "", "", source, (ci+1)*100000)...)
		}
		var u *Message
		for mi := range c.Messages {
			m := c.Messages[mi]
			if extra := extraNotes(m.PostTurnSummary, m.LastTurnReport, m.SideSessionNotes); extra != "" {
				if m.Content != "" {
					m.Content += "\n\n" + extra
				} else {
					m.Content = extra
				}
			}
			if m.Content == "" {
				continue
			}
			if m.Role == "user" {
				q := m
				u = &q
			} else if m.Role == "assistant" && u != nil {
				at := m.Timestamp
				if !m.HasTime {
					at = u.Timestamp
				}
				if at.IsZero() {
					at = convTime
				}
				if at.IsZero() {
					at = time.Unix(0, int64((ci+1)*100000+mi+1)*1000).UTC()
				}
				content := "In Claude app conversation " + c.Title + ", user asked:\n" + u.Content + "\n\nClaude responded:\n" + m.Content
				out = append(out, makeChunks(content, "turn", c.ID, at, u.ID, m.ID, source, (ci+1)*100000+mi+1)...)
				u = nil
			} else if m.PostTurnSummary != "" || m.LastTurnReport != "" || m.SideSessionNotes != "" {
				at := m.Timestamp
				if !m.HasTime {
					at = convTime
				}
				if at.IsZero() {
					at = time.Unix(0, int64((ci+1)*100000+mi+1)*1000).UTC()
				}
				out = append(out, makeChunks("In Claude app conversation "+c.Title+":\n"+m.Content, "summary", c.ID, at, "", m.ID, source, (ci+1)*100000+mi+1)...)
			}
		}
	}
	return out, is, nil
}
func extraNotes(post, report, notes string) string {
	var parts []string
	if post != "" {
		parts = append(parts, "Post-turn summary:\n"+post)
	}
	if report != "" {
		parts = append(parts, "Last turn report:\n"+report)
	}
	if notes != "" {
		parts = append(parts, "Side session notes:\n"+notes)
	}
	return strings.Join(parts, "\n\n")
}
func makeChunks(content, kind, session string, at time.Time, userID, assistantID string, source Source, line int) []store.Chunk {
	parts, base := splitContent(content), id(content, session, key(at))
	chunks := make([]store.Chunk, 0, len(parts))
	for i, part := range parts {
		chunkID, parent := base, ""
		if len(parts) > 1 {
			part = fmt.Sprintf("[Part %d/%d] %s", i+1, len(parts), part)
			chunkID = id(part, session, key(at)+fmt.Sprintf("_part%d", i+1))
			if i > 0 {
				parent = base
			}
		}
		chunks = append(chunks, store.Chunk{ID: chunkID, Content: part, ChunkType: kind, SessionID: session, ProjectPath: "/claude-app/export", ProjectName: "Claude App", Timestamp: at, UserUUID: userID, AssistantUUID: assistantID, SourceFile: source.FilePath, SourceLine: int64(line), ParentChunkID: parent, MachineID: source.MachineID})
	}
	return chunks
}
func splitContent(content string) []string {
	if len(content) <= MaxChunkContentLength {
		return []string{content}
	}
	parts, start := make([]string, 0), 0
	for start < len(content) {
		end := start + MaxChunkContentLength
		if end >= len(content) {
			return append(parts, content[start:])
		}
		regionStart := max(start, end-1000)
		region := content[regionStart:end]
		if at := strings.LastIndex(region, "\n\n"); at > 0 {
			end = regionStart + at + 2
		} else if at := max(strings.LastIndex(region, ". "), strings.LastIndex(region, "! "), strings.LastIndex(region, "? "), strings.LastIndex(region, "\n")); at > 0 {
			end = regionStart + at + 1
		}
		for end > start && (content[end]&0xc0) == 0x80 {
			end--
		}
		parts = append(parts, content[start:end])
		start = max(start+1, end-ChunkOverlap)
		for start < len(content) && (content[start]&0xc0) == 0x80 {
			start++
		}
	}
	return parts
}
func convos(v any) ([]any, bool) {
	if a, ok := v.([]any); ok {
		return a, true
	}
	if o, ok := v.(map[string]any); ok {
		a, ok := o["conversations"].([]any)
		return a, ok
	}
	return nil, false
}
func message(v any, i int) (Message, bool) {
	o, ok := v.(map[string]any)
	if !ok {
		return Message{}, false
	}
	r, a := role(o)
	m := Message{
		ID:               first(text(o["uuid"]), text(o["id"]), text(o["message_id"]), fmt.Sprint(i)),
		Role:             r,
		Author:           a,
		Content:          content(o),
		PostTurnSummary:  first(value(o["postTurnSummary"]), value(o["post_turn_summary"])),
		LastTurnReport:   first(value(o["lastTurnReport"]), value(o["last_turn_report"])),
		SideSessionNotes: first(value(o["sideSessionNotes"]), value(o["side_session_notes"])),
	}
	m.Timestamp, m.HasTime = stamp(first(text(o["created_at"]), text(o["createdAt"]), text(o["timestamp"])))
	for _, k := range []string{"artifacts", "tool_uses", "tools"} {
		if a, ok := o[k].([]any); ok {
			for _, x := range a {
				m.Artifacts = append(m.Artifacts, jsonText(x))
			}
		}
	}
	return m, true
}
func role(o map[string]any) (string, string) {
	v := o["sender"]
	if v == nil {
		v = o["role"]
	}
	if v == nil {
		v = o["author"]
	}
	if a, ok := v.(map[string]any); ok {
		v = a["role"]
		if v == nil {
			v = a["name"]
		}
	}
	s := strings.ToLower(text(v))
	if s == "human" {
		s = "user"
	}
	if s == "claude" {
		s = "assistant"
	}
	return s, text(v)
}
func content(o map[string]any) string {
	for _, k := range []string{"text", "content", "message"} {
		if s := value(o[k]); s != "" {
			return s
		}
	}
	return ""
}
func value(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		p := []string{}
		for _, v := range x {
			if s := value(v); s != "" {
				p = append(p, s)
			}
		}
		return strings.Join(p, "\n")
	case map[string]any:
		for _, k := range []string{"text", "content", "message", "value"} {
			if s := value(x[k]); s != "" {
				return s
			}
		}
	}
	return ""
}
func stamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	v, e := time.Parse(time.RFC3339Nano, s)
	return v.UTC(), e == nil
}
func text(v any) string { s, _ := v.(string); return s }
func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
func key(t time.Time) string { return t.Format("2006-01-02 15:04:05.999999999+00:00") }
func id(a, b, c string) string {
	s := sha256.Sum256([]byte(a + b + c))
	return hex.EncodeToString(s[:])[:16]
}
func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
func depth(v any, n int) int {
	m := n
	switch x := v.(type) {
	case map[string]any:
		for _, v := range x {
			m = max(m, depth(v, n+1))
		}
	case []any:
		for _, v := range x {
			m = max(m, depth(v, n+1))
		}
	}
	return m
}

type bound struct {
	r io.Reader
	n int64
}

func (b *bound) Read(p []byte) (int, error) {
	if b.n == 0 {
		return 0, errLarge
	}
	if int64(len(p)) > b.n {
		p = p[:b.n]
	}
	n, e := b.r.Read(p)
	b.n -= int64(n)
	return n, e
}
