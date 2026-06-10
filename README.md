# metrics

[![CI](https://github.com/cplieger/metrics/actions/workflows/ci.yaml/badge.svg)](https://github.com/cplieger/metrics/actions/workflows/ci.yaml)
[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/metrics/v2.svg)](https://pkg.go.dev/github.com/cplieger/metrics/v2)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

> Hand-rolled Prometheus text-format exposition library for Go

A lightweight, zero-dependency metrics library that exposes counters, gauges, labeled counters, histograms, and process metrics in Prometheus text format (with optional OpenMetrics negotiation). Standard library only.

## Install

```sh
go get github.com/cplieger/metrics/v2
```

## Usage

```go
package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cplieger/metrics/v2"
)

func main() {
	// Registry prefix is applied to every registered metric name (myapp_*).
	r := metrics.NewRegistry("myapp")

	reqs := metrics.NewLabeledCounter(
		"http_requests_total", "Total HTTP requests",
		[]string{"method", "status"},
	)
	dur := metrics.NewHistogram(
		"http_request_duration_seconds", "Request latency",
	)
	r.RegisterLabeledCounter(reqs) // exposed as myapp_http_requests_total
	r.RegisterHistogram(dur)       // exposed as myapp_http_request_duration_seconds

	// One-shot HTTP instrumentation: caller owns the label set.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/widget", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	instrumented := metrics.InstrumentHandler(mux, reqs, dur,
		func(rq *http.Request, status int) []string {
			return []string{rq.Method, strconv.Itoa(status)}
		})

	// Or measure a code path with the labeled-histogram timer.
	work := metrics.NewLabeledHistogram("op_seconds", "op", []string{"kind"})
	r.RegisterLabeledHistogram(work)
	t := work.NewTimer("scan")
	time.Sleep(50 * time.Millisecond)
	t.ObserveDuration()

	http.Handle("/metrics", r.Handler())
	http.Handle("/", instrumented)
	_ = http.ListenAndServe(":9090", nil)
}
```

## API

### Constants & Variables

- `DefaultBuckets []float64` — HTTP-latency buckets (`0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0`).
- `APIBuckets []float64` — coarse buckets for outbound API calls and slow collect/scan cycles (`0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30`); use when DefaultBuckets would saturate everything in `+Inf`.
- `OpenMetricsContentType string` — `application/openmetrics-text; version=1.0.0; charset=utf-8`.

### Counters

- `NewCounter(name, help) *Counter` — monotonic counter; `Inc()`, `Add(n int64)`.
- `NewLabeledCounter(name, help, labels) *LabeledCounter` — `Inc(vals...)`; panics on label-arity mismatch.

### Gauges

- `NewGauge(name, help) *Gauge` — float64 gauge; `Set`, `Add`, `Sub`, `Inc`, `Dec`, `Get`.
- `NewLabeledGauge(name, help, labels) *LabeledGauge` — `Set(float64, vals...)`, `Delete(vals...)`, `Reset()`.

### Histograms

- `NewHistogram(name, help, opts ...Option) *Histogram` — `Observe(seconds)`; uses `DefaultBuckets` unless `WithBuckets` is provided.
- `NewLabeledHistogram(name, help, labels, opts ...Option) *LabeledHistogram` — `Observe(seconds, vals...)`.
- `WithBuckets([]float64) Option` — sets custom bucket boundaries.
- `FormatBound(float64) string` — formats a bucket boundary for Prometheus output.

### Timer

- `NewTimer(*Histogram) *Timer` — starts a timer for an unlabeled histogram.
- `(*LabeledHistogram).NewTimer(vals...) *Timer` — starts a timer for the given label set, so per-label latency can use `defer t.ObserveDuration()` ergonomics.
- `(*Timer).ObserveDuration() time.Duration` — records elapsed time and returns it.

### HTTP instrumentation (zero-dep `net/http`)

- `StatusRecorder` / `NewStatusRecorder(w)` — wraps `http.ResponseWriter` to capture the response status; implements `Unwrap` so `http.ResponseController` reaches Flusher / Hijacker.
- `RecordHTTP(c *LabeledCounter, h *Histogram, d time.Duration, labelVals ...string)` — record one request into the caller-supplied counter/histogram (either may be `nil`).
- `InstrumentHandler(next, c, h, labelValues func(r, status) []string) http.Handler` — middleware wrapper. The caller owns the label set, ordering, and any path templating.

### Registry

- `NewRegistry(prefix) *Registry` — every registered metric name is prefixed with `<prefix>_` (process metrics excepted). Pass `""` for no prefix.
- `Register{Counter,Gauge,LabeledCounter,LabeledGauge,Histogram,LabeledHistogram}`.
- `Handler()` — Prometheus text format 0.0.4.
- `OpenMetricsHandler()` — OpenMetrics 1.0.0.
- `NegotiateHandler()` — responds with OpenMetrics when the `Accept` header requests it, otherwise Prometheus text.

### Process metrics (emitted automatically)

- `process_goroutines`, `process_heap_bytes`, `process_gc_pause_seconds_total`, `process_uptime_seconds`, `process_start_time_seconds`.
- Linux only: `process_cpu_seconds_total`, `process_resident_memory_bytes`, `process_open_fds`, `process_max_fds`.

### Low-level writers

`WriteCounter`, `WriteGauge`, `WriteLabeledCounter`, `WriteLabeledGauge`, `WriteHistogram`, `WriteLabeledHistogram`, `WriteProcessMetrics` — for callers building custom handlers.

## Spec conformance

Valid Prometheus text exposition format 0.0.4: label values escape only `\`, `"`, and `\n` (as `\\`, `\"`, `\n`); HELP text escapes `\` and `\n`; metric/label names are validated at creation (panic on invalid); label arity is enforced (panic on mismatch); histograms always include a `+Inf` bucket equal to `_count`.

OpenMetrics 1.0.0 support: content-type `application/openmetrics-text; version=1.0.0; charset=utf-8`, mandatory trailing `# EOF`, TYPE before HELP, counter samples use the `_total` suffix, gauge values render as floats. Use `NegotiateHandler()` for content negotiation, `OpenMetricsHandler()` for direct OpenMetrics output.

## Unsupported by design (SKIP list)

| Feature | Reason |
|---------|--------|
| **Summary metric type** | Prometheus best practices recommend histograms; complex windowed-quantile implementation for no consumer benefit |
| **Exemplars (OpenMetrics)** | Niche; requires tracing integration |
| **Push / remote-write** | All consumers are pull-based |
| **Protobuf exposition format** | Text format is default in Prometheus 3.0; protobuf requires code generation |
| **Native histograms (exponential buckets)** | Requires protobuf format; large specialized implementation |
| **Unregister / dynamic metric lifecycle** | All consumers have static metric sets |
| **Image metrics** | Prior `EnableImageMetrics` / `SetImageMetrics` / `ImageMetric` API removed in v2; consumers that need per-image gauges layer them on `LabeledGauge` — see registry-stats |
| **Float64 counter** | Integer counters are sufficient for all consumers |
| **Gzip response compression** | Use standard HTTP middleware |
| **`Gauge.SetToCurrentTime()`** | Trivial one-liner users can write themselves |

## License

GPL-3.0 — see [LICENSE](LICENSE).
