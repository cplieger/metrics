package metrics_test

import (
	"net/http"

	"github.com/cplieger/metrics/v2"
)

func Example() {
	r := metrics.NewRegistry("myapp") // prefixes every registered metric name with "myapp_"
	reqs := metrics.NewLabeledCounter("http_requests_total", "Total HTTP requests", []string{"method", "status"})
	dur := metrics.NewHistogram("http_duration_seconds", "Request latency", metrics.WithBuckets([]float64{0.01, 0.05, 0.1, 0.5, 1, 5}))
	r.RegisterLabeledCounter(reqs) // exposed as myapp_http_requests_total
	r.RegisterHistogram(dur)       // exposed as myapp_http_duration_seconds

	reqs.Inc("GET", "200")
	timer := metrics.NewTimer(dur)
	timer.ObserveDuration()

	http.Handle("/metrics", r.Handler())
	// Output:
}
