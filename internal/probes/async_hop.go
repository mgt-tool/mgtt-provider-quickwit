package probes

import (
	"context"
	"fmt"

	"github.com/mgt-tool/mgtt-provider-quickwit/internal/quickwitclient"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

// async_hop checks one queue boundary: producer enqueues → consumer dequeues.
// Both spans live in the same trace doc index. We measure the *consumer* side
// (since the publish-to-ack lag is bounded by how long the consumer span takes
// to start) and compare counts to the producer side.
//
// Variables:
//   - producer_span (required): OTEL span name on the publisher side
//   - consumer_span (required): OTEL span name on the consumer side
//   - service_name (optional): scopes consumer query to one service pool
func registerAsyncHop(r *provider.Registry) {
	r.Register("async_hop", map[string]provider.ProbeFn{
		"producer_count_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := producerQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			res, err := c.Search(ctx, quickwitclient.Window5m(q, nil))
			if err != nil {
				return provider.Result{}, err
			}
			return provider.IntResult(res.NumHits), nil
		},

		"consumer_count_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := consumerQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			res, err := c.Search(ctx, quickwitclient.Window5m(q, nil))
			if err != nil {
				return provider.Result{}, err
			}
			return provider.IntResult(res.NumHits), nil
		},

		"success_rate_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, prodQ, err := producerQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			_, consQ, err := consumerQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			prod, err := c.Search(ctx, quickwitclient.Window5m(prodQ, nil))
			if err != nil {
				return provider.Result{}, err
			}
			cons, err := c.Search(ctx, quickwitclient.Window5m(consQ, nil))
			if err != nil {
				return provider.Result{}, err
			}
			return provider.FloatResult(safeRatio(float64(cons.NumHits), float64(prod.NumHits))), nil
		},

		"consumer_error_rate_5m": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := consumerQuery(req)
			if err != nil {
				return provider.Result{}, err
			}
			errQ := q + " AND span_status_code:2"
			total, err := c.Search(ctx, quickwitclient.Window5m(q, nil))
			if err != nil {
				return provider.Result{}, err
			}
			if total.NumHits == 0 {
				return provider.FloatResult(0), nil
			}
			errRes, err := c.Search(ctx, quickwitclient.Window5m(errQ, nil))
			if err != nil {
				return provider.Result{}, err
			}
			return provider.FloatResult(safeRatio(float64(errRes.NumHits), float64(total.NumHits))), nil
		},

		"p99_consumer_duration_ms": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			c, q, err := consumerQuery(req)
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

func producerQuery(req provider.Request) (*quickwitclient.Client, string, error) {
	c, err := newClient(req)
	if err != nil {
		return nil, "", err
	}
	span, err := requireExtra(req, "producer_span", "async_hop")
	if err != nil {
		return nil, "", err
	}
	return c, fmt.Sprintf("span_name:%s", quoteTerm(span)), nil
}

func consumerQuery(req provider.Request) (*quickwitclient.Client, string, error) {
	c, err := newClient(req)
	if err != nil {
		return nil, "", err
	}
	span, err := requireExtra(req, "consumer_span", "async_hop")
	if err != nil {
		return nil, "", err
	}
	q := filterClause(
		fmt.Sprintf("span_name:%s", quoteTerm(span)),
		serviceNameClause(req),
	)
	return c, q, nil
}
