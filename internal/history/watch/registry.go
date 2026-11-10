package watch

import (
	"errors"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/antigravity"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/chatgpt"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/claude"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/claudeapp"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/codex"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/source/gemini"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/store"
	"io"
	"path/filepath"
	"strings"
)

var ErrUnsupportedSource = errors.New("unsupported history source")

type SourceKind string

const (
	SourceClaude      SourceKind = "claude"
	SourceCodex       SourceKind = "codex"
	SourceChatGPT     SourceKind = "chatgpt"
	SourceGemini      SourceKind = "gemini"
	SourceAntigravity SourceKind = "antigravity"
	SourceClaudeApp   SourceKind = "claudeapp"
)

type Registry struct {
	MachineID string
	MaxBytes  int64
}

func (r Registry) Detect(path string) (SourceKind, error) {
	base := filepath.Base(path)
	lowerBase := strings.ToLower(base)
	clean := filepath.ToSlash(path)
	lower := strings.ToLower(clean)
	switch {
	case base == "conversations.json" && strings.Contains(lower, "claude-app"):
		return SourceClaudeApp, nil
	case base == "conversations.json":
		return SourceChatGPT, nil
	case strings.Contains(lower, "/.gemini/antigravity/") || strings.Contains(lower, "/antigravity/user/"):
		if base == "transcript_full.jsonl" {
			return SourceAntigravity, nil
		}
		return "", ErrUnsupportedSource
	case base == "transcript_full.jsonl" || strings.HasSuffix(lowerBase, ".pb"):
		return SourceAntigravity, nil
	case strings.Contains(lower, "/.gemini/tmp/"):
		if base == "logs.json" || (strings.Contains(lower, "/chats/") && strings.HasPrefix(lowerBase, "session-") && strings.HasSuffix(lowerBase, ".json")) {
			return SourceGemini, nil
		}
		return "", ErrUnsupportedSource
	case base == "logs.json" || (strings.Contains(lower, "/chats/") && strings.HasSuffix(lowerBase, ".json")):
		return SourceGemini, nil
	case strings.HasSuffix(lowerBase, ".jsonl") && (strings.Contains(lower, "codex") || strings.Contains(lower, "/archived_sessions/") || strings.HasPrefix(lowerBase, "rollout-")):
		return SourceCodex, nil
	case strings.HasSuffix(lowerBase, ".jsonl"):
		return SourceClaude, nil
	}
	return "", ErrUnsupportedSource
}
func (r Registry) Dispatch(kind SourceKind, input io.Reader, path string) ([]store.Chunk, error) {
	switch kind {
	case SourceClaude:
		c, _, e := claude.Chunks(input, claude.Source{FilePath: path, MachineID: r.MachineID, MaxBytes: r.MaxBytes})
		return c, e
	case SourceCodex:
		c, _, e := codex.Chunks(input, codex.Source{FilePath: path, MachineID: r.MachineID, MaxBytes: r.MaxBytes})
		return c, e
	case SourceChatGPT:
		c, _, e := chatgpt.Chunks(input, chatgpt.Source{FilePath: path, MachineID: r.MachineID, MaxBytes: r.MaxBytes})
		return c, e
	case SourceGemini:
		c, _, e := gemini.Chunks(input, gemini.Source{FilePath: path, MachineID: r.MachineID, MaxBytes: r.MaxBytes})
		return c, e
	case SourceAntigravity:
		c, _, e := antigravity.Chunks(input, antigravity.Source{FilePath: path, MachineID: r.MachineID, MaxBytes: r.MaxBytes, Binary: strings.HasSuffix(path, ".pb")})
		return c, e
	case SourceClaudeApp:
		c, _, e := claudeapp.Chunks(input, claudeapp.Source{FilePath: path, MachineID: r.MachineID, MaxBytes: r.MaxBytes})
		return c, e
	}
	return nil, ErrUnsupportedSource
}
