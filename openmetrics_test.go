package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenMetricsHandler_ContentType(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("test_requests_total", "Total requests")
	r.RegisterCounter(c)
	c.Inc()

	h := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	ct := rec.Header().Get("Content-Type")
	if ct != OpenMetricsContentType {
		t.Errorf("Content-Type = %q, want %q", ct, OpenMetricsContentType)
	}
}

func TestOpenMetricsHandler_EOF(t *testing.T) {
	r := NewRegistry("")
	h := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if !strings.HasSuffix(body, "# EOF\n") {
		t.Errorf("OpenMetrics output must end with '# EOF\\n', got suffix: %q", body[max(0, len(body)-20):])
	}
}

func TestOpenMetricsHandler_CounterTotal(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("test_requests", "Total requests")
	r.RegisterCounter(c)
	c.Add(5)

	h := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "# TYPE test_requests counter") {
		t.Errorf("missing TYPE line: %s", body)
	}
	if !strings.Contains(body, "test_requests_total 5") {
		t.Errorf("counter should have _total suffix: %s", body)
	}
}

func TestOpenMetricsHandler_LabeledCounter(t *testing.T) {
	r := NewRegistry("")
	lc := NewLabeledCounter("http_requests", "HTTP requests", []string{"method"})
	r.RegisterLabeledCounter(lc)
	lc.Inc("GET")
	lc.Inc("GET")
	lc.Inc("POST")

	h := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "# TYPE http_requests counter") {
		t.Errorf("missing TYPE: %s", body)
	}
	if !strings.Contains(body, `http_requests_total{method="GET"} 2`) {
		t.Errorf("missing labeled counter with _total: %s", body)
	}
	if !strings.Contains(body, `http_requests_total{method="POST"} 1`) {
		t.Errorf("missing POST counter: %s", body)
	}
}

func TestOpenMetricsHandler_Gauge(t *testing.T) {
	r := NewRegistry("")
	g := NewGauge("temperature", "Current temp")
	r.RegisterGauge(g)
	g.Set(23.5)

	h := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "# TYPE temperature gauge") {
		t.Errorf("missing TYPE: %s", body)
	}
	if !strings.Contains(body, "temperature 23.5") {
		t.Errorf("missing gauge value: %s", body)
	}
}

func TestOpenMetricsHandler_GaugeInteger(t *testing.T) {
	r := NewRegistry("")
	g := NewGauge("connections", "Active connections")
	r.RegisterGauge(g)
	g.Set(42)

	h := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	// A whole-valued gauge renders as a bare integer ("42") in both formats;
	// the OpenMetrics ABNF realnumber accepts a bare integer, so no ".0" is
	// emitted (matches the Prometheus rendering for parity).
	if !strings.Contains(body, "connections 42\n") {
		t.Errorf("integer gauge should render as bare integer: %s", body)
	}
}

func TestOpenMetricsHandler_Histogram(t *testing.T) {
	r := NewRegistry("")
	h := NewHistogram("request_duration_seconds", "Request latency", WithBuckets([]float64{0.1, 0.5, 1}))
	r.RegisterHistogram(h)
	h.Observe(0.05)
	h.Observe(0.3)
	h.Observe(2.0)

	handler := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "# TYPE request_duration_seconds histogram") {
		t.Errorf("missing TYPE: %s", body)
	}
	if !strings.Contains(body, `request_duration_seconds_bucket{le="0.1"} 1`) {
		t.Errorf("missing bucket: %s", body)
	}
	if !strings.Contains(body, `request_duration_seconds_bucket{le="+Inf"} 3`) {
		t.Errorf("missing +Inf bucket: %s", body)
	}
	if !strings.Contains(body, "request_duration_seconds_count 3") {
		t.Errorf("missing count: %s", body)
	}
	if !strings.Contains(body, "request_duration_seconds_sum") {
		t.Errorf("missing sum: %s", body)
	}
}

func TestOpenMetricsHandler_TypeBeforeHelp(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("my_counter", "A counter")
	r.RegisterCounter(c)
	c.Inc()

	handler := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	typeIdx := strings.Index(body, "# TYPE my_counter")
	helpIdx := strings.Index(body, "# HELP my_counter")
	if typeIdx < 0 || helpIdx < 0 {
		t.Fatalf("missing TYPE or HELP: %s", body)
	}
	if typeIdx > helpIdx {
		t.Errorf("OpenMetrics requires TYPE before HELP, got TYPE at %d, HELP at %d", typeIdx, helpIdx)
	}
}

func TestNegotiateHandler_OpenMetrics(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("neg_counter", "test")
	r.RegisterCounter(c)
	c.Inc()

	handler := r.NegotiateHandler()

	// Request with OpenMetrics Accept header
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0; charset=utf-8")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != OpenMetricsContentType {
		t.Errorf("with OM Accept, Content-Type = %q, want %q", ct, OpenMetricsContentType)
	}
	body := rec.Body.String()
	if !strings.HasSuffix(body, "# EOF\n") {
		t.Error("OpenMetrics response must end with # EOF")
	}
	if !strings.Contains(body, "neg_counter_total") {
		t.Errorf("missing _total suffix: %s", body)
	}
}

func TestNegotiateHandler_Prometheus(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("neg_counter", "test")
	r.RegisterCounter(c)
	c.Inc()

	handler := r.NegotiateHandler()

	// Request without OpenMetrics Accept header
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("without OM Accept, Content-Type = %q, want text/plain", ct)
	}
	body := rec.Body.String()
	if strings.HasSuffix(body, "# EOF\n") {
		t.Error("Prometheus format should not have # EOF")
	}
}

func TestNegotiateHandler_PrometheusStyleAccept(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("neg_counter2", "test")
	r.RegisterCounter(c)
	c.Inc()

	handler := r.NegotiateHandler()

	// Prometheus-style Accept header with quality values
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text;version=1.0.0;q=0.5,text/plain;version=0.0.4;q=0.3")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != OpenMetricsContentType {
		t.Errorf("Content-Type = %q, want OpenMetrics", ct)
	}
}

func TestOpenMetricsHandler_ProcessMetrics(t *testing.T) {
	r := NewRegistry("")
	handler := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE process_goroutines gauge",
		"# TYPE process_heap_bytes gauge",
		"# TYPE process_gc_pause_seconds counter",
		"# TYPE process_uptime_seconds gauge",
		"# TYPE process_start_time_seconds gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in output", want)
		}
	}
}

func TestOpenMetricsHandler_LabeledHistogram(t *testing.T) {
	r := NewRegistry("")
	lh := NewLabeledHistogram("api_duration_seconds", "API latency", []string{"method"}, WithBuckets([]float64{0.1, 1}))
	r.RegisterLabeledHistogram(lh)
	lh.Observe(0.05, "GET")
	lh.Observe(5.0, "POST")

	handler := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "# TYPE api_duration_seconds histogram") {
		t.Errorf("missing TYPE: %s", body)
	}
	if !strings.Contains(body, `api_duration_seconds_bucket{method="GET",le="0.1"} 1`) {
		t.Errorf("missing labeled bucket: %s", body)
	}
	if !strings.Contains(body, `api_duration_seconds_count{method="GET"} 1`) {
		t.Errorf("missing labeled count: %s", body)
	}
}

func TestOpenMetricsHandler_LabeledGauge(t *testing.T) {
	r := NewRegistry("")
	lg := NewLabeledGauge("cpu_usage", "CPU usage", []string{"core"})
	r.RegisterLabeledGauge(lg)
	lg.Set(0.75, "0")
	lg.Set(0.50, "1")

	handler := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "# TYPE cpu_usage gauge") {
		t.Errorf("missing TYPE: %s", body)
	}
	if !strings.Contains(body, `cpu_usage{core="0"} 0.75`) {
		t.Errorf("missing core 0: %s", body)
	}
	if !strings.Contains(body, `cpu_usage{core="1"} 0.5`) {
		t.Errorf("missing core 1: %s", body)
	}
}

func TestOpenMetricsHandler_Full(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("myapp_requests", "Total requests")
	g := NewGauge("myapp_temperature", "Temperature")
	h := NewHistogram("myapp_latency_seconds", "Latency")
	r.RegisterCounter(c)
	r.RegisterGauge(g)
	r.RegisterHistogram(h)

	c.Add(10)
	g.Set(22.5)
	h.Observe(0.042)

	handler := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	// Verify no empty lines between metric families (OpenMetrics requirement)
	if strings.Contains(body, "\n\n") {
		t.Errorf("OpenMetrics must not have empty lines between metric families")
	}
	// Verify ends with EOF
	if !strings.HasSuffix(body, "# EOF\n") {
		t.Error("must end with # EOF")
	}
	// Verify TYPE comes before HELP for each metric
	for _, name := range []string{"myapp_requests", "myapp_temperature", "myapp_latency_seconds"} {
		typeIdx := strings.Index(body, "# TYPE "+name)
		helpIdx := strings.Index(body, "# HELP "+name)
		if typeIdx < 0 || helpIdx < 0 {
			t.Errorf("missing TYPE or HELP for %s", name)
			continue
		}
		if typeIdx > helpIdx {
			t.Errorf("TYPE must come before HELP for %s", name)
		}
	}
}

func TestOpenMetricsHandler_CounterTypeNoTotal(t *testing.T) {
	// OpenMetrics spec: TYPE/HELP lines for counters must NOT include _total suffix.
	// Only the sample line gets _total.
	r := NewRegistry("")
	handler := r.OpenMetricsHandler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	// process_gc_pause_seconds_total is a counter — TYPE must use base name
	if strings.Contains(body, "# TYPE process_gc_pause_seconds_total") {
		t.Errorf("OpenMetrics TYPE line must not include _total suffix for counters:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE process_gc_pause_seconds counter") {
		t.Errorf("missing correct TYPE line for process_gc_pause_seconds:\n%s", body)
	}
	// The sample line must have _total
	if !strings.Contains(body, "process_gc_pause_seconds_total") {
		t.Errorf("sample line must have _total suffix:\n%s", body)
	}
	// process_cpu_seconds_total — same rule
	if strings.Contains(body, "# TYPE process_cpu_seconds_total") {
		t.Errorf("OpenMetrics TYPE line must not include _total for process_cpu_seconds:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE process_cpu_seconds counter") {
		t.Errorf("missing correct TYPE line for process_cpu_seconds:\n%s", body)
	}
	if !strings.Contains(body, "process_cpu_seconds_total") {
		t.Errorf("sample line must have _total for process_cpu_seconds:\n%s", body)
	}
}

func BenchmarkOpenMetricsHandler(b *testing.B) {
	r := NewRegistry("")
	httpReqs := NewLabeledCounter("bench_http_requests", "Total HTTP requests", []string{"method", "path", "status"})
	httpDur := NewHistogram("bench_http_request_duration_seconds", "HTTP request latency")
	tasks := NewCounter("bench_tasks", "Total tasks")
	r.RegisterLabeledCounter(httpReqs)
	r.RegisterHistogram(httpDur)
	r.RegisterCounter(tasks)

	httpReqs.Inc("GET", "/api", "200")
	httpDur.Observe(0.05)
	tasks.Inc()

	h := r.OpenMetricsHandler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func TestOpenMetricsHistogram_IntegerBucketBound_CanonicalLE(t *testing.T) {
	t.Run("unlabeled", func(t *testing.T) {
		r := NewRegistry("")
		h := NewHistogram("om_le_seconds", "latency", WithBuckets([]float64{0.5, 1, 2}))
		r.RegisterHistogram(h)
		h.Observe(0.1)

		rec := httptest.NewRecorder()
		r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		body := rec.Body.String()

		if !strings.Contains(body, `om_le_seconds_bucket{le="1"} 1`) {
			t.Errorf("integer bound must render as le=\"1\" in OpenMetrics:\n%s", body)
		}
		if !strings.Contains(body, `om_le_seconds_bucket{le="2"} 1`) {
			t.Errorf("integer bound must render as le=\"2\" in OpenMetrics:\n%s", body)
		}
		if !strings.Contains(body, `om_le_seconds_bucket{le="0.5"} 1`) {
			t.Errorf("fractional bound must render as le=\"0.5\":\n%s", body)
		}
	})

	t.Run("labeled", func(t *testing.T) {
		r := NewRegistry("")
		lh := NewLabeledHistogram("om_le_lbl_seconds", "latency", []string{"op"}, WithBuckets([]float64{0.5, 1}))
		r.RegisterLabeledHistogram(lh)
		lh.Observe(0.1, "read")

		rec := httptest.NewRecorder()
		r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		body := rec.Body.String()

		if !strings.Contains(body, `om_le_lbl_seconds_bucket{op="read",le="1"} 1`) {
			t.Errorf("labeled integer bound must render as le=\"1\":\n%s", body)
		}
	})
}

func TestOMCounterBaseName_TotalOnlyNameStaysNonEmpty(t *testing.T) {
	// A counter named exactly "_total" must not strip to an empty base, which
	// would emit a malformed "# TYPE  counter" line with no metric name.
	tests := []struct {
		in   string
		want string
	}{
		{"_total", "_total"},
		{"foo_total", "foo"},
		{"foo", "foo"},
		{"requests_total", "requests"},
	}
	for _, tt := range tests {
		if got := omCounterBaseName(tt.in); got != tt.want {
			t.Errorf("omCounterBaseName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAcceptsOpenMetrics_QValueNegotiation(t *testing.T) {
	tests := []struct {
		name   string
		accept string
		want   bool
	}{
		{"explicit om only", "application/openmetrics-text", true},
		{"om with version param", "application/openmetrics-text;version=1.0.0", true},
		{"prometheus real (om ranked higher)", "application/openmetrics-text;version=1.0.0;q=0.5,text/plain;version=0.0.4;q=0.4,*/*;q=0.1", true},
		{"om equal to text/plain", "text/plain;q=0.4,application/openmetrics-text;q=0.4", true},
		{"om explicitly refused q=0", "application/openmetrics-text;q=0,text/plain;q=1", false},
		{"text/plain ranked higher", "text/plain;q=0.9,application/openmetrics-text;q=0.5", false},
		{"text/plain only", "text/plain", false},
		{"wildcard only (om not explicit)", "*/*", false},
		{"empty accept", "", false},
		{"malformed q defaults to 1.0", "application/openmetrics-text;q=banana", true},
		{"uppercase media type still matched (EqualFold)", "APPLICATION/OPENMETRICS-TEXT", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := acceptsOpenMetrics(tt.accept); got != tt.want {
				t.Errorf("acceptsOpenMetrics(%q) = %v, want %v", tt.accept, got, tt.want)
			}
		})
	}
}

func TestMediaQuality_returnsRFC7231QValues(t *testing.T) {
	tests := []struct {
		name, accept, mediaType string
		wantQ                   float64
		wantPres                bool
	}{
		{"absent type", "text/plain", "application/openmetrics-text", 0, false},
		{"present defaults q to 1.0", "application/openmetrics-text", "application/openmetrics-text", 1.0, true},
		{"explicit q", "application/openmetrics-text;q=0.7", "application/openmetrics-text", 0.7, true},
		{"malformed q falls back to 1.0", "application/openmetrics-text;q=xyz", "application/openmetrics-text", 1.0, true},
		{"case-insensitive type match", "APPLICATION/OpenMetrics-Text", "application/openmetrics-text", 1.0, true},
		{"negative q parsed verbatim", "application/openmetrics-text;q=-1", "application/openmetrics-text", -1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, pres := mediaQuality(tt.accept, tt.mediaType)
			if pres != tt.wantPres || q != tt.wantQ {
				t.Errorf("mediaQuality(%q, %q) = (%v, %v), want (%v, %v)",
					tt.accept, tt.mediaType, q, pres, tt.wantQ, tt.wantPres)
			}
		})
	}
}

// TestServeOpenMetrics_logsOnWriteError verifies a failed OpenMetrics exposition
// write is logged at debug level rather than silently swallowed.
func TestServeOpenMetrics_logsOnWriteError(t *testing.T) {
	buf := captureDebugLogs(t)
	reg := NewRegistry("")
	reg.RegisterCounter(NewCounter("om_writeerr_total", "h"))

	reg.serveOpenMetrics(&failWriter{})

	if got := buf.String(); !strings.Contains(got, "writing openmetrics exposition failed") {
		t.Fatalf("serveOpenMetrics() with failing writer: debug log = %q, want the write-failure message", got)
	}
}

// TestOpenMetricsHandler_counterTotalSuffixNotDoubled covers a counter already
// named with the _total suffix: OpenMetrics strips _total for the TYPE/HELP
// family name and keeps it on the sample series, never doubling it.
func TestOpenMetricsHandler_counterTotalSuffixNotDoubled(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("http_requests_total", "Total requests")
	r.RegisterCounter(c)
	c.Add(7)

	rec := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if strings.Contains(body, "_total_total") {
		t.Errorf("double _total suffix in output:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE http_requests counter") {
		t.Errorf("TYPE should use base name (http_requests):\n%s", body)
	}
	if !strings.Contains(body, "# HELP http_requests Total requests") {
		t.Errorf("HELP should use base name (http_requests):\n%s", body)
	}
	if !strings.Contains(body, "http_requests_total 7") {
		t.Errorf("sample line should be http_requests_total 7:\n%s", body)
	}
}

func TestOpenMetricsHandler_labeledCounterTotalSuffixNotDoubled(t *testing.T) {
	r := NewRegistry("")
	lc := NewLabeledCounter("api_calls_total", "API calls", []string{"method"})
	r.RegisterLabeledCounter(lc)
	lc.Inc("GET")

	rec := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

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

// TestAcceptsOpenMetrics_bareQZeroRefused pins the omQ <= 0 refusal at its
// boundary: a bare q=0 with no text/plain present must be refused. The q=0 case
// that also lists text/plain cannot pin this, because the text/plain branch
// returns false either way and masks an omQ < 0 mutation.
func TestAcceptsOpenMetrics_bareQZeroRefused(t *testing.T) {
	if acceptsOpenMetrics("application/openmetrics-text;q=0") {
		t.Error(`acceptsOpenMetrics("application/openmetrics-text;q=0") = true, want false (q<=0 refusal)`)
	}
}

// TestMediaQuality_duplicateTypeKeepsLargestQ verifies the max-selection in
// mediaQuality: with two entries for the same media type, the larger q wins.
func TestMediaQuality_duplicateTypeKeepsLargestQ(t *testing.T) {
	q, present := mediaQuality(
		"application/openmetrics-text;q=0.3,application/openmetrics-text;q=0.8",
		"application/openmetrics-text")
	if !present || q != 0.8 {
		t.Errorf("mediaQuality(duplicate type) = (%v, %v), want (0.8, true)", q, present)
	}
}
