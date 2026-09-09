// Package claudeapp parses official Claude App conversations exports.
package claudeapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/store"
	"io"
	"strings"
	"time"
)

const (
	MaxFileBytes          int64 = 10 << 30
	MaxJSONDepth                = 50
	MaxChunkContentLength       = 8000
)

var errLarge = errors.New("Claude App export too large")

type Source struct {
	FilePath, MachineID string
	MaxBytes            int64
}
type Issue struct{ Conversation, Message, Reason string }
type Conversation struct {
	ID, Title string
	Messages  []Message
}
type Message struct {
	ID, Role, Author, Content string
	Artifacts, Tools          []string
	Timestamp                 time.Time
	HasTime                   bool
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
		c := Conversation{first(text(o["uuid"]), text(o["id"]), text(o["conversation_id"]), fmt.Sprint(i+1)), first(text(o["name"]), text(o["title"]), "Untitled"), nil}
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
		var u *Message
		for mi := range c.Messages {
			m := c.Messages[mi]
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
					at = time.Unix(0, int64((ci+1)*100000+mi+1)*1000).UTC()
				}
				content := "In Claude app conversation " + c.Title + ", user asked:\n" + u.Content + "\n\nClaude responded:\n" + m.Content
				out = append(out, store.Chunk{ID: id(content, c.ID, key(at)), Content: content, ChunkType: "turn", SessionID: c.ID, ProjectPath: "/claude-app/export", ProjectName: "Claude App", Timestamp: at, UserUUID: u.ID, AssistantUUID: m.ID, SourceFile: source.FilePath, SourceLine: int64((ci+1)*100000 + mi + 1), MachineID: source.MachineID})
				u = nil
			}
		}
	}
	return out, is, nil
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
	m := Message{ID: first(text(o["uuid"]), text(o["id"]), text(o["message_id"]), fmt.Sprint(i)), Role: r, Author: a, Content: content(o)}
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
