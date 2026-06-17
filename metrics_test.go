package metrics

import (
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func BenchmarkHistogramObserve(b *testing.B) {
	h := NewHistogram("bench_hist", "bench")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		h.Observe(0.042)
	}
}

func BenchmarkHistogramObserve_Parallel(b *testing.B) {
	h := NewHistogram("bench_hist_par", "bench")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			h.Observe(0.042)
		}
	})
}

func BenchmarkLabeledCounterInc(b *testing.B) {
	lc := NewLabeledCounter("bench_lc", "bench", []string{"method", "path", "status"})
	lc.Inc("GET", "/api", "200")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		lc.Inc("GET", "/api", "200")
	}
}

func BenchmarkLabeledCounterInc_NewKey(b *testing.B) {
	lc := NewLabeledCounter("bench_lc_new", "bench", []string{"method", "path", "status"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		lc.Inc("GET", "/api/"+strings.Repeat("x", i%8), "200")
	}
}

func BenchmarkLabeledCounterInc_Parallel(b *testing.B) {
	lc := NewLabeledCounter("bench_lc_par", "bench", []string{"method", "path", "status"})
	lc.Inc("GET", "/api", "200")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			lc.Inc("GET", "/api", "200")
		}
	})
}

func TestRegistryHandler(t *testing.T) {
	r := NewRegistry("")

	httpReqs := NewLabeledCounter("test_http_requests_total", "Total HTTP requests", []string{"method", "path", "status"})
	activeConns := NewGauge("test_active_connections", "Active connection count")
	tasks := NewCounter("test_tasks_total", "Total tasks")
	events := NewCounter("test_events_total", "Total events")
	httpDur := NewHistogram("test_http_request_duration_seconds", "HTTP request latency")

	r.RegisterLabeledCounter(httpReqs)
	r.RegisterGauge(activeConns)
	r.RegisterCounter(tasks)
	r.RegisterCounter(events)
	r.RegisterHistogram(httpDur)

	httpReqs.Inc("GET", "/api", "200")
	httpDur.Observe(0.05)
	tasks.Inc()

	h := r.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("unexpected Content-Type: %s", ct)
	}
	for _, want := range []string{
		"test_http_request_duration_seconds",
		"test_tasks_total",
		"test_events_total",
		"test_active_connections",
		"process_goroutines",
		"process_heap_bytes",
		"process_uptime_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if !strings.Contains(body, "# HELP") {
		t.Error("output missing # HELP lines")
	}
	if !strings.Contains(body, "# TYPE") {
		t.Error("output missing # TYPE lines")
	}
}

func BenchmarkRegistryHandler(b *testing.B) {
	r := NewRegistry("")
	httpReqs := NewLabeledCounter("bench_http_requests_total", "Total HTTP requests", []string{"method", "path", "status"})
	httpDur := NewHistogram("bench_http_request_duration_seconds", "HTTP request latency")
	tasks := NewCounter("bench_tasks_total", "Total tasks")
	r.RegisterLabeledCounter(httpReqs)
	r.RegisterHistogram(httpDur)
	r.RegisterCounter(tasks)

	httpReqs.Inc("GET", "/api", "200")
	httpDur.Observe(0.05)
	tasks.Inc()

	h := r.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func TestCounterInc(t *testing.T) {
	c := NewCounter("test_counter", "test")
	c.Inc()
	c.Inc()
	if got := c.val.Load(); got != 2 {
		t.Errorf("Counter.Inc() = %d, want 2", got)
	}
}

func TestCounterAdd(t *testing.T) {
	c := NewCounter("test_counter_add", "test")
	c.Add(5)
	c.Add(3)
	if got := c.val.Load(); got != 8 {
		t.Errorf("Counter.Add() = %d, want 8", got)
	}
}

func TestCounterAddNegativePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative Add")
		}
	}()
	c := NewCounter("test_counter_neg", "test")
	c.Add(-1)
}

func TestGaugeFloat64(t *testing.T) {
	g := NewGauge("test_gauge_f64", "test")
	g.Set(3.14)
	if got := g.Get(); math.Abs(got-3.14) > 0.001 {
		t.Errorf("Gauge.Set(3.14) = %f", got)
	}
	g.Inc()
	if got := g.Get(); math.Abs(got-4.14) > 0.001 {
		t.Errorf("Gauge after Inc = %f", got)
	}
	g.Dec()
	if got := g.Get(); math.Abs(got-3.14) > 0.001 {
		t.Errorf("Gauge after Dec = %f", got)
	}
	g.Add(1.5)
	if got := g.Get(); math.Abs(got-4.64) > 0.001 {
		t.Errorf("Gauge after Add = %f", got)
	}
	g.Sub(0.64)
	if got := g.Get(); math.Abs(got-4.0) > 0.001 {
		t.Errorf("Gauge after Sub = %f", got)
	}
}

func TestGaugeIncDec(t *testing.T) {
	g := NewGauge("test_gauge", "test")
	g.Inc()
	g.Inc()
	g.Dec()
	if got := g.Get(); got != 1 {
		t.Errorf("Gauge = %f, want 1", got)
	}
}

func TestLabeledCounterInc(t *testing.T) {
	lc := NewLabeledCounter("test_lc", "test", []string{"method", "status"})
	lc.Inc("GET", "200")
	lc.Inc("GET", "200")
	lc.Inc("POST", "201")

	key := labelKey{"GET", "200", "", ""}
	if got := lc.vals[key].Load(); got != 2 {
		t.Errorf("LabeledCounter[GET,200] = %d, want 2", got)
	}
	key2 := labelKey{"POST", "201", "", ""}
	if got := lc.vals[key2].Load(); got != 1 {
		t.Errorf("LabeledCounter[POST,201] = %d, want 1", got)
	}
}

func TestLabeledCounterArityPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arity mismatch")
		}
	}()
	lc := NewLabeledCounter("test_lc_arity", "test", []string{"method", "status"})
	lc.Inc("GET") // wrong arity
}

func TestLabeledCounterTooManyLabelsPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for >4 labels")
		}
	}()
	NewLabeledCounter("test_lc_many", "test", []string{"a", "b", "c", "d", "e"})
}

func TestHistogramObserve(t *testing.T) {
	h := NewHistogram("test_hist", "test")
	h.Observe(0.003) // <= 0.005
	h.Observe(0.05)  // <= 0.05
	h.Observe(2.0)   // > 1.0, only +Inf

	if got := h.count.Load(); got != 3 {
		t.Errorf("Histogram.count = %d, want 3", got)
	}
	sum := math.Float64frombits(h.sumBits.Load())
	if math.Abs(sum-2.053) > 0.0001 {
		t.Errorf("Histogram.sum = %f, want ~2.053", sum)
	}
	if got := h.buckets[0].Load(); got != 1 {
		t.Errorf("bucket[0] = %d, want 1", got)
	}
	if got := h.buckets[len(h.bounds)].Load(); got != 3 {
		t.Errorf("bucket[+Inf] = %d, want 3", got)
	}
}

func TestHistogramCustomBuckets(t *testing.T) {
	h := NewHistogram("test_custom_hist", "test", WithBuckets([]float64{1, 5, 10}))
	h.Observe(0.5)
	h.Observe(3)
	h.Observe(7)
	h.Observe(20)

	if got := h.buckets[0].Load(); got != 1 {
		t.Errorf("bucket[<=1] = %d, want 1", got)
	}
	if got := h.buckets[1].Load(); got != 2 {
		t.Errorf("bucket[<=5] = %d, want 2", got)
	}
	if got := h.buckets[2].Load(); got != 3 {
		t.Errorf("bucket[<=10] = %d, want 3", got)
	}
	if got := h.buckets[3].Load(); got != 4 {
		t.Errorf("bucket[+Inf] = %d, want 4", got)
	}
}

func TestHistogram_Observe_valueEqualToBound_countsInThatBucket(t *testing.T) {
	// le is "less than or equal": an observation exactly equal to a bound
	// must be counted in that bound's cumulative bucket.
	h := NewHistogram("boundary_hist", "test", WithBuckets([]float64{0.1, 0.5, 1}))

	h.Observe(0.1) // exactly the first bound
	h.Observe(0.5) // exactly the second bound
	h.Observe(1)   // exactly the third bound

	_, count, bucketVals := h.snapshot()

	if count != 3 {
		t.Errorf("Observe boundary values: count = %d, want 3", count)
	}
	// Cumulative: le=0.1 -> {0.1}=1; le=0.5 -> {0.1,0.5}=2; le=1 -> all=3; +Inf=3.
	want := []int64{1, 2, 3, 3}
	for i, w := range want {
		if got := bucketVals[i]; got != w {
			t.Errorf("bucket[%d] = %d, want %d (boundary inclusive)", i, got, w)
		}
	}
}

func FuzzHistogram_BucketPlacementInvariant(f *testing.F) {
	f.Add(0.1, 0.5, 1.0, 0.3)
	f.Add(1.0, 5.0, 10.0, 7.0)
	f.Add(-1.0, 0.0, 1.0, 0.0)

	f.Fuzz(func(t *testing.T, b1, b2, b3, obs float64) {
		for _, v := range []float64{b1, b2, b3, obs} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return
			}
		}
		// Contract: bounds must be strictly increasing finite values.
		if !(b1 < b2 && b2 < b3) {
			return
		}
		bounds := []float64{b1, b2, b3}
		h := NewHistogram("fuzz_placement", "fuzz", WithBuckets(bounds))
		h.Observe(obs)

		_, count, bucketVals := h.snapshot()
		if count != 1 {
			t.Fatalf("count = %d, want 1", count)
		}
		// bucket[i] == 1 iff obs <= bounds[i], else 0 (cumulative).
		for i, bound := range bounds {
			want := int64(0)
			if obs <= bound {
				want = 1
			}
			if got := bucketVals[i]; got != want {
				t.Errorf("obs=%v bound[%d]=%v: bucket=%d, want %d", obs, i, bound, got, want)
			}
		}
		// +Inf bucket always counts every observation.
		if got := bucketVals[len(bounds)]; got != 1 {
			t.Errorf("+Inf bucket = %d, want 1", got)
		}
	})
}

func TestWriteProcessMetrics(t *testing.T) {
	var b strings.Builder
	WriteProcessMetrics(&b)
	out := b.String()
	for _, want := range []string{
		"process_goroutines",
		"process_heap_bytes",
		"process_gc_pause_seconds_total",
		"process_uptime_seconds",
		"process_start_time_seconds",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteProcessMetrics missing %q", want)
		}
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		want string
		in   float64
	}{
		// Whole finite values render as bare integers (valid in both formats).
		{in: 1.0, want: "1"},
		{in: 0, want: "0"},
		{in: -1, want: "-1"},
		{in: 42, want: "42"},
		{in: 1e15, want: "1000000000000000"},
		// Beyond the int64-exact range, fall back to shortest 'g'.
		{in: 1e16, want: "1e+16"},
		{in: -1e16, want: "-1e+16"},
		// Fractional values keep full precision (shortest round-trip).
		{in: 0.005, want: "0.005"},
		{in: 0.5, want: "0.5"},
		{in: 0.025, want: "0.025"},
		{in: 3.14, want: "3.14"},
		{in: 1e-7, want: "1e-07"},
		// Non-finite spec tokens (accepted case-insensitively by both formats).
		{in: math.Inf(1), want: "+Inf"},
		{in: math.Inf(-1), want: "-Inf"},
		{in: math.NaN(), want: "NaN"},
	}
	for _, tt := range tests {
		if got := formatValue(tt.in); got != tt.want {
			t.Errorf("formatValue(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLabeledCounterConcurrent(t *testing.T) {
	lc := NewLabeledCounter("conc_lc", "test", []string{"method", "status"})
	done := make(chan struct{})
	for range 100 {
		go func() {
			lc.Inc("GET", "200")
			done <- struct{}{}
		}()
	}
	for range 100 {
		<-done
	}
	key := labelKey{"GET", "200", "", ""}
	if got := lc.vals[key].Load(); got != 100 {
		t.Errorf("concurrent LabeledCounter = %d, want 100", got)
	}
}

func TestHistogramConcurrent(t *testing.T) {
	h := NewHistogram("conc_hist", "test")
	done := make(chan struct{})
	for range 100 {
		go func() {
			h.Observe(0.01)
			done <- struct{}{}
		}()
	}
	for range 100 {
		<-done
	}
	if got := h.count.Load(); got != 100 {
		t.Errorf("concurrent Histogram.count = %d, want 100", got)
	}
}

func TestWriteLabeledCounterSorted(t *testing.T) {
	lc := NewLabeledCounter("sorted_lc", "test", []string{"method", "path", "status"})
	lc.Inc("POST", "/b", "201")
	lc.Inc("GET", "/a", "200")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()
	if !strings.Contains(out, `method="GET",path="/a",status="200"`) {
		t.Errorf("labels not sorted: %s", out)
	}
}

func TestWriteHistogramFormat(t *testing.T) {
	h := NewHistogram("fmt_hist", "test help")
	h.Observe(0.01)

	var b strings.Builder
	WriteHistogram(&b, h)
	out := b.String()

	if !strings.Contains(out, "# HELP fmt_hist test help") {
		t.Error("missing HELP line")
	}
	if !strings.Contains(out, "# TYPE fmt_hist histogram") {
		t.Error("missing TYPE line")
	}
	if !strings.Contains(out, `fmt_hist_bucket{le="+Inf"} 1`) {
		t.Errorf("missing +Inf bucket: %s", out)
	}
	if !strings.Contains(out, "fmt_hist_sum") {
		t.Error("missing _sum line")
	}
	if !strings.Contains(out, "fmt_hist_count 1") {
		t.Error("missing _count line")
	}
}

func TestWriteCounterFormat(t *testing.T) {
	c := NewCounter("http_requests_total", "Total HTTP requests")
	c.Inc()
	c.Inc()
	c.Inc()

	var b strings.Builder
	WriteCounter(&b, c)
	out := b.String()

	if !strings.Contains(out, "# HELP http_requests_total Total HTTP requests") {
		t.Error("missing HELP line")
	}
	if !strings.Contains(out, "# TYPE http_requests_total counter") {
		t.Error("missing TYPE line")
	}
	if !strings.Contains(out, "http_requests_total 3") {
		t.Errorf("missing counter value: %s", out)
	}
}

func TestWriteGaugeFormat(t *testing.T) {
	g := NewGauge("active_connections", "Active connection count")
	g.Inc()
	g.Inc()
	g.Dec()

	var b strings.Builder
	WriteGauge(&b, g)
	out := b.String()

	if !strings.Contains(out, "# HELP active_connections Active connection count") {
		t.Error("missing HELP line")
	}
	if !strings.Contains(out, "# TYPE active_connections gauge") {
		t.Error("missing TYPE line")
	}
	if !strings.Contains(out, "active_connections 1") {
		t.Errorf("missing gauge value: %s", out)
	}
}

func TestWriteCounter_escapes_help(t *testing.T) {
	c := NewCounter("esc_counter", "line1\\line2\nline3")
	c.Inc()

	var b strings.Builder
	WriteCounter(&b, c)
	out := b.String()

	if !strings.Contains(out, `# HELP esc_counter line1\\line2\nline3`) {
		t.Errorf("HELP not escaped correctly: %s", out)
	}
}

func TestLabelValueEscaping(t *testing.T) {
	lc := NewLabeledCounter("esc_lc", "test", []string{"path"})
	lc.Inc("C:\\DIR\\FILE.TXT")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()

	if !strings.Contains(out, `path="C:\\DIR\\FILE.TXT"`) {
		t.Errorf("label value not escaped correctly: %s", out)
	}
}

func TestLabelValueEscapingNewlineAndQuote(t *testing.T) {
	lc := NewLabeledCounter("esc_lc2", "test", []string{"msg"})
	lc.Inc("hello\n\"world\"")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()

	if !strings.Contains(out, `msg="hello\n\"world\""`) {
		t.Errorf("label escaping wrong: %s", out)
	}
}

func TestLabelValueTabNotOverEscaped(t *testing.T) {
	lc := NewLabeledCounter("esc_lc3", "test", []string{"msg"})
	lc.Inc("a\tb") // tab should NOT be escaped

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()

	if !strings.Contains(out, "msg=\"a\tb\"") {
		t.Errorf("tab should pass through unescaped: %s", out)
	}
}

func TestWriteProcessMetrics_uptimeAndStartTimeReconcile(t *testing.T) {
	// process_uptime_seconds and process_start_time_seconds now derive from a
	// single anchor (package-init processStartTime), so start + uptime must
	// reconcile with now -- the inconsistency l-f3 fixed.
	var b strings.Builder
	WriteProcessMetrics(&b)
	out := b.String()

	var uptime, start float64
	var gotUptime, gotStart bool
	for line := range strings.SplitSeq(out, "\n") {
		if v, ok := strings.CutPrefix(line, "process_uptime_seconds "); ok {
			uptime, _ = strconv.ParseFloat(v, 64)
			gotUptime = true
		}
		if v, ok := strings.CutPrefix(line, "process_start_time_seconds "); ok {
			start, _ = strconv.ParseFloat(v, 64)
			gotStart = true
		}
	}
	if !gotUptime || !gotStart {
		t.Fatal("missing process_uptime_seconds or process_start_time_seconds")
	}
	if uptime < 0 {
		t.Errorf("uptime = %.3f, want >= 0", uptime)
	}
	now := float64(time.Now().Unix())
	if diff := now - (start + uptime); diff < -2 || diff > 2 {
		t.Errorf("start(%.0f) + uptime(%.3f) = %.3f, want ~= now(%.0f); diff=%.3f",
			start, uptime, start+uptime, now, diff)
	}
}

func TestMetricNameValidation(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid metric name")
		}
	}()
	NewCounter("invalid-name", "test")
}

func TestLabelNameValidation(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid label name")
		}
	}()
	NewLabeledCounter("valid_name", "test", []string{"invalid-label"})
}

func TestTimer(t *testing.T) {
	h := NewHistogram("timer_test", "test")
	timer := NewTimer(h)
	time.Sleep(10 * time.Millisecond)
	d := timer.ObserveDuration()
	if d < 10*time.Millisecond {
		t.Errorf("timer duration too short: %v", d)
	}
	if h.count.Load() != 1 {
		t.Error("timer did not observe")
	}
}

func TestLabeledHistogram(t *testing.T) {
	lh := NewLabeledHistogram("lh_test", "test", []string{"method"}, WithBuckets([]float64{0.1, 0.5, 1}))
	lh.Observe(0.05, "GET")
	lh.Observe(0.3, "GET")
	lh.Observe(2.0, "POST")

	var b strings.Builder
	WriteLabeledHistogram(&b, lh)
	out := b.String()

	if !strings.Contains(out, "# TYPE lh_test histogram") {
		t.Error("missing TYPE")
	}
	if !strings.Contains(out, `lh_test_bucket{method="GET",le="0.1"} 1`) {
		t.Errorf("missing GET le=0.1 bucket: %s", out)
	}
	if !strings.Contains(out, `lh_test_bucket{method="GET",le="+Inf"} 2`) {
		t.Errorf("missing GET +Inf bucket: %s", out)
	}
	if !strings.Contains(out, `lh_test_bucket{method="POST",le="+Inf"} 1`) {
		t.Errorf("missing POST +Inf bucket: %s", out)
	}
}

func TestLabeledGauge(t *testing.T) {
	lg := NewLabeledGauge("lg_test", "test", []string{"host"})
	lg.Set(42.5, "server1")
	lg.Set(10, "server2")

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	out := b.String()

	if !strings.Contains(out, "# TYPE lg_test gauge") {
		t.Error("missing TYPE")
	}
	if !strings.Contains(out, `lg_test{host="server1"} 42.5`) {
		t.Errorf("missing server1: %s", out)
	}
	if !strings.Contains(out, `lg_test{host="server2"} 10`) {
		t.Errorf("missing server2: %s", out)
	}
}

func TestLabeledGauge_Reset(t *testing.T) {
	lg := NewLabeledGauge("lg_reset", "test", []string{"host"})
	lg.Set(1, "a")
	lg.Set(2, "b")
	lg.Reset()

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	if b.Len() != 0 {
		t.Errorf("expected empty output after Reset, got: %s", b.String())
	}
}

func TestLabeledGauge_Delete(t *testing.T) {
	lg := NewLabeledGauge("lg_delete", "test", []string{"host"})
	lg.Set(1, "a")
	lg.Set(2, "b")
	lg.Delete("a")

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	out := b.String()
	if strings.Contains(out, `host="a"`) {
		t.Errorf("deleted key still present: %s", out)
	}
	if !strings.Contains(out, `host="b"`) {
		t.Errorf("remaining key missing: %s", out)
	}
}

func TestLabeledGauge_DeleteArityPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arity mismatch")
		}
	}()
	lg := NewLabeledGauge("lg_del_panic", "test", []string{"a", "b"})
	lg.Delete("only_one")
}

func TestLabeledCounter_Reset(t *testing.T) {
	lc := NewLabeledCounter("lc_reset_total", "test", []string{"host"})
	lc.Inc("a")
	lc.Inc("b")
	lc.Reset()

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	if b.Len() != 0 {
		t.Errorf("expected empty output after Reset, got: %s", b.String())
	}
}

func TestLabeledCounter_Delete(t *testing.T) {
	lc := NewLabeledCounter("lc_delete_total", "test", []string{"host"})
	lc.Inc("a")
	lc.Inc("b")
	lc.Delete("a")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()
	if strings.Contains(out, `host="a"`) {
		t.Errorf("deleted key still present: %s", out)
	}
	if !strings.Contains(out, `host="b"`) {
		t.Errorf("remaining key missing: %s", out)
	}
}

func TestLabeledCounter_DeleteArityPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arity mismatch")
		}
	}()
	lc := NewLabeledCounter("lc_del_panic_total", "test", []string{"a", "b"})
	lc.Delete("only_one")
}

func TestLabeledHistogram_Reset(t *testing.T) {
	lh := NewLabeledHistogram("lh_reset", "test", []string{"host"}, WithBuckets([]float64{0.1, 1}))
	lh.Observe(0.05, "a")
	lh.Observe(0.05, "b")
	lh.Reset()

	var b strings.Builder
	WriteLabeledHistogram(&b, lh)
	if b.Len() != 0 {
		t.Errorf("expected empty output after Reset, got: %s", b.String())
	}
}

func TestLabeledHistogram_Delete(t *testing.T) {
	lh := NewLabeledHistogram("lh_delete", "test", []string{"host"}, WithBuckets([]float64{0.1, 1}))
	lh.Observe(0.05, "a")
	lh.Observe(0.05, "b")
	lh.Delete("a")

	var b strings.Builder
	WriteLabeledHistogram(&b, lh)
	out := b.String()
	if strings.Contains(out, `host="a"`) {
		t.Errorf("deleted key still present: %s", out)
	}
	if !strings.Contains(out, `host="b"`) {
		t.Errorf("remaining key missing: %s", out)
	}
}

func TestLabeledHistogram_DeleteArityPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arity mismatch")
		}
	}()
	lh := NewLabeledHistogram("lh_del_panic", "test", []string{"a", "b"})
	lh.Delete("only_one")
}

func TestLabeledGauge_ResetConcurrent(t *testing.T) {
	lg := NewLabeledGauge("lg_conc_reset", "test", []string{"id"})
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			for j := range 20 {
				lg.Set(float64(j), strconv.Itoa(i))
			}
		})
	}
	// Concurrent resets
	for range 10 {
		wg.Go(func() {
			lg.Reset()
		})
	}
	wg.Wait()
}

func TestLabeledGauge_DeleteConcurrent(t *testing.T) {
	lg := NewLabeledGauge("lg_conc_del", "test", []string{"id"})
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			key := strconv.Itoa(i)
			lg.Set(float64(i), key)
			lg.Delete(key)
		})
	}
	wg.Wait()
}

func FuzzHistogramObserve(f *testing.F) {
	f.Add(0.001)
	f.Add(0.5)
	f.Add(1.0)
	f.Add(10.0)
	f.Add(math.MaxFloat64)
	f.Add(0.0)
	f.Add(-1.0)

	h := NewHistogram("fuzz_test", "fuzz")

	f.Fuzz(func(t *testing.T, val float64) {
		countBefore := h.count.Load()
		h.Observe(val)
		countAfter := h.count.Load()

		if countAfter != countBefore+1 {
			t.Errorf("count did not increment: before=%d after=%d", countBefore, countAfter)
		}

		if !math.IsNaN(val) && !math.IsInf(val, 0) {
			sumBits := h.sumBits.Load()
			sum := math.Float64frombits(sumBits)
			if math.IsNaN(sum) {
				t.Error("sum became NaN from finite input")
			}
		}

		var prev int64
		for i := range h.buckets {
			cur := h.buckets[i].Load()
			if cur < prev {
				t.Errorf("bucket[%d] count %d < prev %d", i, cur, prev)
			}
			prev = cur
		}
	})
}

func FuzzLabelValueExposition(f *testing.F) {
	f.Add("simple")
	f.Add("with\"quote")
	f.Add("with\\backslash")
	f.Add("with\nnewline")
	f.Add("null\x00byte")
	f.Add("emoji🎉")
	f.Add("")
	f.Add(strings.Repeat("x", 500))

	f.Fuzz(func(t *testing.T, val string) {
		r := NewRegistry("")
		lc := NewLabeledCounter("fuzz_counter", "fuzz help", []string{"v"})
		lg := NewLabeledGauge("fuzz_gauge", "fuzz help", []string{"v"})
		r.RegisterLabeledCounter(lc)
		r.RegisterLabeledGauge(lg)
		lc.Inc(val)
		lg.Set(1.0, val)

		// Prometheus format
		var b strings.Builder
		WriteLabeledCounter(&b, lc)
		out := b.String()
		// Must not contain raw unescaped newlines inside a label value line
		for line := range strings.SplitSeq(out, "\n") {
			if strings.HasPrefix(line, "#") || line == "" {
				continue
			}
			// Each non-comment sample line must be parseable: metric{labels} value
			if !strings.Contains(line, "{") {
				continue
			}
			// Verify we can find closing brace after opening brace
			braceOpen := strings.Index(line, "{")
			braceClose := strings.LastIndex(line, "}")
			if braceOpen >= 0 && braceClose < braceOpen {
				t.Errorf("malformed label section: %s", line)
			}
		}

		// OpenMetrics format
		b.Reset()
		writeOMLabeledGauge(&b, lg)
		omOut := b.String()
		for line := range strings.SplitSeq(omOut, "\n") {
			if strings.HasPrefix(line, "#") || line == "" {
				continue
			}
			if !strings.Contains(line, "{") {
				continue
			}
			braceOpen := strings.Index(line, "{")
			braceClose := strings.LastIndex(line, "}")
			if braceOpen >= 0 && braceClose < braceOpen {
				t.Errorf("malformed OM label section: %s", line)
			}
		}
	})
}

func TestNewLabeledGauge_TooManyLabelsPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for >4 labels")
		}
	}()
	NewLabeledGauge("lg_many", "test", []string{"a", "b", "c", "d", "e"})
}

func TestNewLabeledHistogram_TooManyLabelsPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for >4 labels")
		}
	}()
	NewLabeledHistogram("lh_many", "test", []string{"a", "b", "c", "d", "e"})
}

func TestWriteCounter_escapes_carriage_return(t *testing.T) {
	c := NewCounter("cr_counter", "line1\rline2")
	c.Inc()

	var b strings.Builder
	WriteCounter(&b, c)
	out := b.String()

	if !strings.Contains(out, `# HELP cr_counter line1\rline2`) {
		t.Errorf("CR in HELP not escaped to \\r: %q", out)
	}
	if strings.Contains(out, "line1\rline2") {
		t.Errorf("raw carriage return leaked into exposition output: %q", out)
	}
}

func TestLabelValueEscapingCarriageReturn(t *testing.T) {
	lc := NewLabeledCounter("esc_cr_lc", "test", []string{"msg"})
	lc.Inc("a\rb")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()

	if !strings.Contains(out, `msg="a\rb"`) {
		t.Errorf("CR in label value not escaped to \\r: %q", out)
	}
	if strings.Contains(out, "a\rb") {
		t.Errorf("raw carriage return leaked into label value: %q", out)
	}
}

func TestHistogramSnapshot(t *testing.T) {
	h := NewHistogram("snap_test", "snapshot",
		WithBuckets([]float64{0.1, 0.5, 1}))
	h.Observe(0.05) // lands in <=0.1, <=0.5, <=1, +Inf
	h.Observe(0.3)  // lands in <=0.5, <=1, +Inf
	h.Observe(2.0)  // lands in +Inf only
	sum, count, buckets := h.snapshot()
	// count=3, sum=2.35, cumulative buckets=[1,2,2,3]
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	wantSum := 0.05 + 0.3 + 2.0
	if math.Abs(sum-wantSum) > 1e-9 {
		t.Errorf("sum = %v, want %v", sum, wantSum)
	}
	want := []int64{1, 2, 2, 3}
	for i := range want {
		if buckets[i] != want[i] {
			t.Errorf("buckets[%d] = %d, want %d", i, buckets[i], want[i])
		}
	}
}

func TestSortedLabelKeys_returnsLexicographicOrder(t *testing.T) {
	var mu sync.RWMutex
	vals := map[labelKey]int{
		{"b", "2", "", ""}: 1,
		{"a", "9", "", ""}: 1,
		{"a", "1", "", ""}: 1,
		{"c", "0", "", ""}: 1,
	}
	got := sortedLabelKeys(&mu, vals)
	want := []labelKey{
		{"a", "1", "", ""},
		{"a", "9", "", ""},
		{"b", "2", "", ""},
		{"c", "0", "", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortedLabelKeys() = %v, want %v", got, want)
	}
}

func TestSortedLabelKeys_empty(t *testing.T) {
	var mu sync.RWMutex
	if got := sortedLabelKeys(&mu, map[labelKey]int{}); len(got) != 0 {
		t.Errorf("sortedLabelKeys(empty) = %v, want empty", got)
	}
}

func TestLabeledCounterAdd(t *testing.T) {
	lc := NewLabeledCounter("test_lc_add", "test", []string{"method", "status"})
	lc.Add(5, "GET", "200") // new key: Store(5)
	lc.Add(3, "GET", "200") // existing key: Add(3) -> 8
	lc.Add(10, "POST", "201")

	key := labelKey{"GET", "200", "", ""}
	if got := lc.vals[key].Load(); got != 8 {
		t.Errorf("LabeledCounter.Add[GET,200] = %d, want 8", got)
	}
	key2 := labelKey{"POST", "201", "", ""}
	if got := lc.vals[key2].Load(); got != 10 {
		t.Errorf("LabeledCounter.Add[POST,201] = %d, want 10", got)
	}
}

func TestLabeledCounterAdd_zeroOnNewKey(t *testing.T) {
	lc := NewLabeledCounter("test_lc_add_zero", "test", []string{"k"})
	lc.Add(0, "a")
	key := labelKey{"a", "", "", ""}
	v, ok := lc.vals[key]
	if !ok {
		t.Fatal("Add(0) should create the label entry")
	}
	if got := v.Load(); got != 0 {
		t.Errorf("LabeledCounter.Add(0)[a] = %d, want 0", got)
	}
}

func TestLabeledCounterAdd_negativePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative Add")
		}
	}()
	lc := NewLabeledCounter("test_lc_add_neg", "test", []string{"k"})
	lc.Add(-1, "a") // correct arity: hits the negative guard, not the arity guard
}

func TestLabeledCounterAdd_arityMismatchPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arity mismatch")
		}
	}()
	lc := NewLabeledCounter("test_lc_add_arity", "test", []string{"method", "status"})
	lc.Add(1, "GET") // n>=0 passes negative guard, then arity guard fires
}

func TestWriteHistogram_SumRendering(t *testing.T) {
	t.Run("tiny sum keeps precision", func(t *testing.T) {
		h := NewHistogram("hist_sum_tiny", "test")
		h.Observe(1e-7) // pre-l-f3 %.6f floored this sum to "0.000000"
		var b strings.Builder
		WriteHistogram(&b, h)
		if got := b.String(); !strings.Contains(got, "hist_sum_tiny_sum 1e-07\n") {
			t.Errorf("WriteHistogram sum = %q, want line %q", got, "hist_sum_tiny_sum 1e-07")
		}
	})
	t.Run("whole sum renders as bare integer", func(t *testing.T) {
		h := NewHistogram("hist_sum_whole", "test")
		h.Observe(2)
		h.Observe(2) // sum = 4; pre-l-f3 %.6f rendered "4.000000"
		var b strings.Builder
		WriteHistogram(&b, h)
		if got := b.String(); !strings.Contains(got, "hist_sum_whole_sum 4\n") {
			t.Errorf("WriteHistogram sum = %q, want line %q", got, "hist_sum_whole_sum 4")
		}
	})
}
