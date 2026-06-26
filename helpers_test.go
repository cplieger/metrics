package metrics

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mustPanicContaining runs fn and fails unless it panics with a string value
// containing want. Used to assert the package's fail-fast guards (registry
// collisions, invalid names, bad bucket bounds).
func mustPanicContaining(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q, got none", want)
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, want) {
			t.Fatalf("panic = %v, want a string containing %q", r, want)
		}
	}()
	fn()
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
// the write-error logging branches of the Prometheus and OpenMetrics handlers.
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
		if !strings.Contains(line, "{") {
			continue
		}
		braceStart := strings.IndexByte(line, '{')
		braceEnd := strings.LastIndexByte(line, '}')
		if braceEnd <= braceStart {
			t.Fatalf("unbalanced braces: %q", line)
		}
		checkLabelQuoting(t, line[braceStart+1:braceEnd])
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
