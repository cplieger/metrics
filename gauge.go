package metrics

import (
	"fmt"
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
	return &Gauge{name: name, help: help}
}

// Set sets the gauge to an arbitrary float64 value.
func (g *Gauge) Set(v float64) { g.bits.Store(math.Float64bits(v)) }

// Add adds a float64 delta to the gauge.
func (g *Gauge) Add(delta float64) {
	for {
		old := g.bits.Load()
		newV := math.Float64frombits(old) + delta
		if g.bits.CompareAndSwap(old, math.Float64bits(newV)) {
			return
		}
	}
}

// Sub subtracts a float64 delta from the gauge.
func (g *Gauge) Sub(delta float64) { g.Add(-delta) }

// Inc increments the gauge by 1.
func (g *Gauge) Inc() { g.Add(1) }

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() { g.Add(-1) }

// Get returns the current gauge value.
func (g *Gauge) Get() float64 { return math.Float64frombits(g.bits.Load()) }

// WriteGauge writes a gauge in Prometheus text format.
func WriteGauge(b *strings.Builder, g *Gauge) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n", g.name, helpEscaper.Replace(g.help), g.name)
	fmt.Fprintf(b, "%s %s\n", g.name, formatValue(g.Get()))
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
	validateLabelNames(labels)
	if len(labels) > 4 {
		panic("metrics: LabeledGauge supports at most 4 labels")
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
	if ptr, loaded := loadOrStore(&lg.mu, lg.vals, key,
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
func (lg *LabeledGauge) Delete(labelVals ...string) {
	key := labelKeyFor(lg.labels, labelVals)
	lg.mu.Lock()
	delete(lg.vals, key)
	lg.mu.Unlock()
}

// WriteLabeledGauge writes a labeled gauge in Prometheus text format.
func WriteLabeledGauge(b *strings.Builder, lg *LabeledGauge) {
	keys := sortedLabelKeys(&lg.mu, lg.vals)
	if len(keys) == 0 {
		return
	}
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n", lg.name, helpEscaper.Replace(lg.help), lg.name)
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
