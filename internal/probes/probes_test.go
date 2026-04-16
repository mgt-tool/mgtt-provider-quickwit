package probes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mgt-tool/mgtt-provider-quickwit/internal/quickwitclient"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

// fakeQuickwit replaces NewQuickwitConstructor for the duration of one test.
// `bodies` is a slice of response bodies served in order — one per Search call.
func fakeQuickwit(t *testing.T, status int, bodies ...string) {
	t.Helper()
	prev := NewQuickwitConstructor
	t.Cleanup(func() { NewQuickwitConstructor = prev })
	idx := 0
	NewQuickwitConstructor = func(_, _, _ string) *quickwitclient.Client {
		c := quickwitclient.New("http://stub", "", "")
		c.Do = func(req *http.Request) (*http.Response, error) {
			body := bodies[0]
			if idx < len(bodies) {
				body = bodies[idx]
			} else {
				body = bodies[len(bodies)-1]
			}
			idx++
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}
		return c
	}
}

func runProbe(t *testing.T, typeName, fact string, extra map[string]string) provider.Result {
	t.Helper()
	r := provider.NewRegistry()
	Register(r)
	res, err := r.Probe(context.Background(), provider.Request{
		Type: typeName, Name: "test", Fact: fact, Extra: extra,
	})
	if err != nil {
		t.Fatalf("probe %s/%s: %v", typeName, fact, err)
	}
	return res
}

func runProbeErr(t *testing.T, typeName, fact string, extra map[string]string) error {
	t.Helper()
	r := provider.NewRegistry()
	Register(r)
	_, err := r.Probe(context.Background(), provider.Request{
		Type: typeName, Name: "test", Fact: fact, Extra: extra,
	})
	return err
}

// ---------------------------------------------------------------------------
// transaction_flow
// ---------------------------------------------------------------------------

func flowExtras() map[string]string {
	return map[string]string{
		"quickwit_url": "http://stub",
		"start_span":   "order.placed",
		"end_span":     "email.confirmation.sent",
	}
}

func TestTransactionFlow_StartedCount(t *testing.T) {
	fakeQuickwit(t, 200, `{"num_hits":42,"hits":[]}`)
	res := runProbe(t, "transaction_flow", "started_count_5m", flowExtras())
	if v, _ := res.Value.(int); v != 42 {
		t.Fatalf("want 42, got %v", res.Value)
	}
}

func TestTransactionFlow_CompletionRate(t *testing.T) {
	// First search: started=100, second: completed=90 → ratio 0.9
	fakeQuickwit(t, 200,
		`{"num_hits":100,"hits":[]}`,
		`{"num_hits":90,"hits":[]}`,
	)
	res := runProbe(t, "transaction_flow", "completion_rate_5m", flowExtras())
	if v, _ := res.Value.(float64); v != 0.9 {
		t.Fatalf("want 0.9, got %v", res.Value)
	}
}

func TestTransactionFlow_CompletionRate_ZeroStartedReturnsZero(t *testing.T) {
	fakeQuickwit(t, 200,
		`{"num_hits":0,"hits":[]}`,
		`{"num_hits":5,"hits":[]}`,
	)
	res := runProbe(t, "transaction_flow", "completion_rate_5m", flowExtras())
	if v, _ := res.Value.(float64); v != 0 {
		t.Fatalf("zero started → 0, got %v", res.Value)
	}
}

func TestTransactionFlow_CompletionRate_ClampedToOne(t *testing.T) {
	// More completions than starts (boundary effect) — must clamp to 1.0.
	fakeQuickwit(t, 200,
		`{"num_hits":10,"hits":[]}`,
		`{"num_hits":12,"hits":[]}`,
	)
	res := runProbe(t, "transaction_flow", "completion_rate_5m", flowExtras())
	if v, _ := res.Value.(float64); v != 1.0 {
		t.Fatalf("want 1.0 (clamped), got %v", res.Value)
	}
}

func TestTransactionFlow_P99LagMs(t *testing.T) {
	fakeQuickwit(t, 200,
		`{"num_hits":1,"hits":[],"aggregations":{"p99":{"values":{"99.0":1234.5}}}}`,
	)
	res := runProbe(t, "transaction_flow", "p99_lag_ms", flowExtras())
	if v, _ := res.Value.(float64); v != 1234.5 {
		t.Fatalf("want 1234.5, got %v", res.Value)
	}
}

func TestTransactionFlow_P99LagMs_NoData(t *testing.T) {
	fakeQuickwit(t, 200, `{"num_hits":0,"hits":[]}`)
	res := runProbe(t, "transaction_flow", "p99_lag_ms", flowExtras())
	if v, _ := res.Value.(float64); v != 0 {
		t.Fatalf("no data → 0, got %v", res.Value)
	}
}

func TestTransactionFlow_MissingStartSpan_ErrUsage(t *testing.T) {
	err := runProbeErr(t, "transaction_flow", "started_count_5m", map[string]string{
		"quickwit_url": "http://stub",
	})
	if !errors.Is(err, provider.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// async_hop
// ---------------------------------------------------------------------------

func hopExtras() map[string]string {
	return map[string]string{
		"quickwit_url":  "http://stub",
		"producer_span": "queue.publish",
		"consumer_span": "queue.consume",
	}
}

func TestAsyncHop_SuccessRate(t *testing.T) {
	fakeQuickwit(t, 200,
		`{"num_hits":50,"hits":[]}`,  // producer
		`{"num_hits":48,"hits":[]}`,  // consumer
	)
	res := runProbe(t, "async_hop", "success_rate_5m", hopExtras())
	if v, _ := res.Value.(float64); v != 48.0/50.0 {
		t.Fatalf("want 0.96, got %v", res.Value)
	}
}

func TestAsyncHop_ErrorRate(t *testing.T) {
	fakeQuickwit(t, 200,
		`{"num_hits":100,"hits":[]}`,
		`{"num_hits":3,"hits":[]}`,
	)
	res := runProbe(t, "async_hop", "consumer_error_rate_5m", hopExtras())
	if v, _ := res.Value.(float64); v != 0.03 {
		t.Fatalf("want 0.03, got %v", res.Value)
	}
}

func TestAsyncHop_ErrorRate_NoTraffic(t *testing.T) {
	fakeQuickwit(t, 200, `{"num_hits":0,"hits":[]}`)
	res := runProbe(t, "async_hop", "consumer_error_rate_5m", hopExtras())
	if v, _ := res.Value.(float64); v != 0 {
		t.Fatalf("no traffic → 0, got %v", res.Value)
	}
}

func TestAsyncHop_MissingProducerSpan_ErrUsage(t *testing.T) {
	err := runProbeErr(t, "async_hop", "producer_count_5m", map[string]string{
		"quickwit_url": "http://stub",
	})
	if !errors.Is(err, provider.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// consumer_health
// ---------------------------------------------------------------------------

func chExtras() map[string]string {
	return map[string]string{
		"quickwit_url":  "http://stub",
		"consumer_span": "worker.process",
	}
}

func TestConsumerHealth_Throughput(t *testing.T) {
	fakeQuickwit(t, 200, `{"num_hits":300,"hits":[]}`)
	res := runProbe(t, "consumer_health", "throughput_per_min", chExtras())
	// 300 spans / 5 minutes = 60.0 per minute
	if v, _ := res.Value.(float64); v != 60.0 {
		t.Fatalf("want 60.0, got %v", res.Value)
	}
}

func TestConsumerHealth_WorkersActive_FromTermsBuckets(t *testing.T) {
	fakeQuickwit(t, 200,
		`{"num_hits":100,"hits":[],"aggregations":{"workers":{"buckets":[
			{"key":"consumer-blue","doc_count":60},
			{"key":"consumer-green","doc_count":40}
		]}}}`,
	)
	res := runProbe(t, "consumer_health", "workers_active", chExtras())
	if v, _ := res.Value.(int); v != 2 {
		t.Fatalf("want 2 distinct service buckets, got %v", res.Value)
	}
}

func TestConsumerHealth_WorkersActive_FallbackOnMissingAgg(t *testing.T) {
	// No aggregations key in response, but hits present → fall back to 1.
	fakeQuickwit(t, 200, `{"num_hits":5,"hits":[]}`)
	res := runProbe(t, "consumer_health", "workers_active", chExtras())
	if v, _ := res.Value.(int); v != 1 {
		t.Fatalf("want 1 fallback, got %v", res.Value)
	}
}

func TestConsumerHealth_WorkersActive_NoSpansZero(t *testing.T) {
	fakeQuickwit(t, 200, `{"num_hits":0,"hits":[]}`)
	res := runProbe(t, "consumer_health", "workers_active", chExtras())
	if v, _ := res.Value.(int); v != 0 {
		t.Fatalf("no spans → 0, got %v", res.Value)
	}
}

func TestConsumerHealth_MissingConsumerSpan_ErrUsage(t *testing.T) {
	err := runProbeErr(t, "consumer_health", "throughput_per_min", map[string]string{
		"quickwit_url": "http://stub",
	})
	if !errors.Is(err, provider.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Registry wiring
// ---------------------------------------------------------------------------

func TestRegistryWiresAllTypes(t *testing.T) {
	r := provider.NewRegistry()
	Register(r)

	wantFacts := map[string][]string{
		"transaction_flow": {"started_count_5m", "completed_count_5m", "completion_rate_5m", "p99_lag_ms"},
		"async_hop":        {"producer_count_5m", "consumer_count_5m", "success_rate_5m", "consumer_error_rate_5m", "p99_consumer_duration_ms"},
		"consumer_health":  {"processed_count_5m", "throughput_per_min", "p99_processing_ms", "error_rate_5m", "workers_active"},
	}
	for typeName, facts := range wantFacts {
		got := r.Facts(typeName)
		gotSet := map[string]bool{}
		for _, f := range got {
			gotSet[f] = true
		}
		for _, w := range facts {
			if !gotSet[w] {
				t.Errorf("registry missing %s/%s", typeName, w)
			}
		}
	}
}
