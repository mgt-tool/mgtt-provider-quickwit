// Package probes implements the quickwit-provider probe surface. All plumbing
// (argv parsing, exit codes, status:not_found translation) lives in the SDK;
// this package only constructs Quickwit search bodies and parses responses.
package probes

import (
	"fmt"
	"strings"

	"github.com/mgt-tool/mgtt-provider-quickwit/internal/quickwitclient"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

// NewQuickwitConstructor is overridable for tests.
var NewQuickwitConstructor = func(baseURL, indexID, token string) *quickwitclient.Client {
	return quickwitclient.New(baseURL, indexID, token)
}

// newClient constructs a Quickwit HTTP client from request extras.
func newClient(req provider.Request) (*quickwitclient.Client, error) {
	url := req.Extra["quickwit_url"]
	if url == "" {
		return nil, fmt.Errorf("%w: quickwit provider requires --quickwit_url <url>", provider.ErrUsage)
	}
	return NewQuickwitConstructor(url, req.Extra["index_id"], req.Extra["auth_token"]), nil
}

// requireExtra returns the value of `key`, or an ErrUsage if missing.
func requireExtra(req provider.Request, key, typeName string) (string, error) {
	if v := req.Extra[key]; v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%w: tracing.%s requires --%s <value>",
		provider.ErrUsage, typeName, key)
}

// quoteTerm escapes the characters Quickwit's query DSL treats specially so
// span names like `checkout.init` and service names containing dashes are
// passed through as a single term.
func quoteTerm(s string) string {
	// Quickwit accepts double-quoted phrases for exact matches.
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// filterClause joins non-empty `field:value` clauses with AND.
func filterClause(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " AND ")
}

// percentileFromAggs reads the named percentile out of a `percentiles` agg
// response. Quickwit's shape is {"values": {"99.0": 123.4}} or sometimes
// {"values": {"99": 123.4}} — try both.
func percentileFromAggs(aggs map[string]any, name string, percent float64) (float64, bool) {
	a, ok := aggs[name].(map[string]any)
	if !ok {
		return 0, false
	}
	values, ok := a["values"].(map[string]any)
	if !ok {
		return 0, false
	}
	keys := []string{
		fmt.Sprintf("%.1f", percent),
		fmt.Sprintf("%g", percent),
	}
	for _, k := range keys {
		if v, ok := values[k]; ok {
			if f, ok := toFloat(v); ok {
				return f, true
			}
		}
	}
	return 0, false
}

// termsBucketCount returns the number of buckets returned by a `terms` agg —
// used as an approximation for cardinality. Capped by the agg's `size`.
func termsBucketCount(aggs map[string]any, name string) int {
	a, ok := aggs[name].(map[string]any)
	if !ok {
		return 0
	}
	buckets, ok := a["buckets"].([]any)
	if !ok {
		return 0
	}
	return len(buckets)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// safeRatio returns a/b, clamped to [0, 1]. b==0 → 0.
func safeRatio(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	r := a / b
	switch {
	case r < 0:
		return 0
	case r > 1:
		return 1
	}
	return r
}

// Register adds the quickwit provider's types to the registry.
func Register(r *provider.Registry) {
	registerTransactionFlow(r)
	registerAsyncHop(r)
	registerConsumerHealth(r)
}
