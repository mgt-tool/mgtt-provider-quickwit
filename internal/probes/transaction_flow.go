package probes

import (
	"context"
	"fmt"

	"github.com/mgt-tool/mgtt-provider-quickwit/internal/quickwitclient"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

// transaction_flow checks that flows starting with `start_span` reach their
// terminal `end_span` at an acceptable rate. Quickwit cannot perform a true
// per-trace join across the index in a single search, so completion_rate is
// computed as the ratio of terminal-stage events to start-stage events in the
// 5-minute window. This is correct in steady state and conservative on
// boundaries (some in-flight flows started in-window won't have terminated yet).
//
// Variables (passed via --start_span / --end_span / --service_name):
//   - start_span (required): OTEL span name of the first observable stage
//   - end_span (required):   OTEL span name of the terminal stage
//   - service_name (optional): scopes both queries to one service
func registerTransactionFlow(r *provider.Registry) {
	r.Register("transaction_flow", map[string]provider.ProbeFn{
		"started_count_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := flowStartQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			res, err := c.Search(ctx, quickwitclient.Window5m(q, nil))
			if err != nil {
				return provider.Result{}, err
			}
			return provider.IntResult(res.NumHits), nil
		},

		"completed_count_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := flowEndQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			res, err := c.Search(ctx, quickwitclient.Window5m(q, nil))
			if err != nil {
				return provider.Result{}, err
			}
			return provider.IntResult(res.NumHits), nil
		},

		"completion_rate_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, startQ, err := flowStartQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			_, endQ, err := flowEndQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			startRes, err := c.Search(ctx, quickwitclient.Window5m(startQ, nil))
			if err != nil {
				return provider.Result{}, err
			}
			endRes, err := c.Search(ctx, quickwitclient.Window5m(endQ, nil))
			if err != nil {
				return provider.Result{}, err
			}
			return provider.FloatResult(safeRatio(float64(endRes.NumHits), float64(startRes.NumHits))), nil
		},

		"p99_lag_ms": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := flowEndQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			aggs := map[string]any{
				"p99": quickwitclient.PercentileAgg("span_duration_millis", 99),
			}
			res, err := c.Search(ctx, quickwitclient.Window5m(q, aggs))
			if err != nil {
				return provider.Result{}, err
			}
			v, ok := percentileFromAggs(res.Aggregations, "p99", 99)
			if !ok {
				return provider.FloatResult(0), nil
			}
			return provider.FloatResult(v), nil
		},
	})
}

// flowStartQuery builds the query selecting the start-stage spans.
func flowStartQuery(req provider.Request) (*quickwitclient.Client, string, error) {
	c, err := newClient(req)
	if err != nil {
		return nil, "", err
	}
	span, err := requireExtra(req, "start_span", "transaction_flow")
	if err != nil {
		return nil, "", err
	}
	q := filterClause(
		fmt.Sprintf("span_name:%s", quoteTerm(span)),
		serviceNameClause(req),
	)
	return c, q, nil
}

// flowEndQuery builds the query selecting the end-stage spans.
func flowEndQuery(req provider.Request) (*quickwitclient.Client, string, error) {
	c, err := newClient(req)
	if err != nil {
		return nil, "", err
	}
	span, err := requireExtra(req, "end_span", "transaction_flow")
	if err != nil {
		return nil, "", err
	}
	q := filterClause(
		fmt.Sprintf("span_name:%s", quoteTerm(span)),
		serviceNameClause(req),
	)
	return c, q, nil
}

// serviceNameClause is shared by all flow probes; empty if user didn't pass one.
func serviceNameClause(req provider.Request) string {
	if s := req.Extra["service_name"]; s != "" {
		return fmt.Sprintf("service_name:%s", quoteTerm(s))
	}
	return ""
}
