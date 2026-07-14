# metrics

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/metrics/v2.svg)](https://pkg.go.dev/github.com/cplieger/metrics/v2)
[![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/metrics)](https://github.com/cplieger/metrics/blob/main/go.mod)
[![Test coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/metrics/badges/coverage.json)](https://github.com/cplieger/metrics/actions/workflows/coverage.yml)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/metrics/badges/mutation.json)](https://github.com/cplieger/metrics/issues?q=label%3Agremlins-tracker)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13214/badge)](https://www.bestpractices.dev/projects/13214)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/metrics/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/metrics)

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
- `NewLabeledCounter(name, help, labels) *LabeledCounter` — `Inc(vals...)`, `Add(int64, vals...)`, `Delete(vals...)`, `Reset()`; panics on label-arity mismatch.

### Gauges

- `NewGauge(name, help) *Gauge` — float64 gauge; `Set`, `Add`, `Sub`, `Inc`, `Dec`, `Get`.
- `NewLabeledGauge(name, help, labels) *LabeledGauge` — `Set(float64, vals...)`, `Delete(vals...)`, `Reset()`.

### Histograms

- `NewHistogram(name, help, opts ...Option) *Histogram` — `Observe(seconds)`; uses `DefaultBuckets` unless `WithBuckets` is provided.
- `NewLabeledHistogram(name, help, labels, opts ...Option) *LabeledHistogram` — `Observe(seconds, vals...)`, `Delete(vals...)`, `Reset()`.
- `WithBuckets([]float64) Option` — sets custom bucket boundaries. Bounds must be a strictly increasing sequence of finite values; the implicit `le="+Inf"` bucket is appended for you, so do not include `+Inf`. Non-finite, duplicate, or out-of-order bounds panic at construction. An empty slice yields a histogram with only the `+Inf` bucket.

### Timer

- `NewTimer(*Histogram) *Timer` — starts a timer for an unlabeled histogram.
- `(*LabeledHistogram).NewTimer(vals...) *Timer` — starts a timer for the given label set, so per-label latency can use `defer t.ObserveDuration()` ergonomics.
- `(*Timer).ObserveDuration() time.Duration` — records elapsed time and returns it.

### HTTP instrumentation (zero-dep `net/http`)

- `StatusRecorder` / `NewStatusRecorder(w)` — wraps `http.ResponseWriter` to capture the response status; implements `Unwrap` so `http.ResponseController` reaches Flusher / Hijacker.
- `RecordHTTP(c *LabeledCounter, h *Histogram, d time.Duration, labelVals ...string)` — record one request into the caller-supplied counter/histogram (either may be `nil`).
- `InstrumentHandler(next, c, h, labelValues func(r, status) []string) http.Handler` — middleware wrapper. The caller owns the label set, ordering, and any path templating.

Label values are caller-owned and must be valid UTF-8: recording a label combination whose value is not valid UTF-8 panics at record time (Prometheus/OpenMetrics require valid UTF-8), not at construction. Values derived from untrusted input (raw request paths, header contents) must be templated or validated before use. Inside an http handler `net/http`'s per-request recover catches the panic and the process survives, but a metric update from a context without panic recovery (a background goroutine, not an http handler) will crash the process on such input.

### Registry

- `NewRegistry(prefix) *Registry` — every registered metric name is prefixed with `<prefix>_` (process metrics excepted). Pass `""` for no prefix.
- `Register{Counter,Gauge,LabeledCounter,LabeledGauge,Histogram,LabeledHistogram}`.
- `Handler()` — Prometheus text format 0.0.4.
- `OpenMetricsHandler()` — OpenMetrics 1.0.0.
- `NegotiateHandler()` — responds with OpenMetrics when the `Accept` header requests it, otherwise Prometheus text.

### Process metrics (emitted automatically)

- `go_goroutines`, `go_memstats_heap_alloc_bytes`, `process_gc_pause_seconds_total`, `process_uptime_seconds`, `process_start_time_seconds` (the goroutine and heap-alloc names match `client_golang`).
- Linux only: `process_cpu_seconds_total`, `process_resident_memory_bytes`, `process_open_fds`, `process_max_fds`.
  - Caveat: `process_cpu_seconds_total` assumes `USER_HZ` (`sysconf(_SC_CLK_TCK)`) = 100, the near-universal Linux default; on a kernel built with a different `CONFIG_HZ` the value is scaled by a constant factor. Reading the real value would require cgo or `golang.org/x/sys`, which the zero-dependency contract excludes.

### Low-level writers

`WriteCounter`, `WriteGauge`, `WriteLabeledCounter`, `WriteLabeledGauge`, `WriteHistogram`, `WriteLabeledHistogram`, `WriteProcessMetrics` — for callers building custom handlers.

## Spec conformance

Valid Prometheus text exposition format 0.0.4: label values escape only `\`, `"`, and `\n` (as `\\`, `\"`, `\n`); HELP text escapes `\` and `\n`; metric/label names are validated at creation (panic on invalid); label arity is enforced (panic on mismatch); label values must be valid UTF-8 (panic at record time on the first invalid value for a series); duplicate metric family names panic at registration (fail-fast, including the reserved `process_*` names); histogram bucket bounds are validated at creation (panic unless strictly increasing and finite); histograms always include a `+Inf` bucket equal to `_count`.

OpenMetrics 1.0.0 support: content-type `application/openmetrics-text; version=1.0.0; charset=utf-8`, mandatory trailing `# EOF`, TYPE before HELP, counter samples use the `_total` suffix. Numeric values render through a single canonical formatter shared by both formats, so a given value is exposed identically: whole values as bare integers (e.g. `42`), other values in shortest round-trippable form, and `+Inf`/`-Inf`/`NaN` for non-finite. Use `NegotiateHandler()` for content negotiation, `OpenMetricsHandler()` for direct OpenMetrics output.

## Unsupported by design (SKIP list)

| Feature                                     | Reason                                                                                                                                              |
| ------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Summary metric type**                     | Prometheus best practices recommend histograms; complex windowed-quantile implementation for no consumer benefit                                    |
| **Exemplars (OpenMetrics)**                 | Niche; requires tracing integration                                                                                                                 |
| **Push / remote-write**                     | All consumers are pull-based                                                                                                                        |
| **Protobuf exposition format**              | Text format is default in Prometheus 3.0; protobuf requires code generation                                                                         |
| **Native histograms (exponential buckets)** | Requires protobuf format; large specialized implementation                                                                                          |
| **Unregister / dynamic metric lifecycle**   | All consumers have static metric sets                                                                                                               |
| **Image metrics**                           | Prior `EnableImageMetrics` / `SetImageMetrics` / `ImageMetric` API removed in v2; consumers that need per-image gauges layer them on `LabeledGauge` |
| **Float64 counter**                         | Integer counters are sufficient for all consumers                                                                                                   |
| **Gzip response compression**               | Use standard HTTP middleware                                                                                                                        |
| **`Gauge.SetToCurrentTime()`**              | Trivial one-liner users can write themselves                                                                                                        |

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude Opus](https://www.anthropic.com/claude) and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

GPL-3.0 — see [LICENSE](LICENSE).
