// Package metrics provides a hand-rolled Prometheus text-format exposition library.
// It requires only the Go standard library.
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var startTime = time.Now()

// Counter is a monotonically increasing counter.
type Counter struct {
	name string
	help string
	val  atomic.Int64
}

// NewCounter creates a named counter.
func NewCounter(name, help string) *Counter {
	return &Counter{name: name, help: help}
}

// Inc increments the counter by 1.
func (c *Counter) Inc() { c.val.Add(1) }

// Gauge is a value that can go up and down.
type Gauge struct {
	name string
	help string
	val  atomic.Int64
}

// NewGauge creates a named gauge.
func NewGauge(name, help string) *Gauge {
	return &Gauge{name: name, help: help}
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() { g.val.Add(1) }

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() { g.val.Add(-1) }

// labelKey is a fixed-size struct key for LabeledCounter, eliminating
// per-Inc string allocation from strings.Join on the hot path.
type labelKey [4]string

// LabeledCounter tracks counts per label combination.
type LabeledCounter struct {
	vals   map[labelKey]*atomic.Int64
	name   string
	help   string
	labels []string
	mu     sync.RWMutex
}

// NewLabeledCounter creates a labeled counter with the given label names.
func NewLabeledCounter(name, help string, labels []string) *LabeledCounter {
	return &LabeledCounter{
		name:   name,
		help:   help,
		labels: labels,
		vals:   make(map[labelKey]*atomic.Int64),
	}
}

// Inc increments the counter for the given label values.
func (lc *LabeledCounter) Inc(labelVals ...string) {
	var key labelKey
	copy(key[:], labelVals)
	lc.mu.RLock()
	v, ok := lc.vals[key]
	lc.mu.RUnlock()
	if ok {
		v.Add(1)
		return
	}
	lc.mu.Lock()
	if v, ok = lc.vals[key]; ok {
		lc.mu.Unlock()
		v.Add(1)
		return
	}
	v = &atomic.Int64{}
	v.Store(1)
	lc.vals[key] = v
	lc.mu.Unlock()
}

// Histogram tracks a distribution using cumulative buckets and atomic CAS for sum.
type Histogram struct {
	name    string
	help    string
	sumBits atomic.Uint64
	count   atomic.Int64
	buckets [len(DefaultBuckets) + 1]atomic.Int64
}

// DefaultBuckets are the default histogram bucket boundaries.
var DefaultBuckets = [8]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0}

// NewHistogram creates a named histogram.
func NewHistogram(name, help string) *Histogram {
	return &Histogram{name: name, help: help}
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
	for i, bound := range DefaultBuckets {
		if seconds <= bound {
			for j := i; j < len(DefaultBuckets); j++ {
				h.buckets[j].Add(1)
			}
			break
		}
	}
	h.buckets[len(DefaultBuckets)].Add(1)
}

// ImageMetric holds per-image gauge data set after each collect cycle.
type ImageMetric struct {
	Registry string
	Owner    string
	Repo     string
	Pulls    int64
	Tags     int
}

var (
	imageMetricsMu sync.RWMutex
	imageMetrics   []ImageMetric
)

// SetImageMetrics replaces the current image gauge data atomically.
func SetImageMetrics(images []ImageMetric) {
	imageMetricsMu.Lock()
	imageMetrics = images
	imageMetricsMu.Unlock()
}

// Registry holds a collection of metrics to be served.
type Registry struct {
	counters        []*Counter
	gauges          []*Gauge
	labeledCounters []*LabeledCounter
	prefix          string
	histograms      []*Histogram
	mu              sync.RWMutex
	showImages      bool
}

// NewRegistry creates a new metrics registry.
func NewRegistry(prefix string) *Registry {
	return &Registry{prefix: prefix}
}

// RegisterCounter adds a counter to the registry.
func (r *Registry) RegisterCounter(c *Counter) { r.mu.Lock(); r.counters = append(r.counters, c); r.mu.Unlock() }

// RegisterGauge adds a gauge to the registry.
func (r *Registry) RegisterGauge(g *Gauge) { r.mu.Lock(); r.gauges = append(r.gauges, g); r.mu.Unlock() }

// RegisterLabeledCounter adds a labeled counter to the registry.
func (r *Registry) RegisterLabeledCounter(lc *LabeledCounter) { r.mu.Lock(); r.labeledCounters = append(r.labeledCounters, lc); r.mu.Unlock() }

// RegisterHistogram adds a histogram to the registry.
func (r *Registry) RegisterHistogram(h *Histogram) { r.mu.Lock(); r.histograms = append(r.histograms, h); r.mu.Unlock() }

// EnableImageMetrics enables image metric output.
func (r *Registry) EnableImageMetrics() { r.mu.Lock(); r.showImages = true; r.mu.Unlock() }

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
		for _, g := range r.gauges {
			WriteGauge(&b, g)
		}
		for _, h := range r.histograms {
			WriteHistogram(&b, h)
		}
		showImages := r.showImages
		r.mu.RUnlock()
		if showImages {
			WriteImageMetrics(&b, r.prefix)
		}
		WriteProcessMetrics(&b)
		_, _ = w.Write([]byte(b.String()))
	}
}

// WriteCounter writes a counter in Prometheus text format.
func WriteCounter(b *strings.Builder, c *Counter) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n",
		c.name, c.help, c.name, c.name, c.val.Load())
}

// WriteGauge writes a gauge in Prometheus text format.
func WriteGauge(b *strings.Builder, g *Gauge) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n",
		g.name, g.help, g.name, g.name, g.val.Load())
}

// WriteLabeledCounter writes a labeled counter in Prometheus text format.
func WriteLabeledCounter(b *strings.Builder, lc *LabeledCounter) {
	lc.mu.RLock()
	keys := make([]labelKey, 0, len(lc.vals))
	for k := range lc.vals {
		keys = append(keys, k)
	}
	lc.mu.RUnlock()
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
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n", lc.name, lc.help, lc.name)
	for _, key := range keys {
		lc.mu.RLock()
		v := lc.vals[key]
		lc.mu.RUnlock()
		type lp struct {
			k, v string
		}
		pairs := make([]lp, len(lc.labels))
		for i, l := range lc.labels {
			pairs[i] = lp{l, key[i]}
		}
		sort.Slice(pairs, func(a, c int) bool { return pairs[a].k < pairs[c].k })
		var labelStr strings.Builder
		for i, p := range pairs {
			if i > 0 {
				labelStr.WriteByte(',')
			}
			fmt.Fprintf(&labelStr, "%s=%q", p.k, p.v)
		}
		fmt.Fprintf(b, "%s{%s} %d\n", lc.name, labelStr.String(), v.Load())
	}
}

// WriteHistogram writes a histogram in Prometheus text format.
func WriteHistogram(b *strings.Builder, h *Histogram) {
	sum := math.Float64frombits(h.sumBits.Load())
	count := h.count.Load()
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name)
	for i, bound := range DefaultBuckets {
		fmt.Fprintf(b, "%s_bucket{le=%q} %d\n", h.name, FormatBound(bound), h.buckets[i].Load())
	}
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", h.name, h.buckets[len(DefaultBuckets)].Load())
	fmt.Fprintf(b, "%s_sum %.6f\n", h.name, sum)
	fmt.Fprintf(b, "%s_count %d\n", h.name, count)
}

// FormatBound formats a bucket boundary for Prometheus output.
func FormatBound(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// WriteImageMetrics writes image gauge metrics in Prometheus text format.
func WriteImageMetrics(b *strings.Builder, prefix string) {
	imageMetricsMu.RLock()
	imgs := imageMetrics
	imageMetricsMu.RUnlock()
	if len(imgs) == 0 {
		return
	}
	pullsName := prefix + "_image_pulls_total"
	b.WriteString("# HELP " + pullsName + " Total pull count per image\n# TYPE " + pullsName + " gauge\n")
	for _, m := range imgs {
		fmt.Fprintf(b, "%s{registry=%q,owner=%q,repo=%q} %d\n", pullsName, m.Registry, m.Owner, m.Repo, m.Pulls)
	}
	hasTags := false
	for _, m := range imgs {
		if m.Tags > 0 {
			hasTags = true
			break
		}
	}
	if hasTags {
		tagsName := prefix + "_image_tags"
		b.WriteString("# HELP " + tagsName + " Number of tags per image\n# TYPE " + tagsName + " gauge\n")
		for _, m := range imgs {
			if m.Tags > 0 {
				fmt.Fprintf(b, "%s{registry=%q,owner=%q,repo=%q} %d\n", tagsName, m.Registry, m.Owner, m.Repo, m.Tags)
			}
		}
	}
}

// WriteProcessMetrics writes Go runtime process metrics.
func WriteProcessMetrics(b *strings.Builder) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Fprintf(b, "# HELP process_goroutines Number of goroutines\n# TYPE process_goroutines gauge\nprocess_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(b, "# HELP process_heap_bytes Heap memory in use\n# TYPE process_heap_bytes gauge\nprocess_heap_bytes %d\n", m.HeapAlloc)
	fmt.Fprintf(b, "# HELP process_gc_pause_seconds_total Total GC pause time\n# TYPE process_gc_pause_seconds_total counter\nprocess_gc_pause_seconds_total %.6f\n", float64(m.PauseTotalNs)/1e9)
	fmt.Fprintf(b, "# HELP process_uptime_seconds Process uptime\n# TYPE process_uptime_seconds gauge\nprocess_uptime_seconds %.3f\n", time.Since(startTime).Seconds())
}
