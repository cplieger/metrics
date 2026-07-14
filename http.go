package metrics

import "time"

// RecordHTTP records one HTTP request into the caller-supplied counter and
// histogram. The caller owns the label set (arity, ordering, and any path
// templating); labelVals must match c's label names. h receives d.Seconds().
// Either metric may be nil to skip it.
//
// Label values must come from a bounded set: each distinct combination
// allocates a series retained until Delete/Reset, so raw paths or header
// values allow unbounded memory growth.
//
// This is the metrics-side hook only; pair it with middleware that captures
// the response status, such as webhttp's StatusRecorder (webhttp.Logging's
// WithRecordMetric calls this hook).
func RecordHTTP(c *LabeledCounter, h *Histogram, d time.Duration, labelVals ...string) {
	if c != nil {
		c.Inc(labelVals...)
	}
	if h != nil {
		h.Observe(d.Seconds())
	}
}
