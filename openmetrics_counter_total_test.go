package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenMetrics_CounterTotalSuffixNotDoubled(t *testing.T) {
	r := NewRegistry("test")
	// Counter named with _total suffix (common Prometheus convention)
	c := NewCounter("http_requests_total", "Total requests")
	r.RegisterCounter(c)
	c.Add(7)

	h := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	// Must NOT produce _total_total
	if strings.Contains(body, "_total_total") {
		t.Errorf("double _total suffix in output:\n%s", body)
	}
	// TYPE must use base name without _total
	if !strings.Contains(body, "# TYPE http_requests counter") {
		t.Errorf("TYPE should use base name (http_requests):\n%s", body)
	}
	// HELP must use base name without _total
	if !strings.Contains(body, "# HELP http_requests Total requests") {
		t.Errorf("HELP should use base name (http_requests):\n%s", body)
	}
	// Sample line must have _total
	if !strings.Contains(body, "http_requests_total 7") {
		t.Errorf("sample line should be http_requests_total 7:\n%s", body)
	}
}

func TestOpenMetrics_LabeledCounterTotalSuffixNotDoubled(t *testing.T) {
	r := NewRegistry("test")
	lc := NewLabeledCounter("api_calls_total", "API calls", []string{"method"})
	r.RegisterLabeledCounter(lc)
	lc.Inc("GET")

	h := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if strings.Contains(body, "_total_total") {
		t.Errorf("double _total suffix:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE api_calls counter") {
		t.Errorf("TYPE should use base name:\n%s", body)
	}
	if !strings.Contains(body, `api_calls_total{method="GET"} 1`) {
		t.Errorf("sample line wrong:\n%s", body)
	}
}
