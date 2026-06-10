// Package metrics provides a hand-rolled Prometheus text-format exposition library.
// It requires only the Go standard library.
//
// Both Prometheus text format (0.0.4) and OpenMetrics text format (1.0.0) are supported.
// Use Handler() for Prometheus format, OpenMetricsHandler() for OpenMetrics, or
// NegotiateHandler() for automatic content negotiation based on the Accept header.
//
// Unsupported by design (SKIP list):
//   - Summary metric type: Prometheus best practices recommend histograms
//   - Exemplars (OpenMetrics): niche; requires tracing integration
//   - Push / remote-write: all consumers are pull-based
//   - Protobuf exposition format: text format is default in Prometheus 3.0
//   - Native histograms (exponential buckets): requires protobuf format
//   - Unregister / dynamic metric lifecycle: all consumers have static metric sets
//   - Float64 counter: integer counters are sufficient
//   - Gzip response compression: use standard HTTP middleware
//   - Gauge.SetToCurrentTime(): trivial one-liner
package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// helpEscaper escapes backslashes and newlines in HELP text per Prometheus exposition format.
var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

// Registry holds a collection of metrics to be served.
type Registry struct {
	startTime         time.Time
	prefix            string
	counters          []*Counter
	gauges            []*Gauge
	labeledCounters   []*LabeledCounter
	labeledGauges     []*LabeledGauge
	histograms        []*Histogram
	labeledHistograms []*LabeledHistogram
	mu                sync.RWMutex
}

// NewRegistry creates a new metrics registry.
func NewRegistry(prefix string) *Registry {
	if prefix != "" {
		validateMetricName(prefix)
	}
	return &Registry{prefix: prefix, startTime: time.Now()}
}

// prefixed joins the registry prefix to a metric name (prefix_name). An empty
// prefix returns the name unchanged.
func (r *Registry) prefixed(name string) string {
	if r.prefix == "" {
		return name
	}
	return r.prefix + "_" + name
}

// RegisterCounter adds a counter to the registry.
func (r *Registry) RegisterCounter(c *Counter) {
	r.mu.Lock()
	c.name = r.prefixed(c.name)
	r.counters = append(r.counters, c)
	r.mu.Unlock()
}

// RegisterGauge adds a gauge to the registry.
func (r *Registry) RegisterGauge(g *Gauge) {
	r.mu.Lock()
	g.name = r.prefixed(g.name)
	r.gauges = append(r.gauges, g)
	r.mu.Unlock()
}

// RegisterLabeledCounter adds a labeled counter to the registry.
func (r *Registry) RegisterLabeledCounter(lc *LabeledCounter) {
	r.mu.Lock()
	lc.name = r.prefixed(lc.name)
	r.labeledCounters = append(r.labeledCounters, lc)
	r.mu.Unlock()
}

// RegisterLabeledGauge adds a labeled gauge to the registry.
func (r *Registry) RegisterLabeledGauge(lg *LabeledGauge) {
	r.mu.Lock()
	lg.name = r.prefixed(lg.name)
	r.labeledGauges = append(r.labeledGauges, lg)
	r.mu.Unlock()
}

// RegisterHistogram adds a histogram to the registry.
func (r *Registry) RegisterHistogram(h *Histogram) {
	r.mu.Lock()
	h.name = r.prefixed(h.name)
	r.histograms = append(r.histograms, h)
	r.mu.Unlock()
}

// RegisterLabeledHistogram adds a labeled histogram to the registry.
func (r *Registry) RegisterLabeledHistogram(lh *LabeledHistogram) {
	r.mu.Lock()
	lh.name = r.prefixed(lh.name)
	r.labeledHistograms = append(r.labeledHistograms, lh)
	r.mu.Unlock()
}

// Handler returns an HTTP handler serving Prometheus text format.
func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var b strings.Builder
		r.mu.RLock()
		for _, lc := range r.labeledCounters {
			WriteLabeledCounter(&b, lc)
		}
		for _, c := range r.counters {
			WriteCounter(&b, c)
		}
		for _, lg := range r.labeledGauges {
			WriteLabeledGauge(&b, lg)
		}
		for _, g := range r.gauges {
			WriteGauge(&b, g)
		}
		for _, h := range r.histograms {
			WriteHistogram(&b, h)
		}
		for _, lh := range r.labeledHistograms {
			WriteLabeledHistogram(&b, lh)
		}
		r.mu.RUnlock()
		WriteProcessMetrics(&b, r.startTime)
		_, _ = w.Write([]byte(b.String()))
	}
}

// FormatBound formats a bucket boundary for Prometheus output.
func FormatBound(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
