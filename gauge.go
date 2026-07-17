package metrics

import (
	"math"
	"strings"
	"sync"
	"sync/atomic"
)

// Gauge is a value that can go up and down (float64).
type Gauge struct {
	name       string
	help       string
	bits       atomic.Uint64
	registered atomic.Bool
}

// NewGauge creates a named gauge.
func NewGauge(name, help string) *Gauge {
	validateMetricName(name)
	help = sanitizeHelp(name, help)
	return &Gauge{name: name, help: help}
}

// Set sets the gauge to an arbitrary float64 value.
func (g *Gauge) Set(v float64) { g.bits.Store(math.Float64bits(v)) }

// addFloatBits atomically adds delta to the float64 stored as IEEE-754 bits
// in u via a compare-and-swap loop -- the package's canonical lock-free float
// accumulate, shared by Gauge.Add and Histogram.Observe's sum update.
func addFloatBits(u *atomic.Uint64, delta float64) {
	for {
		old := u.Load()
		if u.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+delta)) {
			return
		}
	}
}

// Add adds a float64 delta to the gauge.
func (g *Gauge) Add(delta float64) { addFloatBits(&g.bits, delta) }

// Sub subtracts a float64 delta from the gauge.
func (g *Gauge) Sub(delta float64) { g.Add(-delta) }

// Inc increments the gauge by 1.
func (g *Gauge) Inc() { g.Add(1) }

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() { g.Add(-1) }

// Get returns the current gauge value.
func (g *Gauge) Get() float64 { return math.Float64frombits(g.bits.Load()) }

// WriteGauge writes a gauge in Prometheus text format (IR shim).
func WriteGauge(b *strings.Builder, g *Gauge) {
	appendPrometheus(b, []metricFamily{g.family()})
}

// LabeledGauge tracks gauges per label combination.
type LabeledGauge struct {
	vals       map[labelKey]*atomic.Uint64
	name       string
	help       string
	labels     []string
	registered atomic.Bool
	mu         sync.RWMutex
}

// NewLabeledGauge creates a labeled gauge.
func NewLabeledGauge(name, help string, labels []string) *LabeledGauge {
	validateMetricName(name)
	help = sanitizeHelp(name, help)
	labels = validateLabelNames(labels)
	if len(labels) > maxLabels {
		panic("metrics: LabeledGauge supports at most 8 labels")
	}
	return &LabeledGauge{
		name:   name,
		help:   help,
		labels: labels,
		vals:   make(map[labelKey]*atomic.Uint64),
	}
}

// Set sets the gauge for the given label values.
func (lg *LabeledGauge) Set(v float64, labelVals ...string) {
	key := labelKeyFor(lg.labels, labelVals)
	if ptr, loaded := loadOrStore(&lg.mu, lg.vals, &lg.name, &key,
		func() *atomic.Uint64 { u := &atomic.Uint64{}; u.Store(math.Float64bits(v)); return u }); loaded {
		ptr.Store(math.Float64bits(v))
	}
}

// Reset removes all label combinations from the gauge.
func (lg *LabeledGauge) Reset() {
	lg.mu.Lock()
	clear(lg.vals)
	lg.mu.Unlock()
}

// Delete removes a single label combination from the gauge.
// It panics if the number of label values does not match the label count.
// Label values are sanitized to valid UTF-8 the same way recording sanitizes
// them, so Delete called with the original raw values removes the series
// recording created.
func (lg *LabeledGauge) Delete(labelVals ...string) {
	deleteSeries(&lg.mu, lg.vals, lg.labels, labelVals)
}

// WriteLabeledGauge writes a labeled gauge in Prometheus text format (IR shim).
func WriteLabeledGauge(b *strings.Builder, lg *LabeledGauge) {
	if f, ok := lg.family(); ok {
		appendPrometheus(b, []metricFamily{f})
	}
}
