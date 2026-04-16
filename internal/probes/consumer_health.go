package probes

import (
	"context"
	"fmt"

	"github.com/mgt-tool/mgtt-provider-quickwit/internal/quickwitclient"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

// consumer_health watches a pool of workers (queue consumers, batch processors).
// It surfaces "are workers running, how fast, how clean".
//
// Variables:
//   - consumer_span (required): OTEL span name emitted by each consumer per job
//   - service_name (optional): scopes to one service pool
func registerConsumerHealth(r *provider.Registry) {
	r.Register("consumer_health", map[string]provider.ProbeFn{
		"processed_count_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := consumerHealthQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			res, err := c.Search(ctx, quickwitclient.Window5m(q, nil))
			if err != nil {
				return provider.Result{}, err
			}
			return provider.IntResult(res.NumHits), nil
		},

		"throughput_per_min": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := consumerHealthQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			res, err := c.Search(ctx, quickwitclient.Window5m(q, nil))
			if err != nil {
				return provider.Result{}, err
			}
			return provider.FloatResult(float64(res.NumHits) / 5.0), nil
		},

		"p99_processing_ms": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := consumerHealthQuery(req)
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

		"error_rate_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := consumerHealthQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			total, err := c.Search(ctx, quickwitclient.Window5m(q, nil))
			if err != nil {
				return provider.Result{}, err
			}
			if total.NumHits == 0 {
				return provider.FloatResult(0), nil
			}
			errRes, err := c.Search(ctx, quickwitclient.Window5m(q+" AND span_status_code:2", nil))
			if err != nil {
				return provider.Result{}, err
			}
			return provider.FloatResult(safeRatio(float64(errRes.NumHits), float64(total.NumHits))), nil
		},

		"workers_active": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := consumerHealthQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			// Quickwit lacks a `cardinality` agg, so we count distinct
			// `service_name` buckets via `terms`. size:200 is generous —
			// a single consumer pool rarely has more than a few dozen
			// instances. If you exceed this, the count will plateau.
			aggs := map[string]any{
				"workers": quickwitclient.TermsAgg("service_name", 200),
			}
			res, err := c.Search(ctx, quickwitclient.Window5m(q, aggs))
			if err != nil {
				return provider.Result{}, err
			}
			n := termsBucketCount(res.Aggregations, "workers")
			if n == 0 && res.NumHits > 0 {
				// Fall back: spans exist but agg returned nothing → ≥ 1 worker.
				return provider.IntResult(1), nil
			}
			return provider.IntResult(n), nil
		},
	})
}

func consumerHealthQuery(req provider.Request) (*quickwitclient.Client, string, error) {
	c, err := newClient(req)
	if err != nil {
		return nil, "", err
	}
	span, err := requireExtra(req, "consumer_span", "consumer_health")
	if err != nil {
		return nil, "", err
	}
	q := filterClause(
		fmt.Sprintf("span_name:%s", quoteTerm(span)),
		serviceNameClause(req),
	)
	return c, q, nil
}
