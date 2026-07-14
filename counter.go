package metrics

import (
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// cardinalityWarnThreshold is the per-metric series count at which loadOrStore
// emits its one-time label-cardinality warning. Log-only: no cap or rejection
// is imposed and recording behavior is unchanged; the warning turns silent
// unbounded series growth (e.g. a caller using raw request paths as a label
// value) into an actionable, alertable signal before memory is exhausted.
const cardinalityWarnThreshold = 1000

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
	rejectTotalOnlyCounterName(name)
	help = sanitizeHelp(name, help)
	return &Counter{name: name, help: help}
}

// rejectTotalOnlyCounterName panics when a counter is named exactly "_total".
// Stripping the conventional suffix leaves an empty base name, so the
// OpenMetrics encoding cannot be conformant (the sample name would equal the
// family name). Counters are the only metric type with this restriction.
func rejectTotalOnlyCounterName(name string) {
	if name == "_total" {
		panic(`metrics: counter name "_total" has an empty base name; OpenMetrics cannot encode it`)
	}
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
	rejectTotalOnlyCounterName(name)
	help = sanitizeHelp(name, help)
	labels = validateLabelNames(labels)
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
	if v, loaded := loadOrStore(&lc.mu, lc.vals, &lc.name, key,
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
// Label values are sanitized to valid UTF-8 the same way recording sanitizes
// them, so Delete called with the original raw values removes the series
// recording created.
func (lc *LabeledCounter) Delete(labelVals ...string) {
	deleteSeries(&lc.mu, lc.vals, lc.labels, labelVals)
}

// WriteCounter writes a counter in Prometheus text format. It is a thin shim
// over the neutral IR (Counter.family) and the Prometheus encoder, retained as
// part of the package's exported surface.
func WriteCounter(b *strings.Builder, c *Counter) {
	appendPrometheus(b, []metricFamily{c.family()})
}

// loadOrStore returns the entry for key, creating it with makeV under the
// write lock when absent. loaded reports whether the entry already existed.
// name points at the owning metric's name field; it is taken by pointer so the
// hot loaded-path never reads it. Label values carrying invalid UTF-8 are
// sanitized with U+FFFD on the series-creation path (see storeNewSeries), and
// when storing a new series pushes the map exactly to
// cardinalityWarnThreshold, a one-time warning names the metric so a
// label-cardinality explosion is observable before it exhausts memory. All
// warning captures (the metric name, the representative sanitized value)
// happen under the lock in storeNewSeries, so exactly one inserter observes
// each event and the name read is synchronized with the mutex-guarded rename
// in Register*; the warnings themselves are emitted AFTER the write lock is
// released so arbitrary slog handler code never runs under the metric lock.
func loadOrStore[V any](mu *sync.RWMutex, m map[labelKey]V, name *string, key labelKey, makeV func() V) (v V, loaded bool) {
	mu.RLock()
	v, loaded = m[key]
	mu.RUnlock()
	if loaded {
		return v, true
	}
	var w seriesWarnings
	v, loaded, w = storeNewSeries(mu, m, name, key, makeV)
	if w.san {
		slog.Warn("metrics: label value contained invalid UTF-8; sanitized with U+FFFD",
			"metric", w.name, "value", w.sanValue)
	}
	if w.card {
		slog.Warn("metrics: labeled metric series count crossed threshold; possible label-cardinality explosion",
			"metric", w.name, "series", cardinalityWarnThreshold)
	}
	return v, loaded
}

// seriesWarnings carries the warning captures storeNewSeries takes under the
// write lock so loadOrStore can emit the corresponding slog warnings after the
// lock is released.
type seriesWarnings struct {
	name     string // metric name, captured under the lock when any warning fires
	sanValue string // representative sanitized label value, truncated for logging
	san      bool   // sanitization created a new series
	card     bool   // this insert pushed the map exactly to cardinalityWarnThreshold
}

// sanitizeLabelKey runs sanitizeUTF8 over each of key's values. It returns the
// sanitized key, one representative sanitized value (truncated via
// maxLogValueLen for logging), and whether any value changed.
func sanitizeLabelKey(key labelKey) (labelKey, string, bool) {
	var rep string
	changed := false
	for i, v := range key {
		san, ch := sanitizeUTF8(v)
		if !ch {
			continue
		}
		key[i] = san
		if !changed {
			rep = truncateForLog(san)
		}
		changed = true
	}
	return key, rep, changed
}

// storeNewSeries sanitizes key's label values to valid UTF-8 and inserts the
// sanitized key under the write lock (double-checked). The raw key is never
// stored, so records carrying invalid UTF-8 always miss loadOrStore's RLock
// fast path and re-take this slow path, landing on the same sanitized series —
// deliberate: degraded input gets the slow path. Distinct raw values may merge
// into one sanitized series — also deliberate. The returned seriesWarnings is
// captured under the lock (the metric name reads are synchronized with the
// mutex-guarded rename in Register*): w.san fires only when a sanitization
// actually CREATED a new series (the double-check-found path never warns), and
// w.card fires when this insert pushed the map exactly to
// cardinalityWarnThreshold. The defer keeps the unlock on a makeV panic path.
func storeNewSeries[V any](mu *sync.RWMutex, m map[labelKey]V, name *string, key labelKey, makeV func() V) (v V, loaded bool, w seriesWarnings) {
	sanKey, sanValue, sanitized := sanitizeLabelKey(key)
	mu.Lock()
	defer mu.Unlock()
	if v, loaded = m[sanKey]; loaded {
		return v, true, seriesWarnings{}
	}
	v = makeV()
	m[sanKey] = v
	if sanitized {
		w.san = true
		w.sanValue = sanValue
	}
	if len(m) == cardinalityWarnThreshold {
		w.card = true
	}
	if w.san || w.card {
		w.name = *name
	}
	return v, false, w
}

// deleteSeries removes the series for labelVals from m under the write lock.
// The lookup key is sanitized to valid UTF-8 exactly as recording sanitizes it
// (sanitizeLabelKey), so Delete called with the original raw values removes
// the series recording created. Shared by the three labeled Delete methods.
func deleteSeries[V any](mu *sync.RWMutex, m map[labelKey]V, labels, labelVals []string) {
	key := labelKeyFor(labels, labelVals)
	key, _, _ = sanitizeLabelKey(key)
	mu.Lock()
	delete(m, key)
	mu.Unlock()
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

// WriteLabeledCounter writes a labeled counter in Prometheus text format (IR shim).
func WriteLabeledCounter(b *strings.Builder, lc *LabeledCounter) {
	if f, ok := lc.family(); ok {
		appendPrometheus(b, []metricFamily{f})
	}
}
