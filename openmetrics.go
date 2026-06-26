package metrics

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// OpenMetricsContentType is the content type per the OpenMetrics specification.
const OpenMetricsContentType = "application/openmetrics-text; version=1.0.0; charset=utf-8"

// omHelpEscaper escapes backslash, newline, AND double-quote per the OpenMetrics
// 1.0.0 escaped-string rule (Prometheus 0.0.4 helpEscaper does not escape quotes).
var omHelpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)

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
	if _, err := io.WriteString(w, encodeOpenMetrics(r.collect())); err != nil {
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

// writeOMSimpleCounter writes an unlabeled counter in OpenMetrics format. It is
// a thin shim over the neutral IR and the OpenMetrics encoder, retained because
// it is referenced by the test suite.
func writeOMSimpleCounter(b *strings.Builder, c *Counter) {
	appendOpenMetrics(b, []metricFamily{c.family()})
}

// writeOMGauge writes an unlabeled gauge in OpenMetrics format (IR shim).
func writeOMGauge(b *strings.Builder, g *Gauge) {
	appendOpenMetrics(b, []metricFamily{g.family()})
}

// writeOMLabeledGauge writes a labeled gauge in OpenMetrics format (IR shim).
func writeOMLabeledGauge(b *strings.Builder, lg *LabeledGauge) {
	if f, ok := lg.family(); ok {
		appendOpenMetrics(b, []metricFamily{f})
	}
}
