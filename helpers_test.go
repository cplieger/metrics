package metrics

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mustPanicContaining runs fn and fails unless it panics with a string or
// error value containing want. Used to assert the package's fail-fast guards
// (invalid registry prefix, label arity, negative Counter.Add) and the
// MustRegister door, which panics with the registration error.
func mustPanicContaining(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q, got none", want)
		}
		var msg string
		switch v := r.(type) {
		case string:
			msg = v
		case error:
			msg = v.Error()
		default:
			t.Fatalf("panic = %v, want a string or error containing %q", r, want)
		}
		if !strings.Contains(msg, want) {
			t.Fatalf("panic = %v, want it to contain %q", r, want)
		}
	}()
	fn()
}

// mustRegisterError registers m and fails unless Register returns an error
// containing want — the v4 shape of the old panic-at-construction and
// panic-at-registration assertions.
func mustRegisterError(t *testing.T, r *Registry, m Metric, want string) {
	t.Helper()
	err := r.Register(m)
	if err == nil {
		t.Fatalf("Register = nil, want error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Register error = %q, want it to contain %q", err, want)
	}
}

// body serves r's Prometheus handler against a throwaway request and returns
// the response body, the common shape for asserting on a registry's exposition.
func body(t *testing.T, r *Registry) string {
	t.Helper()
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return w.Body.String()
}

// failWriter is an http.ResponseWriter whose Write always fails, used to drive
// the write-error logging branch of the Prometheus handler.
type failWriter struct {
	hdr http.Header
}

func (w *failWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}

func (w *failWriter) Write([]byte) (int, error) {
	return 0, errors.New("forced write failure")
}

func (w *failWriter) WriteHeader(int) {}

// captureDebugLogs redirects the default slog logger to an in-memory buffer at
// Debug level for the duration of the test and returns that buffer.
func captureDebugLogs(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

// assertExpositionLabelsBalanced verifies every labeled exposition line in out
// has balanced braces and a quote-balanced (properly escaped) label section. It
// is shared by the label-value fuzz targets so they assert a structural
// invariant rather than merely catching panics.
func assertExpositionLabelsBalanced(t *testing.T, out string) {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		// The label section runs from the FIRST '{' to the LAST '}': a label
		// value may legitimately contain either brace. Cutting around both
		// separators carries the "found" answer with the slice, so no index
		// arithmetic can go one out.
		_, afterOpen, ok := strings.Cut(line, "{")
		if !ok {
			continue
		}
		inner, _, ok := strings.CutLast(afterOpen, "}")
		if !ok {
			t.Fatalf("unbalanced braces: %q", line)
		}
		checkLabelQuoting(t, inner)
	}
}

func checkLabelQuoting(t *testing.T, inner string) {
	t.Helper()
	inQuote := false
	for i := 0; i < len(inner); i++ {
		switch {
		case inner[i] == '\\' && inQuote:
			i++ // skip escaped char
		case inner[i] == '"':
			inQuote = !inQuote
		}
	}
	if inQuote {
		t.Fatalf("unbalanced quotes in label section: %q", inner)
	}
}
