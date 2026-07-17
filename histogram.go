package metrics

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultBuckets returns the default histogram bucket boundaries (HTTP
// latency). Each call returns a fresh slice, so callers cannot mutate the
// defaults process-wide.
func DefaultBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0}
}

// APIBuckets returns coarse latency buckets (seconds) for outbound API calls
// and slow collect/scan cycles, where DefaultBuckets (max 1.0s) saturates at
// +Inf. Each call returns a fresh slice, so callers cannot mutate the
// boundaries process-wide.
func APIBuckets() []float64 {
	return []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
}

// histogramCfg holds optional configuration for histogram construction.
type histogramCfg struct {
	buckets    []float64
	bucketsSet bool
}

// Option configures optional histogram parameters.
type Option func(*histogramCfg)

// WithBuckets returns an Option that sets custom bucket boundaries for a
// histogram. An empty (or nil) slice yields a histogram with only the implicit
// +Inf bucket.
func WithBuckets(buckets []float64) Option {
	return func(cfg *histogramCfg) {
		cfg.buckets = buckets
		cfg.bucketsSet = true
	}
}

// validateBuckets enforces the histogram bucket contract: bounds must be a
// strictly increasing sequence of finite values. The writers append the
// implicit le="+Inf" bucket, so callers must not include +Inf (nor any other
// non-finite value), and duplicate or out-of-order bounds would emit duplicate
// or non-monotonic le series that Prometheus parsers reject, dropping the
// whole scrape. Bucket bounds are fixed at construction by the programmer, so
// a violation is a programmer error and panics, consistent with
// validateMetricName. The empty bound set is valid: it yields a histogram with
// only the implicit +Inf bucket.
func validateBuckets(bounds []float64) {
	for i, b := range bounds {
		if math.IsNaN(b) || math.IsInf(b, 0) {
			panic(fmt.Sprintf("metrics: histogram bucket bound must be finite, got %v", b))
		}
		if i > 0 && b <= bounds[i-1] {
			panic(fmt.Sprintf("metrics: histogram bucket bounds must be strictly increasing, got %v after %v", b, bounds[i-1]))
		}
	}
}

// resolveBuckets applies the histogram options, falling back to
// DefaultBuckets when no WithBuckets option was supplied, validates the
// resulting bounds, and returns an owned clone. Shared by NewHistogram and
// NewLabeledHistogram so the default-seed and nil-option-skip invariant is
// single-sourced.
func resolveBuckets(opts []Option) []float64 {
	var cfg histogramCfg
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if !cfg.bucketsSet {
		cfg.buckets = DefaultBuckets()
	}
	validateBuckets(cfg.buckets)
	return slices.Clone(cfg.buckets)
}

// Histogram tracks a distribution using cumulative buckets and atomic CAS for sum.
type Histogram struct {
	name       string
	help       string
	bounds     []float64
	buckets    []atomic.Int64
	sumBits    atomic.Uint64
	count      atomic.Int64
	registered atomic.Bool
	// mu is used with inverted RWMutex semantics: Observe holds RLock (writers
	// mutate sum/count/buckets via atomics, so they run concurrently), while
	// snapshot holds the exclusive Lock to read a consistent view with no
	// Observe in flight. Do not swap these: a Lock in Observe serializes the
	// hot path; an RLock in snapshot reintroduces torn count/bucket reads.
	mu sync.RWMutex
}

// NewHistogram creates a histogram with the given name and help text.
// By default it uses DefaultBuckets; use WithBuckets to override.
func NewHistogram(name, help string, opts ...Option) *Histogram {
	validateMetricName(name)
	help = sanitizeHelp(name, help)
	bounds := resolveBuckets(opts)
	return &Histogram{
		name:    name,
		help:    help,
		bounds:  bounds,
		buckets: make([]atomic.Int64, len(bounds)+1),
	}
}

// Observe records a value in the histogram.
func (h *Histogram) Observe(seconds float64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	addFloatBits(&h.sumBits, seconds)
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

// snapshot atomically reads sum, count, and per-bucket counts under the
// histogram lock. Shared by the histogram family builders and writers.
func (h *Histogram) snapshot() (sum float64, count int64, bucketVals []int64) {
	h.mu.Lock()
	sum = math.Float64frombits(h.sumBits.Load())
	count = h.count.Load()
	bucketVals = make([]int64, len(h.buckets))
	for i := range h.buckets {
		bucketVals[i] = h.buckets[i].Load()
	}
	h.mu.Unlock()
	return sum, count, bucketVals
}

// WriteHistogram writes a histogram in Prometheus text format (IR shim).
func WriteHistogram(b *strings.Builder, h *Histogram) {
	appendPrometheus(b, []metricFamily{h.family()})
}

// LabeledHistogram tracks histograms per label combination.
type LabeledHistogram struct {
	vals       map[labelKey]*Histogram
	name       string
	help       string
	bounds     []float64
	labels     []string
	registered atomic.Bool
	mu         sync.RWMutex
}

// NewLabeledHistogram creates a labeled histogram with the given name, help, and label names.
// By default it uses DefaultBuckets; use WithBuckets to override.
func NewLabeledHistogram(name, help string, labels []string, opts ...Option) *LabeledHistogram {
	validateMetricName(name)
	help = sanitizeHelp(name, help)
	labels = validateLabelNames(labels)
	for _, l := range labels {
		if l == "le" {
			panic(`metrics: LabeledHistogram label name "le" is reserved for the bucket bound`)
		}
	}
	if len(labels) > maxLabels {
		panic("metrics: LabeledHistogram supports at most 8 labels")
	}
	return &LabeledHistogram{
		name:   name,
		help:   help,
		labels: labels,
		bounds: resolveBuckets(opts),
		vals:   make(map[labelKey]*Histogram),
	}
}

// Observe records a value for the given label values.
func (lh *LabeledHistogram) Observe(seconds float64, labelVals ...string) {
	key := labelKeyFor(lh.labels, labelVals)
	h, _ := loadOrStore(&lh.mu, lh.vals, &lh.name, &key, func() *Histogram {
		return &Histogram{
			name:    lh.name,
			help:    lh.help,
			bounds:  lh.bounds,
			buckets: make([]atomic.Int64, len(lh.bounds)+1),
		}
	})
	h.Observe(seconds)
}

// Reset removes all label combinations from the histogram.
func (lh *LabeledHistogram) Reset() {
	lh.mu.Lock()
	clear(lh.vals)
	lh.mu.Unlock()
}

// Delete removes a single label combination from the histogram.
// It panics if the number of label values does not match the label count.
// Label values are sanitized to valid UTF-8 the same way recording sanitizes
// them, so Delete called with the original raw values removes the series
// recording created.
func (lh *LabeledHistogram) Delete(labelVals ...string) {
	deleteSeries(&lh.mu, lh.vals, lh.labels, labelVals)
}

// WriteLabeledHistogram writes all child histograms in Prometheus text format (IR shim).
func WriteLabeledHistogram(b *strings.Builder, lh *LabeledHistogram) {
	if f, ok := lh.family(); ok {
		appendPrometheus(b, []metricFamily{f})
	}
}

// Timer measures elapsed time and reports to a Histogram.
type Timer struct {
	start   time.Time
	observe func(float64)
}

// NewTimer starts a timer that will observe into the given histogram.
func NewTimer(h *Histogram) *Timer {
	return &Timer{start: time.Now(), observe: h.Observe}
}

// ObserveDuration records the elapsed time since the timer was created.
func (t *Timer) ObserveDuration() time.Duration {
	d := time.Since(t.start)
	t.observe(d.Seconds())
	return d
}

// NewTimer returns a Timer that, on ObserveDuration, records the elapsed time
// into this labeled histogram with the given label values. This lets the
// common per-label latency case use Timer's defer-ObserveDuration ergonomics
// (plain NewTimer only composes with an unlabeled Histogram).
func (lh *LabeledHistogram) NewTimer(labelVals ...string) *Timer {
	// Fail fast on arity mismatch at construction (labelKeyFor panics), so a
	// wrong-arity timer surfaces where it is created rather than inside a
	// deferred ObserveDuration.
	_ = labelKeyFor(lh.labels, labelVals)
	vals := slices.Clone(labelVals)
	return &Timer{start: time.Now(), observe: func(s float64) { lh.Observe(s, vals...) }}
}
