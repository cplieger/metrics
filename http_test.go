package metrics

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStatusRecorderCaptures(t *testing.T) {
	rec := NewStatusRecorder(httptest.NewRecorder())
	if rec.Status() != http.StatusOK {
		t.Errorf("default status = %d, want 200", rec.Status())
	}
	rec.WriteHeader(http.StatusTeapot)
	rec.WriteHeader(http.StatusInternalServerError) // second call ignored
	if rec.Status() != http.StatusTeapot {
		t.Errorf("status = %d, want 418 (first WriteHeader wins)", rec.Status())
	}
}

func TestStatusRecorderImplicitOK(t *testing.T) {
	rec := NewStatusRecorder(httptest.NewRecorder())
	_, _ = rec.Write([]byte("hi"))
	if rec.Status() != http.StatusOK {
		t.Errorf("Write without WriteHeader = %d, want 200", rec.Status())
	}
}

func TestRecordHTTPNilSafe(t *testing.T) {
	RecordHTTP(nil, nil, time.Second) // must not panic
	c := NewLabeledCounter("req_total", "", []string{"method", "status"})
	h := NewHistogram("req_seconds", "")
	RecordHTTP(c, h, 250*time.Millisecond, "GET", "200")
	r := NewRegistry("")
	r.RegisterLabeledCounter(c)
	r.RegisterHistogram(h)
	out := body(t, r)
	if !strings.Contains(out, `req_total{method="GET",status="200"} 1`) {
		t.Errorf("RecordHTTP did not record counter:\n%s", out)
	}
	if !strings.Contains(out, "req_seconds_count 1") {
		t.Errorf("RecordHTTP did not record histogram:\n%s", out)
	}
}

func TestInstrumentHandler(t *testing.T) {
	c := NewLabeledCounter("http_requests_total", "", []string{"method", "status"})
	h := NewHistogram("http_request_duration_seconds", "")
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	h2 := InstrumentHandler(next, c, h, func(r *http.Request, status int) []string {
		return []string{r.Method, strconv.Itoa(status)}
	})
	w := httptest.NewRecorder()
	h2.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/x", nil))
	if w.Code != http.StatusCreated {
		t.Errorf("status passthrough = %d, want 201", w.Code)
	}
	r := NewRegistry("")
	r.RegisterLabeledCounter(c)
	r.RegisterHistogram(h)
	if out := body(t, r); !strings.Contains(out, `http_requests_total{method="POST",status="201"} 1`) {
		t.Errorf("InstrumentHandler did not record labeled request:\n%s", out)
	}
}

func TestStatusRecorder_Unwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := NewStatusRecorder(inner)
	if got := rec.Unwrap(); got != inner {
		t.Errorf("Unwrap() = %v, want underlying recorder", got)
	}
}

func TestStatusRecorder_UnwrapExposesFlusher(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := NewStatusRecorder(inner)
	rc := http.NewResponseController(rec)
	if err := rc.Flush(); err != nil {
		t.Errorf("Flush through StatusRecorder = %v, want nil", err)
	}
	if !inner.Flushed {
		t.Error("underlying recorder was not flushed; Unwrap did not expose Flusher")
	}
}
