// Package metrics provides a hand-rolled Prometheus text-format exposition library.
// It requires only the Go standard library.
//
// Both Prometheus text format (0.0.4) and OpenMetrics text format (1.0.0) are supported.
// Use Handler() for Prometheus format, OpenMetricsHandler() for OpenMetrics, or
// NegotiateHandler() for automatic content negotiation based on the Accept header.
//
// Registration order: complete all Register* calls before serving a custom
// handler built on the low-level Write* functions. Write* reads the metric
// name without the registry lock, so it is not synchronized with a concurrent
// Register* rename of the same metric. The Registry handlers (guarded by the
// registry lock) and the record paths (Inc/Add/Set/Observe, guarded by the
// metric lock) are safe to run concurrently with registration.
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
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// helpEscaper escapes backslashes and newlines in HELP text per Prometheus exposition format.
var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

// Process metric family names, defined once so the reservation list
// (processFamilyNames) and the process-metric IR builder (processFamilies,
// rendered by both encoders) reference a single source and cannot drift.
const (
	pmGoroutines     = "go_goroutines"
	pmHeapAllocBytes = "go_memstats_heap_alloc_bytes"
	pmGCPause        = "process_gc_pause_seconds"
	pmGCPauseTotal   = "process_gc_pause_seconds_total"
	pmUptime         = "process_uptime_seconds"
	pmStartTime      = "process_start_time_seconds"
	pmCPU            = "process_cpu_seconds"
	pmCPUTotal       = "process_cpu_seconds_total"
	pmResidentBytes  = "process_resident_memory_bytes"
	pmOpenFDs        = "process_open_fds"
	pmMaxFDs         = "process_max_fds"
)

// processFamilyNames are the family names processFamilies emits
// unconditionally. Reserved at creation so a user metric colliding with one fails fast like any
// other duplicate instead of silently producing a duplicate "# TYPE" line that breaks the scrape.
// Both the OM counter base and the Prometheus _total TYPE form are listed for the two process
// counters, because a user counter normalizes to the base while a user gauge/histogram reserves
// its name verbatim.
var processFamilyNames = []string{
	pmGoroutines, pmHeapAllocBytes,
	pmGCPause, pmGCPauseTotal,
	pmUptime, pmStartTime,
	pmCPU, pmCPUTotal,
	pmResidentBytes, pmOpenFDs, pmMaxFDs,
}

// Process metric HELP text, single-sourced so the Prometheus and OpenMetrics
// writers cannot expose divergent descriptions for the same family.
const (
	helpGoroutines = "Number of goroutines that currently exist."
	helpHeapAlloc  = "Number of heap bytes allocated and currently in use."
	helpGCPause    = "Total GC pause time"
	helpUptime     = "Process uptime"
	helpStartTime  = "Start time of the process since unix epoch in seconds"
	helpCPU        = "Total user and system CPU time spent in seconds"
	helpResident   = "Resident memory size in bytes"
	helpOpenFDs    = "Number of open file descriptors"
	helpMaxFDs     = "Maximum number of open file descriptors"
)

// Registry holds a collection of metrics to be served.
type Registry struct {
	names             map[string]string
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
	r := &Registry{
		prefix: prefix,
		names:  make(map[string]string),
	}
	for _, n := range processFamilyNames {
		r.names[n] = "process metric"
	}
	return r
}

// prefixed joins the registry prefix to a metric name (prefix_name). An empty
// prefix returns the name unchanged.
func (r *Registry) prefixed(name string) string {
	if r.prefix == "" {
		return name
	}
	return r.prefix + "_" + name
}

// reserveName records the exposition family name a metric occupies and panics
// if another metric already claims it. The family name is the identifier that
// appears in the "# TYPE" line: counters normalize on their _total-stripped
// base (so foo and foo_total are one family), every other type uses its name
// verbatim. Family names must be unique across the whole registry and across
// types, because a duplicate "# TYPE" line makes both Prometheus and
// OpenMetrics parsers reject the entire scrape. Registration is fail-fast: a
// collision is a programming error, like the panics in validateMetricName and
// the label-arity guards. Callers must hold r.mu.
func (r *Registry) reserveName(family, kind string) {
	if existing, ok := r.names[family]; ok {
		panic(fmt.Sprintf("metrics: %s %q collides with already-registered %s; "+
			"metric family names must be unique across the registry", kind, family, existing))
	}
	r.names[family] = kind
}

// reserveHistogramFamily reserves the histogram base name plus the derived
// _bucket/_sum/_count series names a histogram emits in both writers.
// Callers must hold r.mu.
func (r *Registry) reserveHistogramFamily(name, kind string) {
	r.reserveName(name, kind)
	r.reserveName(name+"_bucket", kind)
	r.reserveName(name+"_sum", kind)
	r.reserveName(name+"_count", kind)
}

// reserveCounterFamily reserves the OpenMetrics base name plus the _total
// sample-series name a counter emits across both formats, mirroring
// reserveHistogramFamily. Callers must hold r.mu.
func (r *Registry) reserveCounterFamily(name, kind string) {
	base := omCounterBaseName(name)
	r.reserveName(base, kind)
	if sample := omCounterSampleName(name); sample != base {
		r.reserveName(sample, kind)
	}
}

// RegisterCounter adds a counter to the registry.
func (r *Registry) RegisterCounter(c *Counter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !c.registered.CompareAndSwap(false, true) {
		panic("metrics: counter already registered")
	}
	c.name = r.prefixed(c.name)
	r.reserveCounterFamily(c.name, "counter")
	r.counters = append(r.counters, c)
}

// RegisterGauge adds a gauge to the registry.
func (r *Registry) RegisterGauge(g *Gauge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !g.registered.CompareAndSwap(false, true) {
		panic("metrics: gauge already registered")
	}
	g.name = r.prefixed(g.name)
	r.reserveName(g.name, "gauge")
	r.gauges = append(r.gauges, g)
}

// RegisterLabeledCounter adds a labeled counter to the registry.
func (r *Registry) RegisterLabeledCounter(lc *LabeledCounter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !lc.registered.CompareAndSwap(false, true) {
		panic("metrics: labeled counter already registered")
	}
	lc.mu.Lock()
	lc.name = r.prefixed(lc.name)
	lc.mu.Unlock()
	r.reserveCounterFamily(lc.name, "labeled counter")
	r.labeledCounters = append(r.labeledCounters, lc)
}

// RegisterLabeledGauge adds a labeled gauge to the registry.
func (r *Registry) RegisterLabeledGauge(lg *LabeledGauge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !lg.registered.CompareAndSwap(false, true) {
		panic("metrics: labeled gauge already registered")
	}
	lg.mu.Lock()
	lg.name = r.prefixed(lg.name)
	lg.mu.Unlock()
	r.reserveName(lg.name, "labeled gauge")
	r.labeledGauges = append(r.labeledGauges, lg)
}

// RegisterHistogram adds a histogram to the registry.
func (r *Registry) RegisterHistogram(h *Histogram) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !h.registered.CompareAndSwap(false, true) {
		panic("metrics: histogram already registered")
	}
	h.name = r.prefixed(h.name)
	r.reserveHistogramFamily(h.name, "histogram")
	r.histograms = append(r.histograms, h)
}

// RegisterLabeledHistogram adds a labeled histogram to the registry.
func (r *Registry) RegisterLabeledHistogram(lh *LabeledHistogram) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !lh.registered.CompareAndSwap(false, true) {
		panic("metrics: labeled histogram already registered")
	}
	lh.mu.Lock()
	lh.name = r.prefixed(lh.name)
	lh.mu.Unlock()
	r.reserveHistogramFamily(lh.name, "labeled histogram")
	r.labeledHistograms = append(r.labeledHistograms, lh)
}

// Handler returns an HTTP handler serving Prometheus text format.
func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, err := io.WriteString(w, encodePrometheus(r.collect())); err != nil {
			slog.Debug("metrics: writing prometheus exposition failed", "error", err)
		}
	}
}

// formatValue renders a float64 metric value in the canonical form shared by
// the Prometheus 0.0.4 and OpenMetrics 1.0.0 writers, so a given value is
// exposed identically regardless of format. Non-finite values use the spec
// tokens "+Inf"/"-Inf"/"NaN" (accepted case-insensitively by both formats per
// the OpenMetrics ABNF). A finite value that is exactly integral and within the
// int64-exact range renders as a bare integer (e.g. "42", "1718193600"); the
// OpenMetrics ABNF's realnumber accepts a bare integer, so no ".0" is needed.
// Everything else uses the shortest round-trippable form (strconv 'g'), which
// preserves full precision and never floors a small magnitude to zero the way
// the fixed-precision %.6f the Prometheus writers previously used did.
func formatValue(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	if v >= -1e15 && v <= 1e15 && v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
