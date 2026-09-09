package clientregistry

import (
	"errors"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/apiclient"
	"github.com/no13productions/ai-agent-history-rag-mcp/internal/history/watch"
	"path/filepath"
	"testing"
	"time"
)

func config(t *testing.T) Config {
	return Config{MachineID: "m", StateDir: filepath.Join(t.TempDir(), "state"), QueueCapacity: 2, API: apiclient.Config{Endpoint: "https://example.test", Token: "token", Timeout: time.Second}, Sources: []watch.SourceKind{watch.SourceClaudeApp, watch.SourceCodex, watch.SourceClaude, watch.SourceChatGPT, watch.SourceGemini, watch.SourceAntigravity}}
}
func TestOpenValidatedRegistry(t *testing.T) {
	r, e := Open(config(t))
	if e != nil {
		t.Fatal(e)
	}
	if !r.Supports(watch.SourceCodex) || r.Supports("nope") || r.Sources[0] != watch.SourceAntigravity {
		t.Fatal(r)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestRefusesBadConfigs(t *testing.T) {
	c := config(t)
	c.Sources = c.Sources[:5]
	if _, e := Open(c); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	c = config(t)
	c.Sources[1] = c.Sources[0]
	if _, e := Open(c); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	c = config(t)
	c.Sources[0] = "unknown"
	if _, e := Open(c); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	c = config(t)
	c.API.Token = ""
	if _, e := Open(c); !errors.Is(e, apiclient.ErrConfig) {
		t.Fatal(e)
	}
	if (&Registry{}).Close() == nil {
		t.Fatal("incomplete")
	}
	if (*Registry)(nil).Close() != nil {
		t.Fatal("nil")
	}
}
