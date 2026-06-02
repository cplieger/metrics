package metrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenMetricsContentType is the content type per the OpenMetrics specification.
const OpenMetricsContentType = "application/openmetrics-text; version=1.0.0; charset=utf-8"

// NegotiateHandler returns an HTTP handler that performs content negotiation.
// If the client sends an Accept header preferring OpenMetrics, it responds in
// OpenMetrics text format; otherwise it falls back to Prometheus text format 0.0.4.
func (r *Registry) NegotiateHandler() http.HandlerFunc {
	promHandler := r.Handler()
	return func(w http.ResponseWriter, req *http.Request) {
		if acceptsOpenMetrics(req.Header.Get("Accept")) {
			r.serveOpenMetrics(w)
			return
		}
		promHandler(w, req)
	}
}

// OpenMetricsHandler returns an HTTP handler that always serves OpenMetrics text format.
func (r *Registry) OpenMetricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		r.serveOpenMetrics(w)
	}
}

func (r *Registry) serveOpenMetrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", OpenMetricsContentType)
	var b strings.Builder
	r.mu.RLock()
	for _, lc := range r.labeledCounters {
		writeOMCounter(&b, lc)
	}
	for _, c := range r.counters {
		writeOMSimpleCounter(&b, c)
	}
	for _, lg := range r.labeledGauges {
		writeOMLabeledGauge(&b, lg)
	}
	for _, g := range r.gauges {
		writeOMGauge(&b, g)
	}
	for _, h := range r.histograms {
		writeOMHistogram(&b, h)
	}
	for _, lh := range r.labeledHistograms {
		writeOMLabeledHistogram(&b, lh)
	}
	r.mu.RUnlock()
	writeOMProcessMetrics(&b, r.startTime)
	b.WriteString("# EOF\n")
	_, _ = w.Write([]byte(b.String()))
}

// acceptsOpenMetrics checks if the Accept header prefers OpenMetrics over Prometheus text.
func acceptsOpenMetrics(accept string) bool {
	return strings.Contains(accept, "application/openmetrics-text")
}

// omFormatFloat formats a float for OpenMetrics output.
func omFormatFloat(v float64) string {
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	if math.IsInf(v, -1) {
		return "-Inf"
	}
	if math.IsNaN(v) {
		return "NaN"
	}
	if v == float64(int64(v)) && v >= -1e15 && v <= 1e15 {
		return fmt.Sprintf("%d.0", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

// omCounterBaseName returns the base metric name for a counter, stripping _total if present.
func omCounterBaseName(name string) string {
	return strings.TrimSuffix(name, "_total")
}

// omCounterSampleName returns the sample name for a counter, ensuring _total suffix.
func omCounterSampleName(name string) string {
	if strings.HasSuffix(name, "_total") {
		return name
	}
	return name + "_total"
}

func writeOMSimpleCounter(b *strings.Builder, c *Counter) {
	base := omCounterBaseName(c.name)
	sample := omCounterSampleName(c.name)
	fmt.Fprintf(b, "# TYPE %s counter\n", base)
	fmt.Fprintf(b, "# HELP %s %s\n", base, helpEscaper.Replace(c.help))
	fmt.Fprintf(b, "%s %d\n", sample, c.val.Load())
}

func writeOMCounter(b *strings.Builder, lc *LabeledCounter) {
	lc.mu.RLock()
	keys := make([]labelKey, 0, len(lc.vals))
	for k := range lc.vals {
		keys = append(keys, k)
	}
	lc.mu.RUnlock()
	if len(keys) == 0 {
		return
	}
	sortLabelKeys(keys)
	base := omCounterBaseName(lc.name)
	sample := omCounterSampleName(lc.name)
	fmt.Fprintf(b, "# TYPE %s counter\n", base)
	fmt.Fprintf(b, "# HELP %s %s\n", base, helpEscaper.Replace(lc.help))
	for _, key := range keys {
		lc.mu.RLock()
		v := lc.vals[key]
		lc.mu.RUnlock()
		labelStr := buildLabelString(lc.labels, key)
		fmt.Fprintf(b, "%s{%s} %d\n", sample, labelStr, v.Load())
	}
}

func writeOMGauge(b *strings.Builder, g *Gauge) {
	v := g.Get()
	fmt.Fprintf(b, "# TYPE %s gauge\n", g.name)
	fmt.Fprintf(b, "# HELP %s %s\n", g.name, helpEscaper.Replace(g.help))
	fmt.Fprintf(b, "%s %s\n", g.name, omFormatFloat(v))
}

func writeOMLabeledGauge(b *strings.Builder, lg *LabeledGauge) {
	lg.mu.RLock()
	keys := make([]labelKey, 0, len(lg.vals))
	for k := range lg.vals {
		keys = append(keys, k)
	}
	lg.mu.RUnlock()
	if len(keys) == 0 {
		return
	}
	sortLabelKeys(keys)
	fmt.Fprintf(b, "# TYPE %s gauge\n", lg.name)
	fmt.Fprintf(b, "# HELP %s %s\n", lg.name, helpEscaper.Replace(lg.help))
	for _, key := range keys {
		lg.mu.RLock()
		ptr := lg.vals[key]
		lg.mu.RUnlock()
		if ptr == nil {
			continue
		}
		v := math.Float64frombits(ptr.Load())
		labelStr := buildLabelString(lg.labels, key)
		fmt.Fprintf(b, "%s{%s} %s\n", lg.name, labelStr, omFormatFloat(v))
	}
}

func writeOMHistogram(b *strings.Builder, h *Histogram) {
	sum := math.Float64frombits(h.sumBits.Load())
	count := h.count.Load()
	fmt.Fprintf(b, "# TYPE %s histogram\n", h.name)
	fmt.Fprintf(b, "# HELP %s %s\n", h.name, helpEscaper.Replace(h.help))
	for i, bound := range h.bounds {
		fmt.Fprintf(b, "%s_bucket{le=\"%s\"} %d\n", h.name, FormatBound(bound), h.buckets[i].Load())
	}
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", h.name, h.buckets[len(h.bounds)].Load())
	fmt.Fprintf(b, "%s_count %d\n", h.name, count)
	fmt.Fprintf(b, "%s_sum %s\n", h.name, omFormatFloat(sum))
}

func writeOMLabeledHistogram(b *strings.Builder, lh *LabeledHistogram) {
	lh.mu.RLock()
	keys := make([]labelKey, 0, len(lh.vals))
	for k := range lh.vals {
		keys = append(keys, k)
	}
	lh.mu.RUnlock()
	if len(keys) == 0 {
		return
	}
	sortLabelKeys(keys)
	fmt.Fprintf(b, "# TYPE %s histogram\n", lh.name)
	fmt.Fprintf(b, "# HELP %s %s\n", lh.name, helpEscaper.Replace(lh.help))
	for _, key := range keys {
		lh.mu.RLock()
		h := lh.vals[key]
		lh.mu.RUnlock()
		labelStr := buildLabelString(lh.labels, key)
		sum := math.Float64frombits(h.sumBits.Load())
		count := h.count.Load()
		for i, bound := range h.bounds {
			fmt.Fprintf(b, "%s_bucket{%s,le=\"%s\"} %d\n", lh.name, labelStr, FormatBound(bound), h.buckets[i].Load())
		}
		fmt.Fprintf(b, "%s_bucket{%s,le=\"+Inf\"} %d\n", lh.name, labelStr, h.buckets[len(h.bounds)].Load())
		fmt.Fprintf(b, "%s_count{%s} %d\n", lh.name, labelStr, count)
		fmt.Fprintf(b, "%s_sum{%s} %s\n", lh.name, labelStr, omFormatFloat(sum))
	}
}

func writeOMProcessMetrics(b *strings.Builder, startTime time.Time) {
	var d processMetricsData
	collectProcessMetrics(&d, startTime)

	fmt.Fprintf(b, "# TYPE process_goroutines gauge\n# HELP process_goroutines Number of goroutines\nprocess_goroutines %d.0\n", d.goroutines)
	fmt.Fprintf(b, "# TYPE process_heap_bytes gauge\n# HELP process_heap_bytes Heap memory in use\nprocess_heap_bytes %d.0\n", d.heapAlloc)
	fmt.Fprintf(b, "# TYPE process_gc_pause_seconds counter\n# HELP process_gc_pause_seconds Total GC pause time\nprocess_gc_pause_seconds_total %s\n", omFormatFloat(d.gcPause))
	fmt.Fprintf(b, "# TYPE process_uptime_seconds gauge\n# HELP process_uptime_seconds Process uptime\nprocess_uptime_seconds %s\n", omFormatFloat(d.uptime))
	fmt.Fprintf(b, "# TYPE process_start_time_seconds gauge\n# HELP process_start_time_seconds Start time of the process since unix epoch in seconds\nprocess_start_time_seconds %s\n", omFormatFloat(processStartTime))

	if d.cpuSeconds >= 0 {
		fmt.Fprintf(b, "# TYPE process_cpu_seconds counter\n# HELP process_cpu_seconds Total user and system CPU time spent in seconds\nprocess_cpu_seconds_total %s\n", omFormatFloat(d.cpuSeconds))
	}
	if d.rss > 0 {
		fmt.Fprintf(b, "# TYPE process_resident_memory_bytes gauge\n# HELP process_resident_memory_bytes Resident memory size in bytes\nprocess_resident_memory_bytes %d.0\n", d.rss)
	}
	if d.openFDs >= 0 {
		fmt.Fprintf(b, "# TYPE process_open_fds gauge\n# HELP process_open_fds Number of open file descriptors\nprocess_open_fds %d.0\n", d.openFDs)
		if d.maxFDs > 0 {
			fmt.Fprintf(b, "# TYPE process_max_fds gauge\n# HELP process_max_fds Maximum number of open file descriptors\nprocess_max_fds %d.0\n", d.maxFDs)
		}
	}
}

// sortLabelKeys sorts label keys lexicographically.
func sortLabelKeys(keys []labelKey) {
	sort.Slice(keys, func(a, c int) bool {
		for i := range keys[a] {
			if keys[a][i] != keys[c][i] {
				return keys[a][i] < keys[c][i]
			}
		}
		return false
	})
}
