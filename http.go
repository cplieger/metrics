package metrics

import (
	"net/http"
	"time"
)

// StatusRecorder wraps an http.ResponseWriter to capture the response status
// code for instrumentation. Construct via NewStatusRecorder. It implements
// Unwrap (Go 1.20+ http.ResponseController convention) so the underlying
// writer's Flusher/Hijacker remain reachable for streaming/upgrade handlers.
type StatusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

// NewStatusRecorder wraps w; the recorded status defaults to 200 until the
// handler calls WriteHeader or Write.
func NewStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: w, status: http.StatusOK}
}

// Status returns the captured status code (200 if the handler set none).
func (s *StatusRecorder) Status() int { return s.status }

// WriteHeader records the status once, then forwards it.
func (s *StatusRecorder) WriteHeader(code int) {
	if !s.written {
		s.status = code
		s.written = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write marks the response as written (implicit 200 if WriteHeader was not
// called) and forwards the bytes.
func (s *StatusRecorder) Write(b []byte) (int, error) {
	s.written = true
	return s.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying writer so http.NewResponseController can reach
// optional interfaces (Flusher for SSE, Hijacker for WebSocket upgrades).
func (s *StatusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// RecordHTTP records one HTTP request into the caller-supplied counter and
// histogram. The caller owns the label set (arity, ordering, and any path
// templating); labelVals must match c's label names. h receives d.Seconds().
// Either metric may be nil to skip it.
func RecordHTTP(c *LabeledCounter, h *Histogram, d time.Duration, labelVals ...string) {
	if c != nil {
		c.Inc(labelVals...)
	}
	if h != nil {
		h.Observe(d.Seconds())
	}
}

// InstrumentHandler wraps next so each request records into c and h via
// RecordHTTP. labelValues maps a request and its final status to the label
// values matching c's label names, keeping arity/ordering/path-templating
// caller-owned. Middleware that also needs request-id, access logging, or
// path-skip handling should instead use StatusRecorder + RecordHTTP directly
// inside that middleware rather than this wrapper.
func InstrumentHandler(next http.Handler, c *LabeledCounter, h *Histogram, labelValues func(r *http.Request, status int) []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := NewStatusRecorder(w)
		start := time.Now()
		next.ServeHTTP(rec, r)
		RecordHTTP(c, h, time.Since(start), labelValues(r, rec.Status())...)
	})
}
