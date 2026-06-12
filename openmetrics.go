package metrics

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// OpenMetricsContentType is the content type per the OpenMetrics specification.
const OpenMetricsContentType = "application/openmetrics-text; version=1.0.0; charset=utf-8"

// omHelpEscaper escapes backslash, newline, AND double-quote per the OpenMetrics
// 1.0.0 escaped-string rule (Prometheus 0.0.4 helpEscaper does not escape quotes).
var omHelpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, `"`, `\"`)

// NegotiateHandler returns an HTTP handler that performs content negotiation.
// If the client sends an Accept header preferring OpenMetrics, it responds in
// OpenMetrics text format; otherwise it falls back to Prometheus text format 0.0.4.
func (r *Registry) NegotiateHandler() http.HandlerFunc {
	promHandler := r.Handler()
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Vary", "Accept")
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
	w.Header().Set("X-Content-Type-Options", "nosniff")
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
	writeOMProcessMetrics(&b)
	b.WriteString("# EOF\n")
	if _, err := io.WriteString(w, b.String()); err != nil {
		slog.Debug("metrics: writing openmetrics exposition failed", "error", err)
	}
}

// acceptsOpenMetrics reports whether the Accept header prefers OpenMetrics over
// Prometheus text. OpenMetrics is served only when the client explicitly lists
// application/openmetrics-text with a non-zero q-value AND ranks it at least as
// high as text/plain (RFC 7231 quality values: an omitted q defaults to 1.0,
// q=0 means "not acceptable"). This honours an explicit refusal
// (application/openmetrics-text;q=0) and a client that ranks Prometheus text
// higher — both of which a bare substring match would mishandle.
func acceptsOpenMetrics(accept string) bool {
	omQ, omPresent := mediaQuality(accept, "application/openmetrics-text")
	if !omPresent || omQ <= 0 {
		return false
	}
	if txtQ, txtPresent := mediaQuality(accept, "text/plain"); txtPresent && txtQ > omQ {
		return false
	}
	return true
}

// mediaQuality returns the q-value the Accept header assigns to an exact media
// type and whether that type appears at all. Per RFC 7231 the q parameter
// defaults to 1.0 when omitted; a malformed q also falls back to 1.0. Only an
// exact type match counts — wildcards (*/*) are not expanded, matching the
// explicit-mention requirement for serving OpenMetrics.
func mediaQuality(accept, mediaType string) (q float64, present bool) {
	for part := range strings.SplitSeq(accept, ",") {
		segs := strings.Split(part, ";")
		if !strings.EqualFold(strings.TrimSpace(segs[0]), mediaType) {
			continue
		}
		cur := 1.0
		for _, p := range segs[1:] {
			name, v, found := strings.Cut(strings.TrimSpace(p), "=")
			if !found || !strings.EqualFold(name, "q") {
				continue
			}
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				cur = parsed
			}
		}
		if !present || cur > q {
			q, present = cur, true
		}
	}
	return q, present
}

// omCounterBaseName returns the base metric name for a counter, stripping _total if present.
func omCounterBaseName(name string) string {
	base := strings.TrimSuffix(name, "_total")
	if base == "" {
		// A counter named exactly "_total" strips to an empty base, which would
		// emit a malformed "# TYPE  counter" line with no metric name. Keep the
		// full name so the family stays valid and non-empty.
		return name
	}
	return base
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
	fmt.Fprintf(b, "# HELP %s %s\n", base, omHelpEscaper.Replace(c.help))
	fmt.Fprintf(b, "%s %d\n", sample, c.val.Load())
}

func writeOMCounter(b *strings.Builder, lc *LabeledCounter) {
	keys := sortedLabelKeys(&lc.mu, lc.vals)
	if len(keys) == 0 {
		return
	}
	base := omCounterBaseName(lc.name)
	sample := omCounterSampleName(lc.name)
	fmt.Fprintf(b, "# TYPE %s counter\n", base)
	fmt.Fprintf(b, "# HELP %s %s\n", base, omHelpEscaper.Replace(lc.help))
	for _, key := range keys {
		lc.mu.RLock()
		v := lc.vals[key]
		lc.mu.RUnlock()
		if v == nil {
			continue
		}
		labelStr := buildLabelString(lc.labels, key)
		fmt.Fprintf(b, "%s{%s} %d\n", sample, labelStr, v.Load())
	}
}

func writeOMGauge(b *strings.Builder, g *Gauge) {
	v := g.Get()
	fmt.Fprintf(b, "# TYPE %s gauge\n", g.name)
	fmt.Fprintf(b, "# HELP %s %s\n", g.name, omHelpEscaper.Replace(g.help))
	fmt.Fprintf(b, "%s %s\n", g.name, formatValue(v))
}

func writeOMLabeledGauge(b *strings.Builder, lg *LabeledGauge) {
	keys := sortedLabelKeys(&lg.mu, lg.vals)
	if len(keys) == 0 {
		return
	}
	fmt.Fprintf(b, "# TYPE %s gauge\n", lg.name)
	fmt.Fprintf(b, "# HELP %s %s\n", lg.name, omHelpEscaper.Replace(lg.help))
	for _, key := range keys {
		lg.mu.RLock()
		ptr := lg.vals[key]
		lg.mu.RUnlock()
		if ptr == nil {
			continue
		}
		v := math.Float64frombits(ptr.Load())
		labelStr := buildLabelString(lg.labels, key)
		fmt.Fprintf(b, "%s{%s} %s\n", lg.name, labelStr, formatValue(v))
	}
}

func writeOMHistogram(b *strings.Builder, h *Histogram) {
	sum, count, bucketVals := h.snapshot()
	fmt.Fprintf(b, "# TYPE %s histogram\n", h.name)
	fmt.Fprintf(b, "# HELP %s %s\n", h.name, omHelpEscaper.Replace(h.help))
	for i, bound := range h.bounds {
		fmt.Fprintf(b, "%s_bucket{le=\"%s\"} %d\n", h.name, formatValue(bound), bucketVals[i])
	}
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", h.name, bucketVals[len(h.bounds)])
	fmt.Fprintf(b, "%s_sum %s\n", h.name, formatValue(sum))
	fmt.Fprintf(b, "%s_count %d\n", h.name, count)
}

func writeOMLabeledHistogram(b *strings.Builder, lh *LabeledHistogram) {
	keys := sortedLabelKeys(&lh.mu, lh.vals)
	if len(keys) == 0 {
		return
	}
	fmt.Fprintf(b, "# TYPE %s histogram\n", lh.name)
	fmt.Fprintf(b, "# HELP %s %s\n", lh.name, omHelpEscaper.Replace(lh.help))
	for _, key := range keys {
		lh.mu.RLock()
		h := lh.vals[key]
		lh.mu.RUnlock()
		if h == nil {
			continue
		}
		labelStr := buildLabelString(lh.labels, key)
		sum, count, bucketVals := h.snapshot()
		for i, bound := range h.bounds {
			fmt.Fprintf(b, "%s_bucket{%s,le=\"%s\"} %d\n", lh.name, labelStr, formatValue(bound), bucketVals[i])
		}
		fmt.Fprintf(b, "%s_bucket{%s,le=\"+Inf\"} %d\n", lh.name, labelStr, bucketVals[len(h.bounds)])
		fmt.Fprintf(b, "%s_sum{%s} %s\n", lh.name, labelStr, formatValue(sum))
		fmt.Fprintf(b, "%s_count{%s} %d\n", lh.name, labelStr, count)
	}
}

// writeOMProcessMetrics writes Go runtime and standard process metrics in
// OpenMetrics format. process_goroutines/heap/gc/uptime/start_time are emitted
// on every platform. process_cpu_seconds_total, process_resident_memory_bytes,
// process_open_fds and process_max_fds are sourced from /proc and are
// Linux-only; on other platforms they are silently omitted. CPU time assumes
// USER_HZ=100 (Linux).
func writeOMProcessMetrics(b *strings.Builder) {
	var d processMetricsData
	collectProcessMetrics(&d)

	fmt.Fprintf(b, "# TYPE %s gauge\n# HELP %s %s\n%s %d\n", pmGoroutines, pmGoroutines, helpGoroutines, pmGoroutines, d.goroutines)
	fmt.Fprintf(b, "# TYPE %s gauge\n# HELP %s %s\n%s %d\n", pmHeapBytes, pmHeapBytes, helpHeapBytes, pmHeapBytes, d.heapAlloc)
	fmt.Fprintf(b, "# TYPE %s counter\n# HELP %s %s\n%s %s\n", pmGCPause, pmGCPause, helpGCPause, pmGCPauseTotal, formatValue(d.gcPause))
	fmt.Fprintf(b, "# TYPE %s gauge\n# HELP %s %s\n%s %s\n", pmUptime, pmUptime, helpUptime, pmUptime, formatValue(d.uptime))
	fmt.Fprintf(b, "# TYPE %s gauge\n# HELP %s %s\n%s %d\n", pmStartTime, pmStartTime, helpStartTime, pmStartTime, processStartTime.Unix())

	if d.hasCPU() {
		fmt.Fprintf(b, "# TYPE %s counter\n# HELP %s %s\n%s %s\n", pmCPU, pmCPU, helpCPU, pmCPUTotal, formatValue(d.cpuSeconds))
	}
	if d.hasRSS() {
		fmt.Fprintf(b, "# TYPE %s gauge\n# HELP %s %s\n%s %d\n", pmResidentBytes, pmResidentBytes, helpResident, pmResidentBytes, d.rss)
	}
	if d.hasOpenFDs() {
		fmt.Fprintf(b, "# TYPE %s gauge\n# HELP %s %s\n%s %d\n", pmOpenFDs, pmOpenFDs, helpOpenFDs, pmOpenFDs, d.openFDs)
		if d.hasMaxFDs() {
			fmt.Fprintf(b, "# TYPE %s gauge\n# HELP %s %s\n%s %d\n", pmMaxFDs, pmMaxFDs, helpMaxFDs, pmMaxFDs, d.maxFDs)
		}
	}
}
