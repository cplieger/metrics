# metrics
> Hand-rolled Prometheus text-format exposition library for Go

A lightweight, zero-dependency metrics library that exposes counters, gauges, labeled counters, and histograms in Prometheus text format. Extracted from multiple internal services into a standalone, reusable package. Standard library only.

## Install
<!-- TODO: registry/pull link -->
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
	dur := metrics.NewHistogram("myapp_http_duration_seconds", "Request latency")
	r.RegisterLabeledCounter(reqs)
	r.RegisterHistogram(dur)

	reqs.Inc("GET", "200")
	dur.Observe(0.042)

	http.Handle("/metrics", r.Handler())
	http.ListenAndServe(":9090", nil)
}
```

## API
- `NewCounter(name, help) *Counter` — monotonic counter with `Inc()`
- `NewGauge(name, help) *Gauge` — up/down gauge with `Inc()`, `Dec()`
- `NewLabeledCounter(name, help, labels) *LabeledCounter` — per-label-combination counter with `Inc(vals...)`
- `NewHistogram(name, help) *Histogram` — cumulative-bucket histogram with `Observe(seconds)`
- `NewRegistry(prefix) *Registry` — collects metrics; `Handler()` returns `http.HandlerFunc`
- `SetImageMetrics([]ImageMetric)` — set per-image gauge data
- `WriteProcessMetrics(*strings.Builder)` — emit goroutines, heap, GC, uptime
- `WriteCounter`, `WriteGauge`, `WriteLabeledCounter`, `WriteHistogram`, `WriteImageMetrics` — low-level formatters

## License
GPL-3.0 — see [LICENSE](LICENSE).
