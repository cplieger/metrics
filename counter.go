package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing counter.
type Counter struct {
	name string
	help string
	val  atomic.Int64
}

// NewCounter creates a named counter.
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
	if len(labelVals) != len(lc.labels) {
		panic("metrics: label arity mismatch")
	}
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

// WriteCounter writes a counter in Prometheus text format.
func WriteCounter(b *strings.Builder, c *Counter) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n",
		c.name, helpEscaper.Replace(c.help), c.name, c.name, c.val.Load())
}

// buildLabelString builds a sorted, spec-escaped label string from labels and key.
func buildLabelString(labels []string, key labelKey) string {
	type lp struct{ k, v string }
	pairs := make([]lp, len(labels))
	for i, l := range labels {
		pairs[i] = lp{l, key[i]}
	}
	sort.Slice(pairs, func(a, c int) bool { return pairs[a].k < pairs[c].k })
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
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n", lc.name, helpEscaper.Replace(lc.help), lc.name)
	for _, key := range keys {
		lc.mu.RLock()
		v := lc.vals[key]
		lc.mu.RUnlock()
		labelStr := buildLabelString(lc.labels, key)
		fmt.Fprintf(b, "%s{%s} %d\n", lc.name, labelStr, v.Load())
	}
}
