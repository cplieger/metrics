package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func body(t *testing.T, r *Registry) string {
	t.Helper()
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return w.Body.String()
}

func TestRegistryAutoPrefix(t *testing.T) {
	r := NewRegistry("app")
	c := NewCounter("widgets_total", "Widgets")
	r.RegisterCounter(c)
	c.Inc()
	out := body(t, r)
	if !strings.Contains(out, "app_widgets_total 1") {
		t.Errorf("RegisterCounter on prefixed registry = %q, want app_widgets_total", out)
	}
	if strings.Contains(out, "\nwidgets_total") || strings.Contains(out, "app_app_") {
		t.Errorf("name not prefixed exactly once:\n%s", out)
	}
	if !strings.Contains(out, "process_uptime_seconds") || strings.Contains(out, "app_process_") {
		t.Errorf("process_* must not be prefixed:\n%s", out)
	}
}

func TestRegistryEmptyPrefixUnchanged(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("widgets_total", "Widgets")
	r.RegisterCounter(c)
	c.Inc()
	if out := body(t, r); !strings.Contains(out, "\nwidgets_total 1") {
		t.Errorf("empty prefix should leave name unchanged:\n%s", out)
	}
}

func TestRegistryInvalidPrefixPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewRegistry with invalid prefix should panic")
		}
	}()
	NewRegistry("bad-prefix!")
}

func TestLabeledHistogramTimer(t *testing.T) {
	lh := NewLabeledHistogram("op_seconds", "op", []string{"kind"})
	r := NewRegistry("")
	r.RegisterLabeledHistogram(lh)
	tm := lh.NewTimer("scan")
	if d := tm.ObserveDuration(); d < 0 {
		t.Fatalf("negative duration %v", d)
	}
	if out := body(t, r); !strings.Contains(out, `op_seconds_count{kind="scan"} 1`) {
		t.Errorf("labeled timer should record one observation:\n%s", out)
	}
}

func TestAPIBucketsWide(t *testing.T) {
	if APIBuckets[len(APIBuckets)-1] < 10 {
		t.Errorf("APIBuckets should extend well past 1s for slow calls, got %v", APIBuckets)
	}
}
