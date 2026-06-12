package metrics

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing counter.
type Counter struct {
	name       string
	help       string
	val        atomic.Int64
	registered atomic.Bool
}

// NewCounter creates a named counter. Per Prometheus/OpenMetrics convention the name
// should end in `_total` (e.g. "http_requests_total"). OpenMetrics output always appends
// `_total` to the sample name, so a counter NOT named with `_total` is exposed under a
// different series name in Prometheus format (raw name) than in OpenMetrics format
// (name+"_total"). Naming counters with `_total` keeps both formats consistent.
func NewCounter(name, help string) *Counter {
	validateMetricName(name)
	return &Counter{name: name, help: help}
}

// Inc increments the counter by 1.
func (c *Counter) Inc() { c.val.Add(1) }

// Add increments the counter by n. Panics if n < 0.
func (c *Counter) Add(n int64) {
	if n < 0 {
		panic("metrics: Counter.Add called with negative value")
	}
	c.val.Add(n)
}

// labelKey is a fixed-size struct key for labeled metrics.
type labelKey [4]string

// labelKeyFor validates arity and packs label values into a fixed-size key.
func labelKeyFor(labels, labelVals []string) labelKey {
	if len(labelVals) != len(labels) {
		panic("metrics: label arity mismatch")
	}
	var key labelKey
	copy(key[:], labelVals)
	return key
}

// LabeledCounter tracks counts per label combination.
type LabeledCounter struct {
	vals       map[labelKey]*atomic.Int64
	name       string
	help       string
	labels     []string
	registered atomic.Bool
	mu         sync.RWMutex
}

// NewLabeledCounter creates a labeled counter with the given label names. As with
// NewCounter, name should end in `_total` so the Prometheus and OpenMetrics series names
// stay consistent (OpenMetrics always appends `_total` to the sample name).
func NewLabeledCounter(name, help string, labels []string) *LabeledCounter {
	validateMetricName(name)
	validateLabelNames(labels)
	if len(labels) > 4 {
		panic("metrics: LabeledCounter supports at most 4 labels")
	}
	return &LabeledCounter{
		name:   name,
		help:   help,
		labels: labels,
		vals:   make(map[labelKey]*atomic.Int64),
	}
}

// Inc increments the counter for the given label values.
func (lc *LabeledCounter) Inc(labelVals ...string) {
	lc.Add(1, labelVals...)
}

// Add increments the counter for the given label values by n. Panics if n < 0.
func (lc *LabeledCounter) Add(n int64, labelVals ...string) {
	if n < 0 {
		panic("metrics: LabeledCounter.Add called with negative value")
	}
	key := labelKeyFor(lc.labels, labelVals)
	if v, loaded := loadOrStore(&lc.mu, lc.vals, key,
		func() *atomic.Int64 { a := &atomic.Int64{}; a.Store(n); return a }); loaded {
		v.Add(n)
	}
}

// Reset removes all label combinations from the counter.
func (lc *LabeledCounter) Reset() {
	lc.mu.Lock()
	clear(lc.vals)
	lc.mu.Unlock()
}

// Delete removes a single label combination from the counter.
// It panics if the number of label values does not match the label count.
func (lc *LabeledCounter) Delete(labelVals ...string) {
	key := labelKeyFor(lc.labels, labelVals)
	lc.mu.Lock()
	delete(lc.vals, key)
	lc.mu.Unlock()
}

// WriteCounter writes a counter in Prometheus text format.
func WriteCounter(b *strings.Builder, c *Counter) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n",
		c.name, helpEscaper.Replace(c.help), c.name, c.name, c.val.Load())
}

// loadOrStore returns the entry for key, creating it with makeV under the
// write lock when absent. loaded reports whether the entry already existed.
func loadOrStore[V any](mu *sync.RWMutex, m map[labelKey]V, key labelKey, makeV func() V) (v V, loaded bool) {
	mu.RLock()
	v, loaded = m[key]
	mu.RUnlock()
	if loaded {
		return v, true
	}
	mu.Lock()
	defer mu.Unlock()
	if v, loaded = m[key]; loaded {
		return v, true
	}
	v = makeV()
	m[key] = v
	return v, false
}

// sortLabelKeys sorts label keys lexicographically.
func sortLabelKeys(keys []labelKey) {
	slices.SortFunc(keys, func(a, b labelKey) int { return slices.Compare(a[:], b[:]) })
}

// sortedLabelKeys snapshots the keys of vals under mu.RLock and returns
// them sorted lexicographically. Callers hold no lock on entry.
func sortedLabelKeys[V any](mu *sync.RWMutex, vals map[labelKey]V) []labelKey {
	mu.RLock()
	keys := make([]labelKey, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	mu.RUnlock()
	sortLabelKeys(keys)
	return keys
}

// buildLabelString builds a sorted, spec-escaped label string from labels and key.
func buildLabelString(labels []string, key labelKey) string {
	type lp struct{ k, v string }
	pairs := make([]lp, len(labels))
	for i, l := range labels {
		pairs[i] = lp{l, key[i]}
	}
	slices.SortFunc(pairs, func(a, b lp) int { return strings.Compare(a.k, b.k) })
	var sb strings.Builder
	for i, p := range pairs {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(p.k)
		sb.WriteString(`="`)
		_, _ = labelEscaper.WriteString(&sb, p.v)
		sb.WriteByte('"')
	}
	return sb.String()
}

// WriteLabeledCounter writes a labeled counter in Prometheus text format.
func WriteLabeledCounter(b *strings.Builder, lc *LabeledCounter) {
	keys := sortedLabelKeys(&lc.mu, lc.vals)
	if len(keys) == 0 {
		return
	}
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n", lc.name, helpEscaper.Replace(lc.help), lc.name)
	for _, key := range keys {
		lc.mu.RLock()
		v := lc.vals[key]
		lc.mu.RUnlock()
		if v == nil {
			continue
		}
		labelStr := buildLabelString(lc.labels, key)
		fmt.Fprintf(b, "%s{%s} %d\n", lc.name, labelStr, v.Load())
	}
}
