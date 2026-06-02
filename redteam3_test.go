package metrics

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// --- Attack surface 1: Reserved suffixes in user-supplied names ---
// Counters/gauges/histograms named with _total, _bucket, _sum, _count, _info, _created suffixes.

func TestReservedSuffix_CounterNamedTotal_Prometheus(t *testing.T) {
	r := NewRegistry("test")
	c := NewCounter("my_requests_total", "Requests")
	r.RegisterCounter(c)
	c.Add(42)

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	// Prometheus format: counter named _total should keep _total in sample and TYPE/HELP
	if !strings.Contains(body, "# TYPE my_requests_total counter") {
		t.Errorf("Prometheus TYPE wrong:\n%s", body)
	}
	if !strings.Contains(body, "# HELP my_requests_total Requests") {
		t.Errorf("Prometheus HELP wrong:\n%s", body)
	}
	if !strings.Contains(body, "my_requests_total 42") {
		t.Errorf("Prometheus sample wrong:\n%s", body)
	}
}

func TestReservedSuffix_CounterNamedTotal_OpenMetrics(t *testing.T) {
	r := NewRegistry("test")
	c := NewCounter("my_requests_total", "Requests")
	r.RegisterCounter(c)
	c.Add(42)

	rec := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	// OpenMetrics: TYPE/HELP use base name (strip _total), sample uses _total
	if !strings.Contains(body, "# TYPE my_requests counter") {
		t.Errorf("OM TYPE wrong:\n%s", body)
	}
	if !strings.Contains(body, "# HELP my_requests Requests") {
		t.Errorf("OM HELP wrong:\n%s", body)
	}
	if !strings.Contains(body, "my_requests_total 42") {
		t.Errorf("OM sample wrong:\n%s", body)
	}
	if strings.Contains(body, "_total_total") {
		t.Errorf("double _total:\n%s", body)
	}
}

func TestReservedSuffix_GaugeNamedTotal(t *testing.T) {
	// A gauge named with _total suffix — unusual but valid metric name
	g := NewGauge("weird_total", "A gauge with _total suffix")
	g.Set(7)
	var b strings.Builder
	WriteGauge(&b, g)
	out := b.String()
	if !strings.Contains(out, "# TYPE weird_total gauge") {
		t.Errorf("gauge TYPE wrong: %s", out)
	}
	if !strings.Contains(out, "weird_total 7") {
		t.Errorf("gauge sample wrong: %s", out)
	}
}

func TestReservedSuffix_GaugeNamedBucket(t *testing.T) {
	g := NewGauge("my_bucket", "A gauge named _bucket")
	g.Set(3)
	var b strings.Builder
	WriteGauge(&b, g)
	if !strings.Contains(b.String(), "my_bucket 3") {
		t.Errorf("gauge named _bucket wrong: %s", b.String())
	}
}

func TestReservedSuffix_HistogramNamedCount(t *testing.T) {
	// Histogram named with _count suffix — should still work
	h := NewHistogram("ops_count", "Operations", WithBuckets([]float64{1, 5, 10}))
	h.Observe(3)
	var b strings.Builder
	WriteHistogram(&b, h)
	out := b.String()
	if !strings.Contains(out, "# TYPE ops_count histogram") {
		t.Errorf("histogram TYPE wrong: %s", out)
	}
	if !strings.Contains(out, "ops_count_bucket") {
		t.Errorf("histogram bucket wrong: %s", out)
	}
	if !strings.Contains(out, "ops_count_sum") {
		t.Errorf("histogram sum wrong: %s", out)
	}
	if !strings.Contains(out, "ops_count_count 1") {
		t.Errorf("histogram count wrong: %s", out)
	}
}

// --- Attack surface 2: Both formats parity ---

func TestFormatParity_AllMetricTypes(t *testing.T) {
	r := NewRegistry("parity")
	c := NewCounter("parity_counter", "A counter")
	g := NewGauge("parity_gauge", "A gauge")
	h := NewHistogram("parity_hist", "A histogram", WithBuckets([]float64{0.1, 1}))
	lc := NewLabeledCounter("parity_lc", "Labeled counter", []string{"k"})
	lg := NewLabeledGauge("parity_lg", "Labeled gauge", []string{"k"})
	lh := NewLabeledHistogram("parity_lh", "Labeled histogram", []string{"k"}, WithBuckets([]float64{0.5}))

	r.RegisterCounter(c)
	r.RegisterGauge(g)
	r.RegisterHistogram(h)
	r.RegisterLabeledCounter(lc)
	r.RegisterLabeledGauge(lg)
	r.RegisterLabeledHistogram(lh)

	c.Add(10)
	g.Set(3.14)
	h.Observe(0.05)
	h.Observe(5.0)
	lc.Inc("v1")
	lg.Set(99, "v1")
	lh.Observe(0.3, "v1")

	// Prometheus format
	rec1 := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	prom := rec1.Body.String()

	// OpenMetrics format
	rec2 := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	om := rec2.Body.String()

	// Both must have all metric families
	for _, name := range []string{"parity_counter", "parity_gauge", "parity_hist", "parity_lc", "parity_lg", "parity_lh"} {
		if !strings.Contains(prom, name) {
			t.Errorf("Prometheus missing %s", name)
		}
		if !strings.Contains(om, name) {
			t.Errorf("OpenMetrics missing %s", name)
		}
	}

	// OpenMetrics must end with EOF
	if !strings.HasSuffix(om, "# EOF\n") {
		t.Error("OpenMetrics missing EOF")
	}
	// Prometheus must NOT end with EOF
	if strings.Contains(prom, "# EOF") {
		t.Error("Prometheus should not have EOF")
	}
}

// --- Attack surface 3: UTF-8 and control chars in label values ---

func TestUTF8_LabelValues(t *testing.T) {
	lc := NewLabeledCounter("utf8_counter", "test", []string{"msg"})

	// Various UTF-8 and control chars
	vals := []string{
		"hello\x00world",          // null byte
		"tab\there",               // tab
		"newline\nhere",           // newline (must be escaped)
		"quote\"here",             // quote (must be escaped)
		"backslash\\here",         // backslash (must be escaped)
		"emoji🎉done",              // multi-byte UTF-8
		"\x01\x02\x03",            // control chars
		"日本語テスト",                  // CJK
		"",                        // empty string
		strings.Repeat("x", 1000), // long value
	}

	for _, v := range vals {
		lc.Inc(v)
	}

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()

	// Must not panic, must produce valid output
	if !strings.Contains(out, "# TYPE utf8_counter counter") {
		t.Errorf("missing TYPE: %s", out)
	}

	// Verify escaping: newlines must be escaped
	if strings.Contains(out, "msg=\"newline\nhere\"") {
		t.Error("raw newline in label value not escaped")
	}
	if !strings.Contains(out, `msg="newline\nhere"`) {
		t.Errorf("newline not properly escaped: %s", out)
	}

	// Quotes must be escaped
	if !strings.Contains(out, `msg="quote\"here"`) {
		t.Errorf("quote not properly escaped: %s", out)
	}

	// Backslash must be escaped
	if !strings.Contains(out, `msg="backslash\\here"`) {
		t.Errorf("backslash not properly escaped: %s", out)
	}
}

func TestUTF8_HelpText(t *testing.T) {
	// Help text with special chars
	c := NewCounter("help_test", "Help with\nnewline and \\backslash")
	c.Inc()
	var b strings.Builder
	WriteCounter(&b, c)
	out := b.String()
	if !strings.Contains(out, `# HELP help_test Help with\nnewline and \\backslash`) {
		t.Errorf("HELP escaping wrong: %s", out)
	}
}

// --- Attack surface 4: Contention races ---

func TestRace_RegistryHandlerDuringMutation(t *testing.T) {
	r := NewRegistry("race")
	c := NewCounter("race_counter", "test")
	g := NewGauge("race_gauge", "test")
	h := NewHistogram("race_hist", "test")
	lc := NewLabeledCounter("race_lc", "test", []string{"k"})
	r.RegisterCounter(c)
	r.RegisterGauge(g)
	r.RegisterHistogram(h)
	r.RegisterLabeledCounter(lc)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Concurrent writers
	wg.Add(4)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				c.Inc()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				g.Set(float64(i))
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				h.Observe(float64(i) * 0.001)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				lc.Inc("val")
			}
		}
	}()

	// Concurrent readers (both formats)
	wg.Add(2)
	go func() {
		defer wg.Done()
		handler := r.Handler()
		for range 100 {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		}
	}()
	go func() {
		defer wg.Done()
		handler := r.OpenMetricsHandler()
		for range 100 {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		}
	}()

	// Let it run briefly then stop
	close(stop)
	wg.Wait()
}

func TestRace_LabeledCounterNewKeys(t *testing.T) {
	lc := NewLabeledCounter("race_newkeys", "test", []string{"id"})
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := range 20 {
				lc.Inc(strings.Repeat("x", n) + string(rune('0'+j%10)))
			}
		}(i)
	}
	wg.Wait()
}

func TestRace_LabeledGaugeNewKeys(t *testing.T) {
	lg := NewLabeledGauge("race_lg_newkeys", "test", []string{"id"})
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := range 20 {
				lg.Set(float64(j), strings.Repeat("y", n)+string(rune('0'+j%10)))
			}
		}(i)
	}
	wg.Wait()
}

func TestRace_LabeledHistogramNewKeys(t *testing.T) {
	lh := NewLabeledHistogram("race_lh_newkeys", "test", []string{"id"}, WithBuckets([]float64{0.1, 1}))
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := range 20 {
				lh.Observe(float64(j)*0.1, strings.Repeat("z", n)+string(rune('0'+j%10)))
			}
		}(i)
	}
	wg.Wait()
}

func TestRace_GaugeCAS(t *testing.T) {
	g := NewGauge("race_gauge_cas", "test")
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			for range 1000 {
				g.Add(0.1)
				g.Sub(0.1)
			}
		})
	}
	wg.Wait()
	// After equal adds and subs, should be ~0 (floating point)
	if v := g.Get(); math.Abs(v) > 1.0 {
		t.Errorf("gauge drift too large: %f", v)
	}
}

// --- Attack surface 5: Malformed bucket configs ---

func TestHistogram_EmptyBuckets(t *testing.T) {
	h := NewHistogram("empty_buckets", "test", WithBuckets([]float64{}))
	h.Observe(1.0)
	h.Observe(5.0)

	var b strings.Builder
	WriteHistogram(&b, h)
	out := b.String()

	// With no bounds, only +Inf bucket should exist
	if !strings.Contains(out, `empty_buckets_bucket{le="+Inf"} 2`) {
		t.Errorf("empty buckets wrong: %s", out)
	}
	if h.count.Load() != 2 {
		t.Errorf("count wrong: %d", h.count.Load())
	}
}

func TestHistogram_DuplicateBuckets(t *testing.T) {
	h := NewHistogram("dup_buckets", "test", WithBuckets([]float64{1, 1, 5, 5, 10}))
	h.Observe(3)

	var b strings.Builder
	WriteHistogram(&b, h)
	out := b.String()

	// Should not panic; duplicates are sorted and output as-is
	if !strings.Contains(out, "# TYPE dup_buckets histogram") {
		t.Errorf("missing TYPE: %s", out)
	}
	if h.count.Load() != 1 {
		t.Errorf("count wrong: %d", h.count.Load())
	}
}

func TestHistogram_NegativeBuckets(t *testing.T) {
	h := NewHistogram("neg_buckets", "test", WithBuckets([]float64{-10, -1, 0, 1, 10}))
	h.Observe(-5)
	h.Observe(0)
	h.Observe(5)

	var b strings.Builder
	WriteHistogram(&b, h)
	out := b.String()

	if !strings.Contains(out, `neg_buckets_bucket{le="-10"} 0`) {
		t.Errorf("negative bucket wrong: %s", out)
	}
	if !strings.Contains(out, `neg_buckets_bucket{le="-1"} 1`) {
		t.Errorf("-1 bucket wrong: %s", out)
	}
	if h.count.Load() != 3 {
		t.Errorf("count wrong: %d", h.count.Load())
	}
}

func TestHistogram_InfBucket(t *testing.T) {
	h := NewHistogram("inf_buckets", "test", WithBuckets([]float64{1, math.Inf(1)}))
	h.Observe(0.5)
	h.Observe(100)

	var b strings.Builder
	WriteHistogram(&b, h)
	out := b.String()

	if !strings.Contains(out, "# TYPE inf_buckets histogram") {
		t.Errorf("missing TYPE: %s", out)
	}
	if h.count.Load() != 2 {
		t.Errorf("count wrong: %d", h.count.Load())
	}
}

func TestHistogram_NaNObserve(t *testing.T) {
	h := NewHistogram("nan_obs", "test", WithBuckets([]float64{0.1, 1}))
	h.Observe(math.NaN())
	// NaN should not panic, count should increment
	if h.count.Load() != 1 {
		t.Errorf("NaN observe count wrong: %d", h.count.Load())
	}
}

func TestHistogram_InfObserve(t *testing.T) {
	h := NewHistogram("inf_obs", "test", WithBuckets([]float64{0.1, 1}))
	h.Observe(math.Inf(1))
	h.Observe(math.Inf(-1))
	if h.count.Load() != 2 {
		t.Errorf("Inf observe count wrong: %d", h.count.Load())
	}
	// +Inf should only land in +Inf bucket
	// -Inf should land in all buckets (it's <= every bound)
	infBucket := h.buckets[len(h.bounds)].Load()
	if infBucket != 2 {
		t.Errorf("+Inf bucket = %d, want 2", infBucket)
	}
}

func TestHistogram_SingleBucket(t *testing.T) {
	h := NewHistogram("single_bucket", "test", WithBuckets([]float64{1}))
	h.Observe(0.5)
	h.Observe(1.5)

	var b strings.Builder
	WriteHistogram(&b, h)
	out := b.String()

	if !strings.Contains(out, `single_bucket_bucket{le="1"} 1`) {
		t.Errorf("single bucket wrong: %s", out)
	}
	if !strings.Contains(out, `single_bucket_bucket{le="+Inf"} 2`) {
		t.Errorf("single bucket +Inf wrong: %s", out)
	}
}

// --- Attack surface 6: OpenMetrics counter _total for labeled counters with _total name ---

func TestReservedSuffix_LabeledCounterNamedTotal_OpenMetrics(t *testing.T) {
	r := NewRegistry("test")
	lc := NewLabeledCounter("api_errors_total", "API errors", []string{"code"})
	r.RegisterLabeledCounter(lc)
	lc.Inc("500")
	lc.Inc("404")

	rec := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if strings.Contains(body, "_total_total") {
		t.Errorf("double _total:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE api_errors counter") {
		t.Errorf("TYPE wrong:\n%s", body)
	}
	if !strings.Contains(body, "# HELP api_errors API errors") {
		t.Errorf("HELP wrong:\n%s", body)
	}
	if !strings.Contains(body, `api_errors_total{code="500"} 1`) {
		t.Errorf("sample wrong:\n%s", body)
	}
}

// --- Attack surface 7: Image metrics with special chars in labels ---

func TestImageMetrics_SpecialCharsInLabels(t *testing.T) {
	SetImageMetrics([]ImageMetric{
		{Registry: "docker\"hub", Owner: "user\\name", Repo: "app\nnewline", Pulls: 1, Tags: 1},
	})
	defer SetImageMetrics(nil)

	var b strings.Builder
	WriteImageMetrics(&b, "test")
	out := b.String()

	// Must escape quotes, backslashes, newlines in label values
	if strings.Contains(out, "docker\"hub") && !strings.Contains(out, `docker\"hub`) {
		t.Errorf("unescaped quote in image label: %s", out)
	}
	if !strings.Contains(out, `user\\name`) {
		t.Errorf("unescaped backslash in image label: %s", out)
	}
}

// --- Attack surface 8: Validate metric name edge cases ---

func TestValidateMetricName_Panics(t *testing.T) {
	invalid := []string{
		"",
		"123abc",
		"metric-name",
		"metric.name",
		"metric name",
		"metric\x00name",
		"metric\nname",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for name %q", name)
				}
			}()
			NewCounter(name, "test")
		})
	}
}

func TestValidateMetricName_Valid(t *testing.T) {
	valid := []string{
		"a",
		"_private",
		":colon",
		"abc_123",
		"A_B_C",
		"metric:submetric",
		"__internal",
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			// Should not panic
			NewCounter(name, "test")
		})
	}
}

func TestValidateLabelName_Panics(t *testing.T) {
	invalid := []string{
		"",
		"123abc",
		"label-name",
		"label:name", // colons not allowed in labels
		"label.name",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for label %q", name)
				}
			}()
			NewLabeledCounter("valid_metric", "test", []string{name})
		})
	}
}

// --- Attack surface 9: OpenMetrics format for process counters ---

func TestOpenMetrics_ProcessCounters_BaseNameInType(t *testing.T) {
	r := NewRegistry("test")
	rec := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	// process_gc_pause_seconds is a counter — TYPE must use base name
	if strings.Contains(body, "# TYPE process_gc_pause_seconds_total") {
		t.Errorf("TYPE should not have _total for process counter:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE process_gc_pause_seconds counter") {
		t.Errorf("missing correct TYPE:\n%s", body)
	}
	// Sample must have _total
	if !strings.Contains(body, "process_gc_pause_seconds_total") {
		t.Errorf("sample missing _total:\n%s", body)
	}
}

// --- Attack surface 10: Concurrent registry registration + handler ---

func TestRace_RegisterDuringServe(t *testing.T) {
	r := NewRegistry("race_reg")
	c := NewCounter("race_reg_c", "test")
	r.RegisterCounter(c)
	c.Inc()

	var wg sync.WaitGroup
	handler := r.Handler()

	// Serve concurrently
	wg.Go(func() {
		for range 50 {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		}
	})

	// Register new metrics concurrently
	wg.Go(func() {
		for i := range 50 {
			g := NewGauge("race_reg_g_"+strings.Repeat("x", i%5)+string(rune('a'+i%26)), "test")
			r.RegisterGauge(g)
			g.Set(float64(i))
		}
	})

	wg.Wait()
}
