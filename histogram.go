package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultBuckets are the default histogram bucket boundaries (HTTP latency).
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0}

// APIBuckets are coarse latency buckets (seconds) for outbound API calls and
// slow collect/scan cycles, where DefaultBuckets (max 1.0s) saturates at +Inf.
var APIBuckets = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// histogramCfg holds optional configuration for histogram construction.
type histogramCfg struct {
	buckets []float64
}

// Option configures optional histogram parameters.
type Option func(*histogramCfg)

// WithBuckets returns an Option that sets custom bucket boundaries for a histogram.
func WithBuckets(buckets []float64) Option {
	return func(cfg *histogramCfg) {
		cfg.buckets = buckets
	}
}

// Histogram tracks a distribution using cumulative buckets and atomic CAS for sum.
type Histogram struct {
	name    string
	help    string
	bounds  []float64
	buckets []atomic.Int64
	sumBits atomic.Uint64
	count   atomic.Int64
}

// NewHistogram creates a histogram with the given name and help text.
// By default it uses DefaultBuckets; use WithBuckets to override.
func NewHistogram(name, help string, opts ...Option) *Histogram {
	cfg := histogramCfg{buckets: DefaultBuckets}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	validateMetricName(name)
	sorted := make([]float64, len(cfg.buckets))
	copy(sorted, cfg.buckets)
	sort.Float64s(sorted)
	h := &Histogram{
		name:    name,
		help:    help,
		bounds:  sorted,
		buckets: make([]atomic.Int64, len(sorted)+1),
	}
	return h
}

// Observe records a value in the histogram.
func (h *Histogram) Observe(seconds float64) {
	for {
		old := h.sumBits.Load()
		newF := math.Float64frombits(old) + seconds
		if h.sumBits.CompareAndSwap(old, math.Float64bits(newF)) {
			break
		}
	}
	h.count.Add(1)
	for i, bound := range h.bounds {
		if seconds <= bound {
			for j := i; j < len(h.bounds); j++ {
				h.buckets[j].Add(1)
			}
			break
		}
	}
	h.buckets[len(h.bounds)].Add(1) // +Inf
}

// WriteHistogram writes a histogram in Prometheus text format.
func WriteHistogram(b *strings.Builder, h *Histogram) {
	sum := math.Float64frombits(h.sumBits.Load())
	count := h.count.Load()
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s histogram\n", h.name, helpEscaper.Replace(h.help), h.name)
	for i, bound := range h.bounds {
		fmt.Fprintf(b, "%s_bucket{le=\"%s\"} %d\n", h.name, FormatBound(bound), h.buckets[i].Load())
	}
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", h.name, h.buckets[len(h.bounds)].Load())
	fmt.Fprintf(b, "%s_sum %.6f\n", h.name, sum)
	fmt.Fprintf(b, "%s_count %d\n", h.name, count)
}

// LabeledHistogram tracks histograms per label combination.
type LabeledHistogram struct {
	vals   map[labelKey]*Histogram
	name   string
	help   string
	bounds []float64
	labels []string
	mu     sync.RWMutex
}

// NewLabeledHistogram creates a labeled histogram with the given name, help, and label names.
// By default it uses DefaultBuckets; use WithBuckets to override.
func NewLabeledHistogram(name, help string, labels []string, opts ...Option) *LabeledHistogram {
	cfg := histogramCfg{buckets: DefaultBuckets}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	validateMetricName(name)
	validateLabelNames(labels)
	if len(labels) > 4 {
		panic("metrics: LabeledHistogram supports at most 4 labels")
	}
	sorted := make([]float64, len(cfg.buckets))
	copy(sorted, cfg.buckets)
	sort.Float64s(sorted)
	return &LabeledHistogram{
		name:   name,
		help:   help,
		labels: labels,
		bounds: sorted,
		vals:   make(map[labelKey]*Histogram),
	}
}

// Observe records a value for the given label values.
func (lh *LabeledHistogram) Observe(seconds float64, labelVals ...string) {
	if len(labelVals) != len(lh.labels) {
		panic("metrics: label arity mismatch")
	}
	var key labelKey
	copy(key[:], labelVals)
	lh.mu.RLock()
	h, ok := lh.vals[key]
	lh.mu.RUnlock()
	if ok {
		h.Observe(seconds)
		return
	}
	lh.mu.Lock()
	if h, ok = lh.vals[key]; ok {
		lh.mu.Unlock()
		h.Observe(seconds)
		return
	}
	h = &Histogram{
		name:    lh.name,
		help:    lh.help,
		bounds:  lh.bounds,
		buckets: make([]atomic.Int64, len(lh.bounds)+1),
	}
	lh.vals[key] = h
	lh.mu.Unlock()
	h.Observe(seconds)
}

// WriteLabeledHistogram writes all child histograms in Prometheus text format.
func WriteLabeledHistogram(b *strings.Builder, lh *LabeledHistogram) {
	lh.mu.RLock()
	keys := make([]labelKey, 0, len(lh.vals))
	for k := range lh.vals {
		keys = append(keys, k)
	}
	lh.mu.RUnlock()
	if len(keys) == 0 {
		return
	}
	sort.Slice(keys, func(a, c int) bool {
		for i := range keys[a] {
			if keys[a][i] != keys[c][i] {
				return keys[a][i] < keys[c][i]
			}
		}
		return false
	})
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s histogram\n", lh.name, helpEscaper.Replace(lh.help), lh.name)
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
		fmt.Fprintf(b, "%s_sum{%s} %.6f\n", lh.name, labelStr, sum)
		fmt.Fprintf(b, "%s_count{%s} %d\n", lh.name, labelStr, count)
	}
}

// NewTimer returns a Timer that, on ObserveDuration, records the elapsed time
// into this labeled histogram with the given label values. This lets the
// common per-label latency case use Timer's defer-ObserveDuration ergonomics
// (plain NewTimer only composes with an unlabeled Histogram).
func (lh *LabeledHistogram) NewTimer(labelVals ...string) *Timer {
	return &Timer{start: time.Now(), observe: func(s float64) { lh.Observe(s, labelVals...) }}
}
