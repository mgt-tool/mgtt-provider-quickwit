# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning: [SemVer](https://semver.org/).

## [0.1.1] — 2026-04-16

### Added

- **`compatibility:` block in `provider.yaml`** — declares the backend versions this provider is built against (Quickwit `0.8.x`), the exact image digests the integration tests run against, and the version-sensitive behaviors callers should know about (`workers_active` falls back to `terms` agg in 0.8.x because no `cardinality` agg exists). Symmetric with mgtt-provider-tempo.
- **README "Compatibility" section** — surfaces the same contract near the top of the page so operators see it before installing.

### Changed

- **Integration test image pinned by digest** — `quickwit/quickwit:0.8.2@sha256:363ff…0ef92`. Same lesson the tempo provider learned the hard way: the `:0.8.2` tag could be re-rolled with breaking aggregation-shape changes; the digest makes the test reproducible.

## [0.1.0] — 2026-04-16

Initial release. Cross-span tracing checks against Quickwit's search API: business flows, queue hops, consumer pools.

### Added

- **`tracing.transaction_flow` type** with four facts: `started_count_5m`, `completed_count_5m`, `completion_rate_5m`, `p99_lag_ms`.
- **`tracing.async_hop` type** with five facts: `producer_count_5m`, `consumer_count_5m`, `success_rate_5m`, `consumer_error_rate_5m`, `p99_consumer_duration_ms`.
- **`tracing.consumer_health` type** with five facts: `processed_count_5m`, `throughput_per_min`, `p99_processing_ms`, `error_rate_5m`, `workers_active`.
- **`internal/quickwitclient/`** — HTTP client with timeout, auth headers, and Quickwit status code → sentinel error mapping (401/403→Forbidden, 404→NotFound, 400→Usage, 5xx→Transient).
- **Example model** in `examples/magento-platform.model.yaml` — seven tracing components (two flows, three hops, two pools) wired alongside kubernetes/aws infra for a real Magento storefront.
- **Integration tests** in `test/integration/` exercising eight end-to-end scenarios against a real Quickwit container, covering all three types plus usage- and transient-error exit codes.
- **README "Emitting spans to Quickwit"** section with the field contract, language-agnostic OTEL bootstrap pointers, and a `curl` to verify spans arrive before debugging the model.
