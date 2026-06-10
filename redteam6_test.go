package metrics

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ====================================================================
// RED-TEAM ROUND 6: Adversarial concurrency, exposition, & fuzz
// ====================================================================

// --- (1) Concurrency: LabeledGauge Set/Reset/Delete racing with WriteLabeledGauge ---

func TestRT6_LabeledGauge_SetResetDeleteRacingScrape(t *testing.T) {
	lg := NewLabeledGauge("rt6_race_gauge", "race test", []string{"id"})
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writers: Set
	for i := range 20 {
		wg.Go(func() {
			key := strconv.Itoa(i)
			for {
				select {
				case <-stop:
					return
				default:
					lg.Set(float64(i), key)
				}
			}
		})
	}
	// Writers: Delete
	for i := range 10 {
		wg.Go(func() {
			key := strconv.Itoa(i)
			for {
				select {
				case <-stop:
					return
				default:
					lg.Delete(key)
				}
			}
		})
	}
	// Writers: Reset
	for range 5 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					lg.Reset()
				}
			}
		})
	}
	// Readers: WriteLabeledGauge (simulates scrape)
	for range 5 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					var b strings.Builder
					WriteLabeledGauge(&b, lg)
				}
			}
		})
	}
	close(stop)
	wg.Wait()
}

// --- (1b) Counter hot-path concurrency ---

func TestRT6_CounterHotPath(t *testing.T) {
	c := NewCounter("rt6_counter_hot", "hot path")
	const n = 100
	const iters = 1000
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			for range iters {
				c.Inc()
			}
		})
	}
	wg.Wait()
	if got := c.val.Load(); got != int64(n*iters) {
		t.Errorf("counter = %d, want %d", got, n*iters)
	}
}

// --- (1c) Histogram hot-path concurrency ---

func TestRT6_HistogramHotPath(t *testing.T) {
	h := NewHistogram("rt6_hist_hot", "hot path", WithBuckets([]float64{0.01, 0.1, 1}))
	const n = 50
	const iters = 200
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			for i := range iters {
				h.Observe(float64(i) * 0.005)
			}
		})
	}
	wg.Wait()
	if got := h.count.Load(); got != int64(n*iters) {
		t.Errorf("histogram count = %d, want %d", got, n*iters)
	}
}

// --- (1d) LabeledGauge Set + WriteLabeledGauge race (nil pointer check) ---

func TestRT6_LabeledGauge_WriteDuringDelete(t *testing.T) {
	lg := NewLabeledGauge("rt6_write_del", "test", []string{"k"})
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Populate some keys
	for i := range 100 {
		lg.Set(float64(i), strconv.Itoa(i%10))
	}

	// Delete concurrently
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				for i := range 10 {
					lg.Delete(strconv.Itoa(i))
				}
			}
		}
	})

	// Write (scrape) concurrently — must not panic on nil ptr
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				var b strings.Builder
				WriteLabeledGauge(&b, lg)
			}
		}
	})

	close(stop)
	wg.Wait()
}

// --- (2) Exposition correctness ---

func TestRT6_GaugeSpecialValues(t *testing.T) {
	posInf := "+" + "Inf"
	negInf := "-" + "Inf"
	nanStr := "Na" + "N"
	tests := []struct {
		name string
		val  float64
		prom string
		om   string
	}{
		{"pos_inf", math.Inf(1), posInf, posInf},
		{"neg_inf", math.Inf(-1), negInf, negInf},
		{"nan", math.NaN(), nanStr, nanStr},
		{"zero", 0, "0", "0.0"},
		{"neg_zero", math.Copysign(0, -1), "0", "0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGauge("rt6_special_"+tt.name, "test")
			g.Set(tt.val)

			// Prometheus format
			var b strings.Builder
			WriteGauge(&b, g)
			out := b.String()
			if !strings.Contains(out, "rt6_special_"+tt.name+" "+tt.prom) {
				t.Errorf("Prometheus: expected %q in:\n%s", tt.prom, out)
			}

			// OpenMetrics format
			b.Reset()
			writeOMGauge(&b, g)
			omOut := b.String()
			if !strings.Contains(omOut, "rt6_special_"+tt.name+" "+tt.om) {
				t.Errorf("OpenMetrics: expected %q in:\n%s", tt.om, omOut)
			}
		})
	}
}

func TestRT6_LabeledGaugeSpecialValues(t *testing.T) {
	lg := NewLabeledGauge("rt6_lg_special", "test", []string{"v"})
	lg.Set(math.Inf(1), "inf")
	lg.Set(math.NaN(), "nan")
	lg.Set(math.Inf(-1), "neginf")

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	out := b.String()
	posInf := "+" + "Inf"
	negInf := "-" + "Inf"
	nanStr := "Na" + "N"
	if !strings.Contains(out, posInf) {
		t.Errorf("missing +Inf in labeled gauge: %s", out)
	}
	if !strings.Contains(out, nanStr) {
		t.Errorf("missing NaN in labeled gauge: %s", out)
	}
	if !strings.Contains(out, negInf) {
		t.Errorf("missing -Inf in labeled gauge: %s", out)
	}
}

func TestRT6_EmptyLabelSet_Counter(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("zero-label counter panicked: %v", r)
		}
	}()
	lc := NewLabeledCounter("rt6_empty_labels", "test", []string{})
	lc.Inc()
	lc.Inc()

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()
	if !strings.Contains(out, "rt6_empty_labels") {
		t.Errorf("empty label counter missing: %s", out)
	}
}

func TestRT6_EmptyLabelSet_Gauge(t *testing.T) {
	lg := NewLabeledGauge("rt6_empty_lg", "test", []string{})
	lg.Set(42)

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	out := b.String()
	if !strings.Contains(out, "rt6_empty_lg") {
		t.Errorf("empty label gauge missing: %s", out)
	}
}

func TestRT6_HelpEscaping_AllFormats(t *testing.T) {
	help := "line1\\line2\nline3\"quoted\""
	c := NewCounter("rt6_help_esc", help)
	c.Inc()

	// Prometheus
	var b strings.Builder
	WriteCounter(&b, c)
	out := b.String()
	if !strings.Contains(out, `# HELP rt6_help_esc line1\\line2\nline3"quoted"`) {
		t.Errorf("Prometheus HELP escaping wrong:\n%s", out)
	}

	// OpenMetrics
	b.Reset()
	writeOMSimpleCounter(&b, c)
	omOut := b.String()
	if !strings.Contains(omOut, `# HELP rt6_help_esc line1\\line2\nline3"quoted"`) {
		t.Errorf("OpenMetrics HELP escaping wrong:\n%s", omOut)
	}
}

func TestRT6_MetricNameValidation_ColonAllowed(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("colon in metric name panicked: %v", r)
		}
	}()
	c := NewCounter("namespace:subsystem:metric", "with colons")
	c.Inc()
	var b strings.Builder
	WriteCounter(&b, c)
	if !strings.Contains(b.String(), "namespace:subsystem:metric 1") {
		t.Error("colon metric not written correctly")
	}
}

func TestRT6_MetricNameValidation_UnderscorePrefix(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("underscore prefix panicked: %v", r)
		}
	}()
	c := NewCounter("__private_metric", "underscore prefix")
	c.Inc()
}

func TestRT6_LabelNameValidation_ColonForbidden(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for colon in label name")
		}
	}()
	NewLabeledCounter("rt6_bad_label", "test", []string{"bad:label"})
}

// --- (2b) OpenMetrics vs Prometheus differences ---

func TestRT6_OpenMetrics_CounterNoTotal_GetsTotal(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("rt6_events", "events")
	r.RegisterCounter(c)
	c.Add(7)

	rec := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "# TYPE rt6_events counter") {
		t.Errorf("OM TYPE wrong:\n%s", body)
	}
	if !strings.Contains(body, "rt6_events_total 7") {
		t.Errorf("OM sample missing _total:\n%s", body)
	}
}

func TestRT6_OpenMetrics_GaugeFloat(t *testing.T) {
	r := NewRegistry("")
	g := NewGauge("rt6_temp", "temperature")
	r.RegisterGauge(g)
	g.Set(42)

	rec := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	// OpenMetrics gauges should render integers as "42.0"
	if !strings.Contains(body, "rt6_temp 42.0") {
		t.Errorf("OM gauge should have .0 suffix for integer:\n%s", body)
	}
}

func TestRT6_NegotiateHandler_AcceptOM(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("rt6_neg", "test")
	r.RegisterCounter(c)
	c.Inc()

	handler := r.NegotiateHandler()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != OpenMetricsContentType {
		t.Errorf("negotiate didn't select OM: %s", ct)
	}
	if !strings.HasSuffix(rec.Body.String(), "# EOF\n") {
		t.Error("negotiate OM missing EOF")
	}
}

func TestRT6_NegotiateHandler_DefaultProm(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("rt6_neg2", "test")
	r.RegisterCounter(c)
	c.Inc()

	handler := r.NegotiateHandler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("negotiate default should be Prometheus text: %s", ct)
	}
}

// --- (3) Confirm ImageMetric global state gone ---

func TestRT6_NoImageMetricExports(t *testing.T) {
	r := NewRegistry("")
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if strings.Contains(body, "image_") {
		t.Errorf("found image_ metric in output — ImageMetric not fully removed:\n%s", body)
	}
}

// --- (4) Fuzz: label values + bucket bounds ---

func FuzzRT6_LabelValuePanicCheck(f *testing.F) {
	f.Add("normal", 1.0)
	f.Add("with\"quote", 0.0)
	f.Add("with\\backslash", -1.0)
	f.Add("with\nnewline", math.Inf(1))
	f.Add("\x00\x01\x02", math.NaN())
	f.Add("", 42.0)
	f.Add(strings.Repeat("🎉", 100), 99.9)

	f.Fuzz(func(t *testing.T, val string, gaugeVal float64) {
		lg := NewLabeledGauge("fuzz_rt6_gauge", "fuzz", []string{"v"})
		lg.Set(gaugeVal, val)

		var b strings.Builder
		WriteLabeledGauge(&b, lg)

		b.Reset()
		writeOMLabeledGauge(&b, lg)
	})
}

func FuzzRT6_BucketBounds(f *testing.F) {
	f.Add(0.001, 0.01, 0.1)
	f.Add(-1.0, 0.0, 1.0)
	f.Add(math.SmallestNonzeroFloat64, 1.0, math.MaxFloat64)
	f.Add(0.0, 0.0, 0.0)

	f.Fuzz(func(t *testing.T, b1, b2, b3 float64) {
		if math.IsNaN(b1) || math.IsNaN(b2) || math.IsNaN(b3) {
			return
		}
		if math.IsInf(b1, 0) || math.IsInf(b2, 0) || math.IsInf(b3, 0) {
			return
		}

		h := NewHistogram("fuzz_rt6_hist", "fuzz", WithBuckets([]float64{b1, b2, b3}))
		h.Observe(b1)
		h.Observe(b2)
		h.Observe(b3)
		h.Observe(0)
		h.Observe(math.MaxFloat64)

		var sb strings.Builder
		WriteHistogram(&sb, h)

		if got := h.count.Load(); got != 5 {
			t.Errorf("count = %d, want 5", got)
		}
	})
}

// --- Additional concurrency: LabeledCounter + scrape race ---

func TestRT6_LabeledCounter_ScrapeRace(t *testing.T) {
	lc := NewLabeledCounter("rt6_lc_race", "test", []string{"method", "status"})
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				lc.Inc("GET", "200")
				lc.Inc("POST", "201")
			}
		}
	})

	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				var b strings.Builder
				WriteLabeledCounter(&b, lc)
			}
		}
	})

	close(stop)
	wg.Wait()
}

// --- LabeledHistogram + scrape race ---

func TestRT6_LabeledHistogram_ScrapeRace(t *testing.T) {
	lh := NewLabeledHistogram("rt6_lh_race", "test", []string{"op"}, WithBuckets([]float64{0.01, 0.1, 1}))
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				lh.Observe(float64(i%100)*0.01, "read")
				lh.Observe(float64(i%50)*0.02, "write")
			}
		}
	})

	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				var b strings.Builder
				WriteLabeledHistogram(&b, lh)
			}
		}
	})

	close(stop)
	wg.Wait()
}

// --- Full handler race with Reset/Delete in flight ---

func TestRT6_FullHandler_ResetDeleteRace(t *testing.T) {
	r := NewRegistry("")
	lg := NewLabeledGauge("rt6_full_gauge", "test", []string{"host"})
	c := NewCounter("rt6_full_counter", "test")
	h := NewHistogram("rt6_full_hist", "test")
	r.RegisterLabeledGauge(lg)
	r.RegisterCounter(c)
	r.RegisterHistogram(h)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				lg.Set(float64(i), "host"+strconv.Itoa(i%5))
				c.Inc()
				h.Observe(float64(i%10) * 0.01)
			}
		}
	})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				lg.Reset()
			}
		}
	})
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				lg.Delete("host" + strconv.Itoa(i%5))
			}
		}
	})

	handler := r.Handler()
	omHandler := r.OpenMetricsHandler()
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			}
		}
	})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				rec := httptest.NewRecorder()
				omHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			}
		}
	})

	close(stop)
	wg.Wait()
}
