package metrics_test

import (
	"net/http"

	"github.com/cplieger/metrics"
)

func Example() {
	r := metrics.NewRegistry("myapp")
	reqs := metrics.NewLabeledCounter("myapp_http_requests_total", "Total HTTP requests", []string{"method", "status"})
	dur := metrics.NewHistogram("myapp_http_duration_seconds", "Request latency", metrics.WithBuckets([]float64{0.01, 0.05, 0.1, 0.5, 1, 5}))
	r.RegisterLabeledCounter(reqs)
	r.RegisterHistogram(dur)

	reqs.Inc("GET", "200")

	timer := metrics.NewTimer(dur)
	_ = timer
	timer.ObserveDuration()

	http.Handle("/metrics", r.Handler())
	// Output:
}
