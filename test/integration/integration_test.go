//go:build integration

// Package integration exercises mgtt-provider-quickwit end-to-end against a
// real Quickwit instance. Quickwit runs in docker; trace docs are pushed via
// Quickwit's ingest API. The provider binary is built fresh each run.
//
// Run with:
//
//	go test -tags=integration ./test/integration/...
//
// Requirements on the host: docker, go. Tests are skipped when docker is
// unavailable.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	containerName  = "mgtt-provider-quickwit-it"
	// Pinned by digest. The same lesson the tempo provider learned the hard
	// way: `:0.8.2` can be re-rolled with breaking aggregation-shape changes.
	quickwitImage = "quickwit/quickwit:0.8.2@sha256:363ff56ce45614e46eba1c308e420f56a9f2fd8ab5788cbca0ec6b68a2e0ef92"
	quickwitPort   = "7280"
	quickwitIndex  = "traces-it"
)

var quickwitBaseURL = "http://localhost:" + quickwitPort

// ---------------------------------------------------------------------------
// Test lifecycle
// ---------------------------------------------------------------------------

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "docker not on PATH; skipping quickwit integration tests")
		os.Exit(0)
	}
	if err := ensureQuickwit(); err != nil {
		panic("ensureQuickwit: " + err.Error())
	}
	if err := waitForReady(2 * time.Minute); err != nil {
		panic("waitForReady: " + err.Error())
	}
	if err := ensureIndex(); err != nil {
		panic("ensureIndex: " + err.Error())
	}
	code := m.Run()
	// Container is preserved across runs for iteration speed; destroy with:
	//   docker rm -f mgtt-provider-quickwit-it
	os.Exit(code)
}

func ensureQuickwit() error {
	out, _ := exec.Command("docker", "ps", "--filter", "name=^"+containerName+"$",
		"--format", "{{.Names}}").Output()
	if strings.Contains(string(out), containerName) {
		return nil
	}
	exec.Command("docker", "rm", "-f", containerName).Run()

	cmd := exec.Command("docker", "run", "-d",
		"--name", containerName,
		"-p", quickwitPort+":7280",
		quickwitImage,
		"run",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func waitForReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(quickwitBaseURL + "/health/livez")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				time.Sleep(1 * time.Second)
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("quickwit did not become ready within %s", timeout)
}

func ensureIndex() error {
	// If index exists, delete it for a clean slate (test data accumulates
	// across runs and would skew counts otherwise).
	exec.Command("curl", "-sS", "-X", "DELETE",
		quickwitBaseURL+"/api/v1/indexes/"+quickwitIndex).Run()

	cfgPath, err := filepath.Abs(filepath.Join("testdata", "test_index.yaml"))
	if err != nil {
		return err
	}
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost,
		quickwitBaseURL+"/api/v1/indexes",
		bytes.NewReader(cfgBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create index: HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Trace doc emitter
// ---------------------------------------------------------------------------

type spanDoc struct {
	Timestamp        int64  `json:"timestamp"`
	TraceID          string `json:"trace_id"`
	SpanID           string `json:"span_id"`
	SpanName         string `json:"span_name"`
	ServiceName      string `json:"service_name"`
	SpanStatusCode   int    `json:"span_status_code"`
	SpanDurationMS   int    `json:"span_duration_millis"`
}

// pushSpans emits n synthetic spans. status: 0=unset, 1=ok, 2=error.
// commit=force forces a commit so the docs are searchable immediately.
func pushSpans(t *testing.T, name, service string, n, durationMs, status int) {
	t.Helper()
	now := time.Now().Unix()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := 0; i < n; i++ {
		_ = enc.Encode(spanDoc{
			Timestamp:      now,
			TraceID:        randomHex(16),
			SpanID:         randomHex(8),
			SpanName:       name,
			ServiceName:    service,
			SpanStatusCode: status,
			SpanDurationMS: durationMs,
		})
	}
	url := quickwitBaseURL + "/api/v1/" + quickwitIndex + "/ingest?commit=force"
	resp, err := http.Post(url, "application/x-ndjson", &buf)
	if err != nil {
		t.Fatalf("push spans: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("ingest: HTTP %d: %s", resp.StatusCode, body)
	}
	// Quickwit's commit takes a moment to propagate to searches.
	time.Sleep(2 * time.Second)
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Provider binary harness
// ---------------------------------------------------------------------------

func buildProviderBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "mgtt-provider-quickwit")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build provider: %v\n%s", err, out)
	}
	return bin
}

type probeResult struct {
	Value  any    `json:"value"`
	Raw    string `json:"raw"`
	Status string `json:"status"`
}

func probe(t *testing.T, binary, typeName, fact string, extras ...string) probeResult {
	t.Helper()
	args := []string{"probe", "test_inv", fact, "--type", typeName}
	args = append(args, extras...)
	cmd := exec.Command(binary, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe %s/%s extras=%v: %v\nstderr: %s", typeName, fact, extras, err, stderr.String())
	}
	var r probeResult
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("decode probe output: %v (raw=%q)", err, out)
	}
	return r
}

func probeAllowFail(t *testing.T, binary string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run provider: %v", err)
	}
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), code
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		// Fall back to test file location for pre-init runs.
		wd, _ := os.Getwd()
		return filepath.Join(wd, "..", "..")
	}
	return strings.TrimSpace(string(out))
}

// uniqueSpanName returns a span name unique to this test run so concurrent
// tests don't see each other's spans (Quickwit container is shared).
func uniqueSpanName(t *testing.T, prefix string) string {
	t.Helper()
	return prefix + "." + randomHex(4)
}

func quickwitFlags(extras ...string) []string {
	return append([]string{
		"--quickwit_url", quickwitBaseURL,
		"--index_id", quickwitIndex,
	}, extras...)
}

// ---------------------------------------------------------------------------
// Scenario 1 — transaction_flow happy path: matching start/end counts
// ---------------------------------------------------------------------------

func TestScenario_TransactionFlow_Healthy(t *testing.T) {
	start := uniqueSpanName(t, "order.placed")
	end := uniqueSpanName(t, "email.sent")
	pushSpans(t, start, "magento-web", 50, 100, 1)
	pushSpans(t, end, "magento-consumer", 48, 200, 1) // 96% completion

	binary := buildProviderBinary(t)
	args := quickwitFlags("--start_span", start, "--end_span", end)

	t.Run("started_count_5m", func(t *testing.T) {
		r := probe(t, binary, "transaction_flow", "started_count_5m", args...)
		v, _ := r.Value.(float64)
		if int(v) < 50 {
			t.Fatalf("want >= 50, got %v", r.Value)
		}
	})

	t.Run("completion_rate_5m approximately 0.96", func(t *testing.T) {
		r := probe(t, binary, "transaction_flow", "completion_rate_5m", args...)
		v, _ := r.Value.(float64)
		// Allow a wide band — Quickwit commit ordering can shift counts.
		if v < 0.85 || v > 1.0 {
			t.Fatalf("want completion_rate around 0.96, got %v", v)
		}
	})

	t.Run("p99_lag_ms reflects end-span duration", func(t *testing.T) {
		r := probe(t, binary, "transaction_flow", "p99_lag_ms", args...)
		v, _ := r.Value.(float64)
		// All end spans were 200ms.
		if v < 150 || v > 300 {
			t.Fatalf("want p99 around 200ms, got %v", v)
		}
	})
}

// ---------------------------------------------------------------------------
// Scenario 2 — transaction_flow broken: start fires but end never does
// ---------------------------------------------------------------------------

func TestScenario_TransactionFlow_Broken(t *testing.T) {
	start := uniqueSpanName(t, "broken.start")
	end := uniqueSpanName(t, "broken.end") // never pushed
	pushSpans(t, start, "magento-web", 30, 100, 1)

	binary := buildProviderBinary(t)
	args := quickwitFlags("--start_span", start, "--end_span", end)

	r := probe(t, binary, "transaction_flow", "completion_rate_5m", args...)
	v, _ := r.Value.(float64)
	if v != 0 {
		t.Fatalf("no end spans → completion_rate must be 0, got %v", v)
	}
}

// ---------------------------------------------------------------------------
// Scenario 3 — async_hop happy path: producer ≈ consumer
// ---------------------------------------------------------------------------

func TestScenario_AsyncHop_Healthy(t *testing.T) {
	prod := uniqueSpanName(t, "queue.publish")
	cons := uniqueSpanName(t, "queue.consume")
	pushSpans(t, prod, "magento-web", 100, 5, 1)
	pushSpans(t, cons, "magento-consumer", 95, 50, 1)

	binary := buildProviderBinary(t)
	args := quickwitFlags("--producer_span", prod, "--consumer_span", cons)

	r := probe(t, binary, "async_hop", "success_rate_5m", args...)
	v, _ := r.Value.(float64)
	if v < 0.85 || v > 1.0 {
		t.Fatalf("want success_rate around 0.95, got %v", v)
	}
}

// ---------------------------------------------------------------------------
// Scenario 4 — async_hop with errors
// ---------------------------------------------------------------------------

func TestScenario_AsyncHop_ConsumerErrors(t *testing.T) {
	prod := uniqueSpanName(t, "queue.publish")
	cons := uniqueSpanName(t, "queue.consume")
	pushSpans(t, prod, "magento-web", 50, 5, 1)
	pushSpans(t, cons, "magento-consumer", 30, 50, 1) // ok
	pushSpans(t, cons, "magento-consumer", 20, 50, 2) // error

	binary := buildProviderBinary(t)
	args := quickwitFlags("--producer_span", prod, "--consumer_span", cons)

	r := probe(t, binary, "async_hop", "consumer_error_rate_5m", args...)
	v, _ := r.Value.(float64)
	if v < 0.30 || v > 0.50 {
		t.Fatalf("want error_rate around 0.4, got %v", v)
	}
}

// ---------------------------------------------------------------------------
// Scenario 5 — consumer_health: distinct services, throughput
// ---------------------------------------------------------------------------

func TestScenario_ConsumerHealth_TwoWorkers(t *testing.T) {
	span := uniqueSpanName(t, "worker.process")
	pushSpans(t, span, "consumer-blue", 40, 30, 1)
	pushSpans(t, span, "consumer-green", 35, 30, 1)

	binary := buildProviderBinary(t)
	args := quickwitFlags("--consumer_span", span)

	t.Run("workers_active counts distinct services", func(t *testing.T) {
		r := probe(t, binary, "consumer_health", "workers_active", args...)
		v, _ := r.Value.(float64)
		if int(v) < 2 {
			t.Fatalf("want 2 workers, got %v", r.Value)
		}
	})

	t.Run("throughput_per_min reflects total spans / 5", func(t *testing.T) {
		r := probe(t, binary, "consumer_health", "throughput_per_min", args...)
		v, _ := r.Value.(float64)
		// 75 spans / 5 min = 15.0 — but allow for boundary effects.
		if v < 10 || v > 20 {
			t.Fatalf("want throughput around 15, got %v", v)
		}
	})
}

// ---------------------------------------------------------------------------
// Scenario 6 — no data: querying an unknown span name
// ---------------------------------------------------------------------------

func TestScenario_NoData(t *testing.T) {
	binary := buildProviderBinary(t)
	r := probe(t, binary, "transaction_flow", "started_count_5m",
		quickwitFlags(
			"--start_span", "span.never.emitted."+randomHex(4),
			"--end_span", "ignored",
		)...)
	v, _ := r.Value.(float64)
	if int(v) != 0 {
		t.Fatalf("missing span should yield 0, got %v", v)
	}
}

// ---------------------------------------------------------------------------
// Scenario 7 — usage errors must surface as exit 1
// ---------------------------------------------------------------------------

func TestScenario_MissingStartSpan_ErrUsage(t *testing.T) {
	binary := buildProviderBinary(t)
	_, stderr, code := probeAllowFail(t, binary,
		"probe", "x", "started_count_5m",
		"--type", "transaction_flow",
		"--quickwit_url", quickwitBaseURL,
	)
	if code != 1 {
		t.Fatalf("missing --start_span: want exit 1, got %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--start_span") {
		t.Fatalf("error message should mention --start_span: %s", stderr)
	}
}

func TestScenario_MissingQuickwitURL_ErrUsage(t *testing.T) {
	binary := buildProviderBinary(t)
	_, stderr, code := probeAllowFail(t, binary,
		"probe", "x", "started_count_5m",
		"--type", "transaction_flow",
		"--start_span", "irrelevant",
		"--end_span", "irrelevant",
	)
	if code != 1 {
		t.Fatalf("missing --quickwit_url: want exit 1, got %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--quickwit_url") {
		t.Fatalf("error message should mention --quickwit_url: %s", stderr)
	}
}

// ---------------------------------------------------------------------------
// Scenario 8 — transient error (unreachable Quickwit) → exit 4
// ---------------------------------------------------------------------------

func TestScenario_UnreachableQuickwit_ErrTransient(t *testing.T) {
	binary := buildProviderBinary(t)
	_, stderr, code := probeAllowFail(t, binary,
		"probe", "x", "started_count_5m",
		"--type", "transaction_flow",
		"--quickwit_url", "http://localhost:1",
		"--start_span", "irrelevant",
		"--end_span", "irrelevant",
	)
	if code != 4 {
		t.Fatalf("unreachable quickwit: want exit 4 (transient), got %d stderr=%s", code, stderr)
	}
}

var _ = context.Background // keep imports
