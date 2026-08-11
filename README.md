# metrics

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/metrics/v3.svg)](https://pkg.go.dev/github.com/cplieger/metrics/v3)
[![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/metrics)](https://github.com/cplieger/metrics/blob/main/go.mod)
[![Test coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/metrics/badges/coverage.json)](https://github.com/cplieger/metrics/actions/workflows/coverage.yml)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/metrics/badges/mutation.json)](https://github.com/cplieger/metrics/issues?q=label%3Agremlins-tracker)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13214/badge)](https://www.bestpractices.dev/projects/13214)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/metrics/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/metrics)

> Hand-rolled Prometheus text-format exposition library for Go

A zero-dependency metrics library that exposes counters, gauges, labeled counters, histograms, and process metrics in Prometheus text format. Standard library only.

## Install

```sh
go get github.com/cplieger/metrics/v3
```

## Usage

```go
package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cplieger/metrics/v3"
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

	// HTTP instrumentation: call RecordHTTP from middleware once the
	// response status is known. Caller owns the label set.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/widget", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	instrumented := http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
		start := time.Now()
		status := http.StatusOK // in real code, capture via a status-recording writer
		mux.ServeHTTP(w, rq)
		metrics.RecordHTTP(reqs, dur, time.Since(start), rq.Method, strconv.Itoa(status))
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

### Bucket presets

- `DefaultBuckets() []float64`: HTTP-latency buckets (`0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0`). Returns a fresh slice on every call.
- `APIBuckets() []float64`: coarse buckets for outbound API calls and slow collect/scan cycles (`0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30`); use when DefaultBuckets would saturate everything in `+Inf`. Returns a fresh slice on every call.

### Counters

- `NewCounter(name, help) *Counter`: monotonic counter; `Inc()`, `Add(n int64)`. Saturates at `math.MaxInt64` instead of wrapping negative.
- `NewLabeledCounter(name, help, labels) *LabeledCounter`: `Inc(vals...)`, `Add(int64, vals...)`, `Delete(vals...)`, `Reset()`; panics on label-arity mismatch.

Labeled metrics support at most 8 label names; construction panics beyond that.

### Gauges

- `NewGauge(name, help) *Gauge`: float64 gauge; `Set`, `Add`, `Sub`, `Inc`, `Dec`, `Get`.
- `NewLabeledGauge(name, help, labels) *LabeledGauge`: `Set(float64, vals...)`, `Delete(vals...)`, `Reset()`.

### Histograms

- `NewHistogram(name, help, opts ...Option) *Histogram`: `Observe(seconds)`; uses `DefaultBuckets` unless `WithBuckets` is provided.
- `NewLabeledHistogram(name, help, labels, opts ...Option) *LabeledHistogram`: `Observe(seconds, vals...)`, `Delete(vals...)`, `Reset()`.
- `WithBuckets([]float64) Option`: sets custom bucket boundaries. Bounds must be a strictly increasing sequence of finite values; the implicit `le="+Inf"` bucket is appended for you, so do not include `+Inf`. Non-finite, duplicate, or out-of-order bounds panic at construction. An empty slice yields a histogram with only the `+Inf` bucket.

### Timer

- `NewTimer(*Histogram) *Timer`: starts a timer for an unlabeled histogram.
- `(*LabeledHistogram).NewTimer(vals...) *Timer`: starts a timer for the given label set, so per-label latency can use `defer t.ObserveDuration()` ergonomics.
- `(*Timer).ObserveDuration() time.Duration`: records elapsed time and returns it.

### HTTP instrumentation

- `RecordHTTP(c *LabeledCounter, h *Histogram, d time.Duration, labelVals ...string)`: record one request into the caller-supplied counter/histogram (either may be `nil`). The caller owns the label set, ordering, and any path templating.

Pair `RecordHTTP` with middleware that captures the response status, such as [webhttp](https://github.com/cplieger/webhttp) (its `StatusRecorder` plus `Logging`'s `WithRecordMetric`) or middleware of your own, and call it once the final status is known.

Label values are caller-owned. Invalid UTF-8 never panics: the value is sanitized with the Unicode replacement character (U+FFFD) at record time, and a warning naming the metric is logged when the sanitized series is first created (repeat records do not re-warn). Sanitizing merges distinct raw values that repair to the same string into one series, and every record carrying invalid UTF-8 takes the slower series-creation path, so validate values derived from untrusted input before use. Untrusted label values are also a cardinality risk: each distinct label combination allocates a series retained until `Delete`/`Reset`, so labeling by raw request path or header content grows memory and scrape size without bound. Template paths to a fixed route set.

### Registry

- `NewRegistry(prefix) *Registry`: every registered metric name is prefixed with `<prefix>_` (process metrics excepted). Pass `""` for no prefix.
- `Register{Counter,Gauge,LabeledCounter,LabeledGauge,Histogram,LabeledHistogram}`.
- `Handler()`: Prometheus text format 0.0.4.

### Process metrics (emitted automatically)

- `go_goroutines`, `go_memstats_heap_alloc_bytes`, `process_gc_pause_seconds_total`, `process_uptime_seconds`, `process_start_time_seconds` (the goroutine and heap-alloc names match `client_golang`).
- Linux only: `process_cpu_seconds_total`, `process_resident_memory_bytes`, `process_open_fds`, `process_max_fds`.

### Low-level writers

`WriteCounter`, `WriteGauge`, `WriteLabeledCounter`, `WriteLabeledGauge`, `WriteHistogram`, `WriteLabeledHistogram`, `WriteProcessMetrics`: for callers building custom handlers.

## Spec conformance

The output is valid Prometheus text exposition format 0.0.4:

- Label values escape only `\`, `"`, and `\n` (as `\\`, `\"`, `\n`); HELP text escapes `\` and `\n`.
- Metric and label names are validated at creation, label arity is enforced on every record, duplicate metric family names panic at registration (including the reserved `process_*` names), and histogram bucket bounds panic unless strictly increasing and finite. All are fail-fast panics.
- Label values and HELP text are always exposed as valid UTF-8: invalid input is sanitized with U+FFFD and warned once (label values when the degraded series is first created, HELP text at construction). Neither path panics.
- Histograms always include a `+Inf` bucket equal to `_count`.

Numeric values render through a single canonical formatter: whole values as bare integers (e.g. `42`), other values in shortest round-trippable form, and `+Inf`/`-Inf`/`NaN` for non-finite.

## Unsupported by Design

| Feature                                     | Reason                                                                                                                                              |
| ------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Summary metric type**                     | Prometheus best practices recommend histograms; complex windowed-quantile implementation for no consumer benefit                                    |
| **OpenMetrics exposition + negotiation**    | Removed in v3: no consumer ever negotiated it, and Prometheus text is the scrape default. The v2 line retains it                                    |
| **Exemplars**                               | Niche; requires tracing integration and OpenMetrics or protobuf exposition                                                                          |
| **Push / remote-write**                     | All consumers are pull-based                                                                                                                        |
| **Protobuf exposition format**              | Text format is default in Prometheus 3.0; protobuf requires code generation                                                                         |
| **Native histograms (exponential buckets)** | Requires protobuf format; large specialized implementation                                                                                          |
| **Unregister / dynamic metric lifecycle**   | All consumers have static metric sets                                                                                                               |
| **Image metrics**                           | Prior `EnableImageMetrics` / `SetImageMetrics` / `ImageMetric` API removed in v2; consumers that need per-image gauges layer them on `LabeledGauge` |
| **Float64 counter**                         | Integer counters are sufficient for all consumers                                                                                                   |
| **Gzip response compression**               | Use standard HTTP middleware                                                                                                                        |
| **`Gauge.SetToCurrentTime()`**              | Trivial one-liner users can write themselves                                                                                                        |

## Contributing

Issues and PRs are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the
conventions and how to run the checks locally.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
