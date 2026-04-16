// Package quickwitclient wraps Quickwit's search API with timeout, auth, and
// the classify-to-sentinel-error mapping used by every quickwit-provider probe.
package quickwitclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mgt-tool/mgtt/sdk/provider"
)

const (
	defaultTimeout = 30 * time.Second
	defaultIndex   = "otel-traces-v0_7"
	maxBytes       = 10 * 1024 * 1024
)

// Client is a thin Quickwit HTTP wrapper. Tests inject Do for fakes.
type Client struct {
	BaseURL string
	IndexID string
	Token   string
	Timeout time.Duration
	Do      func(req *http.Request) (*http.Response, error)
}

// New returns a Client with sensible defaults. indexID may be empty to use
// Quickwit's standard OTEL traces index.
func New(baseURL, indexID, token string) *Client {
	if indexID == "" {
		indexID = defaultIndex
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		IndexID: indexID,
		Token:   token,
		Timeout: defaultTimeout,
		Do:      (&http.Client{Timeout: defaultTimeout}).Do,
	}
}

// SearchRequest mirrors Quickwit's JSON search body. Only fields used by this
// provider are modeled.
type SearchRequest struct {
	Query          string         `json:"query"`
	StartTimestamp int64          `json:"start_timestamp,omitempty"`
	EndTimestamp   int64          `json:"end_timestamp,omitempty"`
	MaxHits        int            `json:"max_hits"`
	Aggs           map[string]any `json:"aggs,omitempty"`
}

// SearchResponse mirrors the relevant fragment of Quickwit's response.
type SearchResponse struct {
	NumHits      int             `json:"num_hits"`
	Hits         []json.RawMessage `json:"hits"`
	Aggregations map[string]any  `json:"aggregations"`
}

// Search runs a search against the configured index and returns the parsed
// response.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal quickwit request: %v", provider.ErrProtocol, err)
	}
	url := c.BaseURL + "/api/v1/" + c.IndexID + "/search"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", provider.ErrEnv, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.Do(httpReq)
	if err != nil {
		return nil, classifyTransport(err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read quickwit response: %v", provider.ErrTransient, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTP(resp.StatusCode, respBody)
	}
	var out SearchResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("%w: parse quickwit json: %v", provider.ErrProtocol, err)
	}
	return &out, nil
}

// Window5m builds a SearchRequest for the trailing 5-minute window.
// span timestamps in OTEL trace docs are nanoseconds; Quickwit's
// start_timestamp/end_timestamp are seconds.
func Window5m(query string, aggs map[string]any) SearchRequest {
	now := time.Now()
	return SearchRequest{
		Query:          query,
		StartTimestamp: now.Add(-5 * time.Minute).Unix(),
		EndTimestamp:   now.Unix(),
		MaxHits:        0,
		Aggs:           aggs,
	}
}

// PercentileAgg builds a `percentiles` agg over the named field.
func PercentileAgg(field string, percents ...float64) map[string]any {
	return map[string]any{
		"percentiles": map[string]any{
			"field":    field,
			"percents": percents,
		},
	}
}

// TermsAgg builds a `terms` agg over the named field with the given size.
func TermsAgg(field string, size int) map[string]any {
	return map[string]any{
		"terms": map[string]any{
			"field": field,
			"size":  size,
		},
	}
}

// classifyTransport maps net errors to sentinel errors.
func classifyTransport(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "TLS handshake timeout"):
		return fmt.Errorf("%w: %s", provider.ErrTransient, msg)
	}
	return fmt.Errorf("%w: %s", provider.ErrEnv, msg)
}

// classifyHTTP maps Quickwit HTTP error codes to sentinel errors.
func classifyHTTP(status int, body []byte) error {
	first := firstLine(string(body))
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: quickwit HTTP %d: %s", provider.ErrForbidden, status, first)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: quickwit HTTP %d: %s", provider.ErrNotFound, status, first)
	case status == http.StatusBadRequest:
		return fmt.Errorf("%w: quickwit rejected query: %s", provider.ErrUsage, first)
	case status >= 500 && status < 600:
		return fmt.Errorf("%w: quickwit HTTP %d: %s", provider.ErrTransient, status, first)
	}
	return fmt.Errorf("%w: quickwit HTTP %d: %s", provider.ErrEnv, status, first)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
