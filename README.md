# metrics

[![CI](https://github.com/cplieger/metrics/actions/workflows/ci.yaml/badge.svg)](https://github.com/cplieger/metrics/actions/workflows/ci.yaml)
[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/metrics.svg)](https://pkg.go.dev/github.com/cplieger/metrics)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

> Hand-rolled Prometheus text-format exposition library for Go

A lightweight, zero-dependency metrics library that exposes counters, gauges, labeled counters, histograms, and more in Prometheus text format. Standard library only.

## Install

Go: `go get github.com/cplieger/metrics@latest`

## Usage
```go
package main

import (
	"net/http"
	"github.com/cplieger/metrics"
)

func main() {
	r := metrics.NewRegistry("myapp")
	reqs := metrics.NewLabeledCounter("myapp_http_requests_total", "Total HTTP requests", []string{"method", "status"})
	dur := metrics.NewHistogram("myapp_http_duration_seconds", "Request latency", metrics.WithBuckets([]float64{0.01, 0.05, 0.1, 0.5, 1, 5}))
	r.RegisterLabeledCounter(reqs)
	r.RegisterHistogram(dur)

	reqs.Inc("GET", "200")

	timer := metrics.NewTimer(dur)
	// ... do work ...
	timer.ObserveDuration()

	http.Handle("/metrics", r.Handler())
	http.ListenAndServe(":9090", nil)
}
```

## API

### Constants & Variables
- `DefaultBuckets []float64` — default histogram bucket boundaries (`0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0`)
- `OpenMetricsContentType string` — OpenMetrics content type (`application/openmetrics-text; version=1.0.0; charset=utf-8`)

### Counters
- `NewCounter(name, help) *Counter` — monotonic counter with `Inc()`, `Add(n int64)`
- `NewLabeledCounter(name, help, labels) *LabeledCounter` — per-label-combination counter with `Inc(vals...)`

### Gauges
- `NewGauge(name, help) *Gauge` — float64 gauge with `Set(float64)`, `Add(float64)`, `Sub(float64)`, `Inc()`, `Dec()`, `Get()`
- `NewLabeledGauge(name, help, labels) *LabeledGauge` — per-label gauge with `Set(float64, vals...)`

### Histograms
- `NewHistogram(name, help, opts ...Option) *Histogram` — histogram with `Observe(seconds)`; uses DefaultBuckets unless `WithBuckets` is provided
- `NewLabeledHistogram(name, help, labels, opts ...Option) *LabeledHistogram` — labeled histogram with `Observe(seconds, vals...)`
- `WithBuckets([]float64) Option` — sets custom bucket boundaries
- `FormatBound(float64) string` — formats a bucket boundary for Prometheus output
- `type Option func(*histogramCfg)` — functional option for histogram configuration

### Timer
- `NewTimer(h *Histogram) *Timer` — starts a timer; call `ObserveDuration()` to record elapsed time

### Registry
- `NewRegistry(prefix) *Registry` — collects metrics; `Handler()` returns `http.HandlerFunc`
- `RegisterCounter`, `RegisterGauge`, `RegisterLabeledCounter`, `RegisterLabeledGauge`, `RegisterHistogram`, `RegisterLabeledHistogram`
- `EnableImageMetrics()` — enables image metric output in handlers
- `OpenMetricsHandler()` — returns handler serving OpenMetrics text format (1.0.0)
- `NegotiateHandler()` — returns handler with content negotiation (OpenMetrics if Accept header requests it, otherwise Prometheus text)

### Image Metrics
- `SetImageMetrics([]ImageMetric)` — set per-image gauge data

### Process Metrics (emitted automatically)
- `process_goroutines`, `process_heap_bytes`, `process_gc_pause_seconds_total`, `process_uptime_seconds`
- `process_start_time_seconds`, `process_cpu_seconds_total` (Linux), `process_resident_memory_bytes` (Linux)
- `process_open_fds`, `process_max_fds` (Linux)

### Low-level Writers
- `WriteCounter`, `WriteGauge`, `WriteLabeledCounter`, `WriteLabeledGauge`, `WriteHistogram`, `WriteLabeledHistogram`, `WriteImageMetrics`, `WriteProcessMetrics`

## Spec Conformance

This library emits valid Prometheus text exposition format (version 0.0.4):
- Label values are escaped per spec: only `\`, `"`, and `\n` are escaped (as `\\`, `\"`, `\n`)
- HELP text escapes `\` and `\n` only
- Metric and label names are validated at creation time (`[a-zA-Z_:][a-zA-Z0-9_:]*` for metrics, `[a-zA-Z_][a-zA-Z0-9_]*` for labels)
- Label arity is enforced (panics on mismatch)
- Histograms always include `+Inf` bucket equal to `_count`

### OpenMetrics Text Format

Full support for OpenMetrics text exposition format 1.0.0 (the CNCF-standard successor to Prometheus format):
- Content-Type: `application/openmetrics-text; version=1.0.0; charset=utf-8`
- Exposition ends with mandatory `# EOF` line
- TYPE metadata appears before HELP (per spec ordering)
- Counter samples use `_total` suffix
- Gauge values rendered as floats (e.g., `42.0`)
- Content negotiation via `NegotiateHandler()` (responds to Accept header)
- Direct access via `OpenMetricsHandler()`

## Unsupported by Design (SKIP List)

The following features are intentionally not implemented:

| Feature | Reason |
|---------|--------|
| **Summary metric type** | Prometheus best practices recommend histograms; complex windowed-quantile implementation for no consumer benefit |
| **Exemplars (OpenMetrics)** | Niche; requires tracing integration and adds complexity for a feature most scrapers ignore |
| **Push / remote-write** | All consumers are pull-based |
| **Protobuf exposition format** | Text format is default in Prometheus 3.0; protobuf requires code generation |
| **Native histograms (exponential buckets)** | Requires protobuf format; large specialized implementation |
| **Unregister / dynamic metric lifecycle** | All consumers have static metric sets |
| **Float64 counter** | Integer counters are sufficient for all consumers |
| **Gzip response compression** | Use standard HTTP middleware |
| **Gauge.SetToCurrentTime()** | Trivial one-liner users can write themselves |

## License
GPL-3.0 — see [LICENSE](LICENSE).
