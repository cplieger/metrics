package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Gauge is a value that can go up and down (float64).
type Gauge struct {
	name string
	help string
	bits atomic.Uint64
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
	v := g.Get()
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n", g.name, helpEscaper.Replace(g.help), g.name)
	if v == float64(int64(v)) && !math.IsInf(v, 0) && !math.IsNaN(v) {
		fmt.Fprintf(b, "%s %d\n", g.name, int64(v))
	} else {
		fmt.Fprintf(b, "%s %g\n", g.name, v)
	}
}

// LabeledGauge tracks gauges per label combination.
type LabeledGauge struct {
	vals   map[labelKey]*atomic.Uint64
	name   string
	help   string
	labels []string
	mu     sync.RWMutex
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
	if len(labelVals) != len(lg.labels) {
		panic("metrics: label arity mismatch")
	}
	var key labelKey
	copy(key[:], labelVals)
	lg.mu.RLock()
	ptr, ok := lg.vals[key]
	lg.mu.RUnlock()
	if ok {
		ptr.Store(math.Float64bits(v))
		return
	}
	lg.mu.Lock()
	if ptr, ok = lg.vals[key]; ok {
		lg.mu.Unlock()
		ptr.Store(math.Float64bits(v))
		return
	}
	ptr = &atomic.Uint64{}
	ptr.Store(math.Float64bits(v))
	lg.vals[key] = ptr
	lg.mu.Unlock()
}

// WriteLabeledGauge writes a labeled gauge in Prometheus text format.
func WriteLabeledGauge(b *strings.Builder, lg *LabeledGauge) {
	lg.mu.RLock()
	keys := make([]labelKey, 0, len(lg.vals))
	for k := range lg.vals {
		keys = append(keys, k)
	}
	lg.mu.RUnlock()
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
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n", lg.name, helpEscaper.Replace(lg.help), lg.name)
	for _, key := range keys {
		lg.mu.RLock()
		ptr := lg.vals[key]
		lg.mu.RUnlock()
		v := math.Float64frombits(ptr.Load())
		labelStr := buildLabelString(lg.labels, key)
		if v == float64(int64(v)) && !math.IsInf(v, 0) && !math.IsNaN(v) {
			fmt.Fprintf(b, "%s{%s} %d\n", lg.name, labelStr, int64(v))
		} else {
			fmt.Fprintf(b, "%s{%s} %g\n", lg.name, labelStr, v)
		}
	}
}
