package metrics

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// This file defines the neutral metric-family intermediate representation (IR)
// and the two per-format encoders that materialise a scrape.
//
// The scrape pipeline is: collect the registry's metrics into a flat
// []metricFamily once (Registry.collect), then hand that slice to exactly one
// encoder — encodePrometheus for text format 0.0.4 or encodeOpenMetrics for
// OpenMetrics 1.0.0. The IR is format-neutral: every value and label string is
// pre-rendered in its canonical, format-independent form (formatValue for
// floats, %d for integers, buildLabelString for labels — all identical across
// both formats), so the encoders only differ in the genuinely format-specific
// bits:
//
//   - HELP/TYPE line ORDER (Prometheus: HELP then TYPE; OpenMetrics: TYPE then HELP)
//   - HELP escaping (Prometheus does not escape the double-quote; OpenMetrics does)
//   - counter naming (OpenMetrics strips _total for the TYPE/HELP family name and
//     ensures _total on the sample series; Prometheus uses the registered name verbatim)
//   - the trailing "# EOF\n" (OpenMetrics only)
//
// Counters carry their registered name verbatim in metricFamily.name (the
// _total-suffixed form for the two process counters); the OpenMetrics encoder
// derives the base/sample names from it via omCounterBaseName/omCounterSampleName.

// Metric type discriminators used in the "# TYPE" line.
const (
	typeCounter   = "counter"
	typeGauge     = "gauge"
	typeHistogram = "histogram"
)

// metricFamily is one "# TYPE"/"# HELP" block: a metric name, its type, its
// HELP text (raw, escaped per-format by the encoder), and the samples it emits.
type metricFamily struct {
	name    string
	typ     string
	help    string
	samples []sample
}

// sample is a single exposition line within a family. nameSuffix is appended to
// the family's series base name ("" for counters and gauges; "_bucket"/"_sum"/
// "_count" for histograms). labels is the pre-rendered, spec-escaped label
// content WITHOUT the surrounding braces ("" when the sample has no labels).
// value is the pre-rendered value token (formatValue output or a base-10
// integer), identical across both exposition formats.
type sample struct {
	nameSuffix string
	labels     string
	value      string
}

// family materialises an unlabeled counter into the IR. An unlabeled counter is
// always emitted.
func (c *Counter) family() metricFamily {
	return metricFamily{
		name:    c.name,
		typ:     typeCounter,
		help:    c.help,
		samples: []sample{{value: strconv.FormatInt(c.val.Load(), 10)}},
	}
}

// family materialises a labeled counter. ok is false when no label combination
// has been observed, matching the writers' "no keys, no output" behavior (no
// "# TYPE"/"# HELP" block is emitted for an empty labeled metric).
func (lc *LabeledCounter) family() (fam metricFamily, ok bool) {
	keys := sortedLabelKeys(&lc.mu, lc.vals)
	if len(keys) == 0 {
		return metricFamily{}, false
	}
	samples := make([]sample, 0, len(keys))
	for _, key := range keys {
		lc.mu.RLock()
		v := lc.vals[key]
		lc.mu.RUnlock()
		if v == nil {
			continue
		}
		samples = append(samples, sample{
			labels: buildLabelString(lc.labels, key),
			value:  strconv.FormatInt(v.Load(), 10),
		})
	}
	return metricFamily{name: lc.name, typ: typeCounter, help: lc.help, samples: samples}, len(samples) > 0
}

// family materialises an unlabeled gauge. An unlabeled gauge is always emitted.
func (g *Gauge) family() metricFamily {
	return metricFamily{
		name:    g.name,
		typ:     typeGauge,
		help:    g.help,
		samples: []sample{{value: formatValue(g.Get())}},
	}
}

// family materialises a labeled gauge. ok is false when no label combination
// has been set.
func (lg *LabeledGauge) family() (fam metricFamily, ok bool) {
	keys := sortedLabelKeys(&lg.mu, lg.vals)
	if len(keys) == 0 {
		return metricFamily{}, false
	}
	samples := make([]sample, 0, len(keys))
	for _, key := range keys {
		lg.mu.RLock()
		ptr := lg.vals[key]
		lg.mu.RUnlock()
		if ptr == nil {
			continue
		}
		samples = append(samples, sample{
			labels: buildLabelString(lg.labels, key),
			value:  formatValue(math.Float64frombits(ptr.Load())),
		})
	}
	return metricFamily{name: lg.name, typ: typeGauge, help: lg.help, samples: samples}, len(samples) > 0
}

// family materialises an unlabeled histogram. An unlabeled histogram is always
// emitted.
func (h *Histogram) family() metricFamily {
	return metricFamily{
		name:    h.name,
		typ:     typeHistogram,
		help:    h.help,
		samples: histogramSamples(h, ""),
	}
}

// family materialises a labeled histogram. ok is false when no label
// combination has been observed.
func (lh *LabeledHistogram) family() (fam metricFamily, ok bool) {
	keys := sortedLabelKeys(&lh.mu, lh.vals)
	if len(keys) == 0 {
		return metricFamily{}, false
	}
	samples := make([]sample, 0, len(keys)*(len(lh.bounds)+3))
	for _, key := range keys {
		lh.mu.RLock()
		h := lh.vals[key]
		lh.mu.RUnlock()
		if h == nil {
			continue
		}
		samples = append(samples, histogramSamples(h, buildLabelString(lh.labels, key))...)
	}
	return metricFamily{name: lh.name, typ: typeHistogram, help: lh.help, samples: samples}, len(samples) > 0
}

// histogramSamples expands one histogram into its cumulative bucket, sum, and
// count samples. labelStr is the pre-rendered user-label content (empty for an
// unlabeled histogram); the implicit le label is appended to each bucket. The
// ordering — every finite bucket, then the +Inf bucket, then _sum, then _count
// — matches the writers and both exposition formats.
func histogramSamples(h *Histogram, labelStr string) []sample {
	sum, count, bucketVals := h.snapshot()
	samples := make([]sample, 0, len(h.bounds)+3)
	for i, bound := range h.bounds {
		samples = append(samples, sample{
			nameSuffix: "_bucket",
			labels:     leLabels(labelStr, formatValue(bound)),
			value:      strconv.FormatInt(bucketVals[i], 10),
		})
	}
	samples = append(samples,
		sample{nameSuffix: "_bucket", labels: leLabels(labelStr, "+Inf"), value: strconv.FormatInt(bucketVals[len(h.bounds)], 10)},
		sample{nameSuffix: "_sum", labels: labelStr, value: formatValue(sum)},
		sample{nameSuffix: "_count", labels: labelStr, value: strconv.FormatInt(count, 10)},
	)
	return samples
}

// leLabels joins the user labels with the implicit le bucket label. With no
// user labels it yields just le="<bound>"; otherwise the le label follows the
// user labels, matching the writers' `{labels,le="..."}` rendering.
func leLabels(labelStr, le string) string {
	if labelStr == "" {
		return `le="` + le + `"`
	}
	return labelStr + `,le="` + le + `"`
}

// collect materialises the whole registry into a flat []metricFamily in the
// canonical scrape order (labeled counters, counters, labeled gauges, gauges,
// histograms, labeled histograms, then process metrics). Registry metrics are
// read under the registry read lock; process metrics are collected afterwards,
// outside the lock, because gathering them performs /proc and runtime reads
// that must not block registration.
func (r *Registry) collect() []metricFamily {
	r.mu.RLock()
	fams := make([]metricFamily, 0,
		len(r.labeledCounters)+len(r.counters)+len(r.labeledGauges)+
			len(r.gauges)+len(r.histograms)+len(r.labeledHistograms)+len(processFamilyNames))
	for _, lc := range r.labeledCounters {
		if f, ok := lc.family(); ok {
			fams = append(fams, f)
		}
	}
	for _, c := range r.counters {
		fams = append(fams, c.family())
	}
	for _, lg := range r.labeledGauges {
		if f, ok := lg.family(); ok {
			fams = append(fams, f)
		}
	}
	for _, g := range r.gauges {
		fams = append(fams, g.family())
	}
	for _, h := range r.histograms {
		fams = append(fams, h.family())
	}
	for _, lh := range r.labeledHistograms {
		if f, ok := lh.family(); ok {
			fams = append(fams, f)
		}
	}
	r.mu.RUnlock()
	return append(fams, processFamilies()...)
}

// encodePrometheus renders the IR in Prometheus text format 0.0.4: HELP before
// TYPE, HELP escaped without the double-quote, the registered name used
// verbatim for both the family lines and the sample series, and no trailing
// "# EOF".
func encodePrometheus(fams []metricFamily) string {
	var b strings.Builder
	appendPrometheus(&b, fams)
	return b.String()
}

func appendPrometheus(b *strings.Builder, fams []metricFamily) {
	for i := range fams {
		f := &fams[i]
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", f.name, helpEscaper.Replace(f.help), f.name, f.typ)
		for j := range f.samples {
			s := &f.samples[j]
			writeSample(b, f.name+s.nameSuffix, s.labels, s.value)
		}
	}
}

// encodeOpenMetrics renders the IR in OpenMetrics text format 1.0.0: TYPE before
// HELP, HELP escaped including the double-quote, counter family lines using the
// _total-stripped base name while the sample series keeps _total, and the
// mandatory trailing "# EOF".
func encodeOpenMetrics(fams []metricFamily) string {
	var b strings.Builder
	appendOpenMetrics(&b, fams)
	b.WriteString("# EOF\n")
	return b.String()
}

func appendOpenMetrics(b *strings.Builder, fams []metricFamily) {
	for i := range fams {
		f := &fams[i]
		headerName, seriesBase := f.name, f.name
		if f.typ == typeCounter {
			headerName = omCounterBaseName(f.name)
			seriesBase = omCounterSampleName(f.name)
		}
		fmt.Fprintf(b, "# TYPE %s %s\n# HELP %s %s\n", headerName, f.typ, headerName, omHelpEscaper.Replace(f.help))
		for j := range f.samples {
			s := &f.samples[j]
			writeSample(b, seriesBase+s.nameSuffix, s.labels, s.value)
		}
	}
}

// writeSample writes one exposition line, omitting the brace group when the
// sample carries no labels.
func writeSample(b *strings.Builder, name, labels, value string) {
	if labels == "" {
		fmt.Fprintf(b, "%s %s\n", name, value)
		return
	}
	fmt.Fprintf(b, "%s{%s} %s\n", name, labels, value)
}
