// Package apiclient implements the bounded remote ingestion protocol boundary.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/velenzaboc/ai-agent-history-rag-mcp/internal/history/ingest"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxResponseBytes = int64(1 << 20)

var (
	ErrConfig   = errors.New("invalid API client config")
	ErrResponse = errors.New("invalid API response")
)

type Config struct {
	Endpoint, Token string
	Timeout         time.Duration
	Transport       http.RoundTripper
}
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}
type Result struct {
	RecordID string `json:"record_id"`
	Accepted bool   `json:"accepted"`
}

func New(c Config) (*Client, error) {
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" || c.Token == "" || c.Timeout <= 0 {
		return nil, ErrConfig
	}
	t := c.Transport
	if t == nil {
		t = http.DefaultTransport
	}
	return &Client{strings.TrimRight(c.Endpoint, "/"), c.Token, &http.Client{Timeout: c.Timeout, Transport: t}}, nil
}
func (c *Client) Upload(ctx context.Context, p ingest.EncodedRequest) (Result, error) {
	if c == nil {
		return Result{}, ErrConfig
	}
	body := p.Body()
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/history/ingest", bytes.NewReader(body))
	if e != nil {
		return Result{}, e
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", p.RecordID())
	r.Header.Set("X-Request-SHA256", fmt.Sprintf("%x", p.RequestSHA256()))
	resp, e := c.http.Do(r)
	if e != nil {
		return Result{}, e
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if e != nil {
		return Result{}, e
	}
	if len(data) > int(MaxResponseBytes) {
		return Result{}, ErrResponse
	}
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return Result{}, fmt.Errorf("retryable HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out Result
	if json.Unmarshal(data, &out) != nil || !out.Accepted || out.RecordID != p.RecordID() {
		return Result{}, ErrResponse
	}
	return out, nil
}
