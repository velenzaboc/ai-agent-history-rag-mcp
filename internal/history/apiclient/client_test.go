package apiclient

import (
	"context"
	"errors"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/ingest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConfig(t *testing.T) {
	if _, e := New(Config{}); !errors.Is(e, ErrConfig) {
		t.Fatal(e)
	}
	if _, e := New(Config{Endpoint: "http://x", Token: "x", Timeout: time.Second}); !errors.Is(e, ErrConfig) {
		t.Fatal(e)
	}
}
func TestUploadResponses(t *testing.T) {
	p := ingest.EncodedRequest{}
	_ = p
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal(r.Header)
		}
		w.Write([]byte(`{"record_id":"","accepted":true}`))
	}))
	defer server.Close()
	c, e := New(Config{Endpoint: server.URL, Token: "token", Timeout: time.Second, Transport: server.Client().Transport})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Upload(context.Background(), p); e != nil {
		t.Fatal(e)
	}
}

func TestUploadRejectsResponsesAndClassifiesRetry(t *testing.T) {
	for name, response := range map[string]*http.Response{
		"retry":    {StatusCode: 503, Body: io.NopCloser(strings.NewReader("down")), Header: make(http.Header)},
		"status":   {StatusCode: 400, Body: io.NopCloser(strings.NewReader("bad")), Header: make(http.Header)},
		"bad_json": {StatusCode: 200, Body: io.NopCloser(strings.NewReader("{")), Header: make(http.Header)},
		"rejected": {StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"record_id":"","accepted":false}`)), Header: make(http.Header)},
		"large":    {StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", int(MaxResponseBytes)+1))), Header: make(http.Header)},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := New(Config{Endpoint: "https://example.test", Token: "t", Timeout: time.Second, Transport: transport(func(*http.Request) (*http.Response, error) { return response, nil })})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Upload(context.Background(), ingest.EncodedRequest{}); err == nil {
				t.Fatal("expected response failure")
			}
		})
	}
	c, _ := New(Config{Endpoint: "https://example.test", Token: "t", Timeout: time.Second, Transport: transport(func(*http.Request) (*http.Response, error) { return nil, errors.New("network") })})
	if _, err := c.Upload(context.Background(), ingest.EncodedRequest{}); err == nil {
		t.Fatal("network error")
	}
}
