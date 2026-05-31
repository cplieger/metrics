package metrics

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	r := NewRegistry("test")

	httpReqs := NewLabeledCounter("test_http_requests_total", "Total HTTP requests", []string{"method", "path", "status"})
	sseClients := NewGauge("test_sse_clients", "Current SSE client count")
	spawns := NewCounter("test_bridge_spawns_total", "Total bridge spawns")
	pushSends := NewCounter("test_push_sends_total", "Total push notification sends")
	httpDur := NewHistogram("test_http_request_duration_seconds", "HTTP request latency")

	r.RegisterLabeledCounter(httpReqs)
	r.RegisterGauge(sseClients)
	r.RegisterCounter(spawns)
	r.RegisterCounter(pushSends)
	r.RegisterHistogram(httpDur)

	httpReqs.Inc("GET", "/api", "200")
	httpDur.Observe(0.05)
	spawns.Inc()

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
		"test_bridge_spawns_total",
		"test_push_sends_total",
		"test_sse_clients",
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
	r := NewRegistry("bench")
	httpReqs := NewLabeledCounter("bench_http_requests_total", "Total HTTP requests", []string{"method", "path", "status"})
	httpDur := NewHistogram("bench_http_request_duration_seconds", "HTTP request latency")
	spawns := NewCounter("bench_bridge_spawns_total", "Total bridge spawns")
	r.RegisterLabeledCounter(httpReqs)
	r.RegisterHistogram(httpDur)
	r.RegisterCounter(spawns)

	httpReqs.Inc("GET", "/api", "200")
	httpDur.Observe(0.05)
	spawns.Inc()

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

func TestGaugeIncDec(t *testing.T) {
	g := NewGauge("test_gauge", "test")
	g.Inc()
	g.Inc()
	g.Dec()
	if got := g.val.Load(); got != 1 {
		t.Errorf("Gauge = %d, want 1", got)
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
	// bucket[0] (<=0.005) should have 1
	if got := h.buckets[0].Load(); got != 1 {
		t.Errorf("bucket[0] = %d, want 1", got)
	}
	// +Inf bucket should have 3
	if got := h.buckets[len(DefaultBuckets)].Load(); got != 3 {
		t.Errorf("bucket[+Inf] = %d, want 3", got)
	}
}

func TestImageMetrics(t *testing.T) {
	// Reset global state
	SetImageMetrics(nil)

	var b strings.Builder
	WriteImageMetrics(&b, "test")
	if b.Len() != 0 {
		t.Error("expected empty output for nil images")
	}

	SetImageMetrics([]ImageMetric{
		{Registry: "dockerhub", Owner: "lib", Repo: "nginx", Pulls: 1000, Tags: 5},
		{Registry: "ghcr", Owner: "user", Repo: "app", Pulls: 50, Tags: 0},
	})

	b.Reset()
	WriteImageMetrics(&b, "test")
	out := b.String()
	if !strings.Contains(out, "test_image_pulls_total") {
		t.Error("missing image_pulls_total")
	}
	if !strings.Contains(out, "test_image_tags") {
		t.Error("missing image_tags")
	}
	if !strings.Contains(out, `registry="dockerhub"`) {
		t.Error("missing dockerhub label")
	}

	// Clean up
	SetImageMetrics(nil)
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
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteProcessMetrics missing %q", want)
		}
	}
}

func TestFormatBound(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{1.0, "1"},
		{0.005, "0.005"},
		{0.5, "0.5"},
		{0.025, "0.025"},
	}
	for _, tt := range tests {
		if got := FormatBound(tt.in); got != tt.want {
			t.Errorf("FormatBound(%v) = %q, want %q", tt.in, got, tt.want)
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

	// Labels within each line should be sorted alphabetically
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
