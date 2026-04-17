# mgtt-provider-quickwit

Cross-span tracing checks for [mgtt](https://github.com/mgt-tool/mgtt) backed by [Quickwit](https://quickwit.io). Use for **multi-span business flows**, **queue-hop lag**, and **consumer-pool health** — the things `tracing.span_invariant` (Tempo) can't see because they span more than one span.

```yaml
order_to_email_flow:
  type: tracing.transaction_flow
  providers: [quickwit]
  vars:
    quickwit_url: http://quickwit.observability.svc:7280
    start_span: "checkout.order.placed"
    end_span:   "email.order_confirmation.sent"
```

When `mgtt plan` walks this component, the provider asks Quickwit: "of the orders that were placed in the last 5 minutes, what fraction got their confirmation email?" — and reasons forward from the answer.

## Compatibility

| | |
|---|---|
| **Backend** | Quickwit |
| **Versions** | `0.8.x` |
| **Tested against** | `quickwit/quickwit:0.8.2` (digest pinned in integration tests) |

Quickwit's aggregation surface is version-sensitive — `0.8.x` lacks a `cardinality` agg, so `workers_active` falls back to a `terms` agg (size: 200). Earlier or later Quickwit deployments may behave differently. See [`provider.yaml`](./provider.yaml#L19) for the full contract.

## Install

Two equivalent paths — pick whichever fits your workflow:

```bash
# Git + host toolchain (requires Go 1.25+)
mgtt provider install quickwit

# Pre-built Docker image (no local toolchain, digest-pinned)
mgtt provider install --image ghcr.io/mgt-tool/mgtt-provider-quickwit:0.1.1@sha256:...
```

The image is published by [this repo's CI](./.github/workflows/docker.yml) on every push to `main` and every `v*` tag. Find the current digest on the [GHCR package page](https://github.com/mgt-tool/mgtt-provider-quickwit/pkgs/container/mgtt-provider-quickwit).

## Capabilities

This provider declares **no `needs:` entries** — it talks only HTTP to the Quickwit URL you configure in `vars.quickwit_url`. It does declare **`network: host`** so the container reaches in-cluster DNS (e.g. `quickwit.observability.svc:7280`) that bridge-mode containers can't resolve.

No host credentials are forwarded; Quickwit auth (when fronted by a proxy) is passed per-component via the `auth_token` model var.

Operators can override or extend the vocabulary via `$MGTT_HOME/capabilities.yaml`, and refuse specific caps via `MGTT_IMAGE_CAPS_DENY=...`. See the [full capabilities reference](https://github.com/mgt-tool/mgtt/blob/main/docs/reference/image-capabilities.md). Git-installed invocations don't go through this layer — the binary runs with the operator's full environment.

## Auth

| Variable | Purpose | Required |
|---|---|---|
| `quickwit_url` | Base URL of the Quickwit search API | yes |
| `index_id` | Index that holds OTEL trace docs (default `otel-traces-v0_7`) | no |
| `auth_token` | Bearer token (when Quickwit is fronted by auth) | no |

Probes are HTTP `POST` to `/api/v1/<index>/search` only — the provider never writes to Quickwit.

## Types

### `tracing.transaction_flow`

A business flow that starts with one span and finishes with another, possibly minutes and several services later.

| Fact | Type | Returns |
|---|---|---|
| `started_count_5m` | int | spans named `<start_span>` in the last 5 minutes |
| `completed_count_5m` | int | spans named `<end_span>` in the last 5 minutes |
| `completion_rate_5m` | float (0–1) | `completed / started`, clamped to 1.0 |
| `p99_lag_ms` | float (ms) | p99 of the end-stage span duration |

States: `live` (≥ 95% completion) → `degraded` (70–95%) → `broken` (< 70%).

### `tracing.async_hop`

One queue boundary: producer publishes, consumer dequeues.

| Fact | Type | Returns |
|---|---|---|
| `producer_count_5m` | int | publish-side spans in the window |
| `consumer_count_5m` | int | consume-side spans in the window |
| `success_rate_5m` | float (0–1) | `consumer / producer`, clamped |
| `consumer_error_rate_5m` | float (0–1) | fraction of consumer spans with `status=ERROR` |
| `p99_consumer_duration_ms` | float (ms) | p99 of consumer span duration |

States: `live` → `lagging` (success rate < 95%) → `failing` (error rate ≥ 5%).

### `tracing.consumer_health`

A pool of workers (queue consumers, batch processors, schedulers).

| Fact | Type | Returns |
|---|---|---|
| `processed_count_5m` | int | consumer spans observed in window |
| `throughput_per_min` | float | `processed_count_5m / 5` |
| `p99_processing_ms` | float (ms) | p99 of per-job processing time |
| `error_rate_5m` | float (0–1) | fraction with `status=ERROR` |
| `workers_active` | int | distinct `service_name` values seen in window |

States: `live` (workers > 0, error rate < 1%) → `starved` (workers but no work) → `failing` (error rate ≥ 5%) → `down` (no workers).

## Emitting spans to Quickwit

**Any OpenTelemetry-instrumented service works** — Go, Java, .NET, Python, Node, Rust, PHP, Ruby, anything that speaks OTLP. The provider doesn't care about the language; it queries the Quickwit index where your trace docs land.

### The contract

The provider issues Quickwit search queries against these fields:

| Field | Type | Used for |
|---|---|---|
| `span_name` | text (raw tokenizer) | matches `--start_span`, `--consumer_span`, etc. |
| `service_name` | text (raw tokenizer) | optional `--service_name` scoping; `workers_active` counts distinct values |
| `span_status_code` | i64 | `2` = error |
| `span_duration_millis` | i64 | percentile aggregations |
| `timestamp` | datetime (fast) | the index's timestamp field |

If you ingest via Quickwit's standard `otel-traces-v0_7` index (auto-created by Quickwit's OTEL receiver), these fields exist by default — point `--index_id` at it and you're done. If you maintain your own index, mirror the field names above.

Three things must be true for any of this to work:

1. **Your spans have stable, query-friendly names.** `start_span: "checkout.order.placed"` in YAML must equal what your tracer's `startSpan("checkout.order.placed")` produces. Don't put dynamic IDs in span names.
2. **Status is set on errors.** `span_status_code = 2` is what `error_rate_5m` counts. Most OTEL SDKs do this automatically when you call `setStatus(StatusCode.ERROR)` or when a span exits via an exception handler.
3. **Spans actually arrive at Quickwit.** Set `OTEL_EXPORTER_OTLP_ENDPOINT` to a collector that forwards to Quickwit (or directly to Quickwit's OTLP receiver, port 7281), and `OTEL_SERVICE_NAME` so spans are attributable.

### One-time service setup (any language)

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.observability.svc:4318
OTEL_SERVICE_NAME=<your-service-name>
OTEL_RESOURCE_ATTRIBUTES=deployment.color=blue,service.version=2.4.7-p3
```

Then add the OTEL SDK + OTLP/HTTP exporter for your language. Per-language bootstrap docs:

| Language | OTEL bootstrap doc |
|---|---|
| Go | [opentelemetry.io/docs/languages/go](https://opentelemetry.io/docs/languages/go/) |
| Java | [opentelemetry.io/docs/languages/java](https://opentelemetry.io/docs/languages/java/) |
| Python | [opentelemetry.io/docs/languages/python](https://opentelemetry.io/docs/languages/python/) |
| Node.js / JS | [opentelemetry.io/docs/languages/js](https://opentelemetry.io/docs/languages/js/) |
| .NET | [opentelemetry.io/docs/languages/net](https://opentelemetry.io/docs/languages/net/) |
| PHP | [opentelemetry.io/docs/languages/php](https://opentelemetry.io/docs/languages/php/) |
| Rust | [opentelemetry.io/docs/languages/rust](https://opentelemetry.io/docs/languages/rust/) |
| Ruby | [opentelemetry.io/docs/languages/ruby](https://opentelemetry.io/docs/languages/ruby/) |

### Naming spans across an async boundary

For a queue hop to be visible to `tracing.async_hop`, both sides must emit a span:

```go
// publisher
ctx, span := tracer.Start(ctx, "queue.publish.sales_email_order_shipment")
publishMessage(...)
span.End()

// consumer
ctx, span := tracer.Start(ctx, "queue.consume.sales_email_order_shipment")
processMessage(...)
span.End()
```

The provider matches by name; trace context propagation through the queue is **not required** for the count-based ratio facts (`success_rate_5m`, `completion_rate_5m`). It's required only if you later want per-trace correlation in Tempo.

### Verify spans arrive

Before wiring the model, hit Quickwit directly with the same query the provider would send:

```bash
curl -X POST 'http://quickwit:7280/api/v1/otel-traces-v0_7/search' \
  -H 'Content-Type: application/json' \
  -d '{"query":"span_name:checkout.order.placed","max_hits":1}'
```

`num_hits: 0` → spans aren't reaching Quickwit. Common causes: collector endpoint wrong, `OTEL_SERVICE_NAME` unset, span name typo, index name mismatch.

## Example model

See [`examples/magento-platform.model.yaml`](./examples/magento-platform.model.yaml) — wires two `tracing.transaction_flow`, three `tracing.async_hop`, and two `tracing.consumer_health` components against a real Magento checkout pipeline alongside `kubernetes.*` and `aws.*` infrastructure.

## Architecture

- `main.go` — 13 lines: registers types and calls `provider.Main`.
- `internal/probes/` — one ProbeFn per fact, builds Quickwit search bodies.
- `internal/quickwitclient/` — HTTP client with timeout, auth headers, status-to-sentinel-error mapping.

Plumbing (argv parsing, exit codes, debug tracing) comes from [`mgtt/sdk/provider`](https://github.com/mgt-tool/mgtt/tree/main/sdk/provider).

## Development

```bash
go build .                            # compile
go test -race ./...                   # unit tests
go test -tags=integration ./...       # integration tests (requires docker)
mgtt provider validate quickwit       # static checks
```
