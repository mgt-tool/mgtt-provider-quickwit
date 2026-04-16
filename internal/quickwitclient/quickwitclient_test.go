package quickwitclient

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mgt-tool/mgtt/sdk/provider"
)

type fakeRT struct {
	status int
	body   string
	err    error
}

func (f fakeRT) do(req *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
	}, nil
}

func TestSearch_HappyPath(t *testing.T) {
	c := New("http://qw:7280", "", "")
	c.Do = fakeRT{
		status: 200,
		body: `{"num_hits":42,"hits":[],"aggregations":{"p99":{"values":{"99.0":1234.5}}}}`,
	}.do
	res, err := c.Search(t.Context(), Window5m(`span_name:checkout.init`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.NumHits != 42 {
		t.Fatalf("want num_hits=42, got %d", res.NumHits)
	}
	if c.IndexID != defaultIndex {
		t.Fatalf("want default index %q, got %q", defaultIndex, c.IndexID)
	}
}

func TestSearch_HTTPCodes(t *testing.T) {
	cases := map[int]error{
		401: provider.ErrForbidden,
		403: provider.ErrForbidden,
		404: provider.ErrNotFound,
		400: provider.ErrUsage,
		500: provider.ErrTransient,
		503: provider.ErrTransient,
	}
	for code, want := range cases {
		c := New("http://qw:7280", "", "")
		c.Do = fakeRT{status: code, body: "error body"}.do
		_, err := c.Search(t.Context(), Window5m("x", nil))
		if !errors.Is(err, want) {
			t.Errorf("HTTP %d: want %v, got %v", code, want, err)
		}
	}
}

func TestSearch_TransportErrors(t *testing.T) {
	cases := []string{
		"dial tcp: lookup quickwit: no such host",
		"connection refused",
		"context deadline exceeded",
		"TLS handshake timeout",
	}
	for _, msg := range cases {
		c := New("http://qw:7280", "", "")
		c.Do = fakeRT{err: errors.New(msg)}.do
		_, err := c.Search(t.Context(), Window5m("x", nil))
		if !errors.Is(err, provider.ErrTransient) {
			t.Errorf("transport %q: want ErrTransient, got %v", msg, err)
		}
	}
}

func TestSearch_BadJSON(t *testing.T) {
	c := New("http://qw:7280", "", "")
	c.Do = fakeRT{status: 200, body: "not json"}.do
	_, err := c.Search(t.Context(), Window5m("x", nil))
	if !errors.Is(err, provider.ErrProtocol) {
		t.Fatalf("want ErrProtocol, got %v", err)
	}
}

func TestAuthHeader(t *testing.T) {
	var seenAuth string
	c := New("http://qw:7280", "custom-index", "secret-token")
	c.Do = func(req *http.Request) (*http.Response, error) {
		seenAuth = req.Header.Get("Authorization")
		if !strings.Contains(req.URL.Path, "custom-index") {
			t.Errorf("expected custom-index in path, got %s", req.URL.Path)
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"num_hits":0,"hits":[]}`)),
		}, nil
	}
	_, _ = c.Search(t.Context(), Window5m("x", nil))
	if seenAuth != "Bearer secret-token" {
		t.Errorf("auth header missing or wrong: %q", seenAuth)
	}
}
