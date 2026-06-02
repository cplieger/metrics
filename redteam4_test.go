package metrics

import (
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ====================================================================
// REFACTOR-SPECIFIC PROBES: functional-options histogram API
// ====================================================================

// TestRefactor_DefaultBucketsParity verifies NewHistogram with no options
// uses exactly DefaultBuckets (parity with old NewHistogram).
func TestRefactor_DefaultBucketsParity(t *testing.T) {
	h := NewHistogram("refactor_default", "test")
	expected := make([]float64, len(DefaultBuckets))
	copy(expected, DefaultBuckets)
	sort.Float64s(expected)
	if !reflect.DeepEqual(h.bounds, expected) {
		t.Errorf("NewHistogram no-opts bounds = %v, want DefaultBuckets %v", h.bounds, expected)
	}
}

// TestRefactor_LabeledDefaultBucketsParity verifies NewLabeledHistogram with no options
// uses exactly DefaultBuckets.
func TestRefactor_LabeledDefaultBucketsParity(t *testing.T) {
	lh := NewLabeledHistogram("refactor_lh_default", "test", []string{"k"})
	expected := make([]float64, len(DefaultBuckets))
	copy(expected, DefaultBuckets)
	sort.Float64s(expected)
	if !reflect.DeepEqual(lh.bounds, expected) {
		t.Errorf("NewLabeledHistogram no-opts bounds = %v, want DefaultBuckets %v", lh.bounds, expected)
	}
}

// TestRefactor_WithBucketsOverridesDefault verifies WithBuckets replaces defaults.
func TestRefactor_WithBucketsOverridesDefault(t *testing.T) {
	custom := []float64{1, 2, 3}
	h := NewHistogram("refactor_custom", "test", WithBuckets(custom))
	if !reflect.DeepEqual(h.bounds, []float64{1, 2, 3}) {
		t.Errorf("bounds = %v, want [1 2 3]", h.bounds)
	}
}

// TestRefactor_WithBucketsSorts verifies unsorted buckets get sorted.
func TestRefactor_WithBucketsSorts(t *testing.T) {
	h := NewHistogram("refactor_unsorted", "test", WithBuckets([]float64{5, 1, 3}))
	if !reflect.DeepEqual(h.bounds, []float64{1, 3, 5}) {
		t.Errorf("bounds = %v, want [1 3 5]", h.bounds)
	}
}

// TestRefactor_WithBucketsEmpty verifies empty bucket slice works.
func TestRefactor_WithBucketsEmpty(t *testing.T) {
	h := NewHistogram("refactor_empty", "test", WithBuckets([]float64{}))
	if len(h.bounds) != 0 {
		t.Errorf("expected 0 bounds, got %d", len(h.bounds))
	}
	// Must still work: only +Inf bucket
	h.Observe(1.0)
	if h.count.Load() != 1 {
		t.Error("observe failed with empty buckets")
	}
	if h.buckets[0].Load() != 1 {
		t.Errorf("+Inf bucket = %d, want 1", h.buckets[0].Load())
	}
}

// TestRefactor_WithBucketsDuplicates verifies duplicates are handled safely.
func TestRefactor_WithBucketsDuplicates(t *testing.T) {
	h := NewHistogram("refactor_dup", "test", WithBuckets([]float64{1, 1, 5, 5}))
	// After sort: [1, 1, 5, 5]
	h.Observe(3)
	// 3 > 1, 3 > 1, 3 <= 5 (at index 2): inner loop increments buckets[2], buckets[3]
	// +Inf always incremented
	if h.count.Load() != 1 {
		t.Errorf("count = %d", h.count.Load())
	}
}

// TestRefactor_WithBucketsNaN verifies NaN bucket boundaries don't cause panic or hang.
func TestRefactor_WithBucketsNaN(t *testing.T) {
	h := NewHistogram("refactor_nan", "test", WithBuckets([]float64{math.NaN(), 0.5, 1.0}))
	h.Observe(0.3)
	h.Observe(0.7)
	h.Observe(2.0)
	if h.count.Load() != 3 {
		t.Errorf("count = %d, want 3", h.count.Load())
	}
}

// TestRefactor_WithBucketsPosInf verifies +Inf in user bounds is safe.
func TestRefactor_WithBucketsPosInf(t *testing.T) {
	h := NewHistogram("refactor_posinf", "test", WithBuckets([]float64{0.5, math.Inf(1)}))
	h.Observe(0.3)
	h.Observe(100)
	if h.count.Load() != 2 {
		t.Errorf("count = %d, want 2", h.count.Load())
	}
}

// TestRefactor_OptionOrderIndependent verifies option ordering doesn't matter.
// (Only one option type exists currently, but test the pattern.)
func TestRefactor_OptionOrderIndependent(t *testing.T) {
	// Apply WithBuckets; the last one wins (or the only one).
	h1 := NewHistogram("refactor_order1", "test", WithBuckets([]float64{1, 2}))
	h2 := NewHistogram("refactor_order2", "test", WithBuckets([]float64{2, 1}))
	if !reflect.DeepEqual(h1.bounds, h2.bounds) {
		t.Errorf("option order produced different results: %v vs %v", h1.bounds, h2.bounds)
	}
}

// TestRefactor_NilOptionSafe verifies passing a nil option doesn't panic.
func TestRefactor_NilOptionSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil option panicked: %v", r)
		}
	}()
	var nilOpt Option
	// This will panic if nil func is called — testing safety.
	NewHistogram("refactor_nil_opt", "test", nilOpt)
}

// TestRefactor_WithBucketsDoesNotMutateInput verifies the input slice isn't modified.
func TestRefactor_WithBucketsDoesNotMutateInput(t *testing.T) {
	input := []float64{5, 3, 1}
	original := make([]float64, len(input))
	copy(original, input)
	NewHistogram("refactor_nomutate", "test", WithBuckets(input))
	if !reflect.DeepEqual(input, original) {
		t.Errorf("WithBuckets mutated input: got %v, want %v", input, original)
	}
}

// TestRefactor_MultipleWithBucketsLastWins verifies multiple WithBuckets: last wins.
func TestRefactor_MultipleWithBucketsLastWins(t *testing.T) {
	h := NewHistogram("refactor_multi", "test",
		WithBuckets([]float64{1, 2, 3}),
		WithBuckets([]float64{10, 20}),
	)
	if !reflect.DeepEqual(h.bounds, []float64{10, 20}) {
		t.Errorf("expected last WithBuckets to win, got %v", h.bounds)
	}
}

// ====================================================================
// RE-ATTACK: exposition escaping (Prometheus + OpenMetrics)
// ====================================================================

func TestReattack_PrometheusEscaping_AllSpecialChars(t *testing.T) {
	lc := NewLabeledCounter("reattack_esc", "help with\nnewline and \\backslash", []string{"v"})
	lc.Inc("val with\nnewline")
	lc.Inc("val with \"quotes\"")
	lc.Inc("val with \\backslash")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()

	// Help text escaping
	if !strings.Contains(out, `# HELP reattack_esc help with\nnewline and \\backslash`) {
		t.Errorf("help escaping wrong: %s", out)
	}
	// Label value escaping
	if !strings.Contains(out, `v="val with\nnewline"`) {
		t.Errorf("newline in label not escaped: %s", out)
	}
	if !strings.Contains(out, `v="val with \"quotes\""`) {
		t.Errorf("quotes in label not escaped: %s", out)
	}
	if !strings.Contains(out, `v="val with \\backslash"`) {
		t.Errorf("backslash in label not escaped: %s", out)
	}
}

func TestReattack_OpenMetricsEscaping(t *testing.T) {
	r := NewRegistry("test")
	lc := NewLabeledCounter("om_esc", "help\nline", []string{"v"})
	r.RegisterLabeledCounter(lc)
	lc.Inc("quote\"here")

	rec := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `v="quote\"here"`) {
		t.Errorf("OM label escaping wrong: %s", body)
	}
	if strings.Contains(body, "_total_total") {
		t.Errorf("double _total: %s", body)
	}
}

// ====================================================================
// RE-ATTACK: _total suffix not doubled
// ====================================================================

func TestReattack_TotalSuffixNotDoubled_Simple(t *testing.T) {
	r := NewRegistry("test")
	c := NewCounter("events_total", "events")
	r.RegisterCounter(c)
	c.Inc()

	rec := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()

	if strings.Contains(body, "_total_total") {
		t.Fatalf("double _total: %s", body)
	}
	if !strings.Contains(body, "# TYPE events counter") {
		t.Errorf("TYPE wrong: %s", body)
	}
	if !strings.Contains(body, "events_total 1") {
		t.Errorf("sample wrong: %s", body)
	}
}

func TestReattack_TotalSuffixNotDoubled_Labeled(t *testing.T) {
	r := NewRegistry("test")
	lc := NewLabeledCounter("req_total", "requests", []string{"m"})
	r.RegisterLabeledCounter(lc)
	lc.Inc("GET")

	rec := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()

	if strings.Contains(body, "_total_total") {
		t.Fatalf("double _total: %s", body)
	}
}

// ====================================================================
// RE-ATTACK: label arity panic
// ====================================================================

func TestReattack_LabelArityPanic_Counter(t *testing.T) {
	lc := NewLabeledCounter("arity_c", "test", []string{"a", "b"})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong arity")
		}
	}()
	lc.Inc("only_one")
}

func TestReattack_LabelArityPanic_Gauge(t *testing.T) {
	lg := NewLabeledGauge("arity_g", "test", []string{"a", "b"})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong arity")
		}
	}()
	lg.Set(1.0, "only_one")
}

func TestReattack_LabelArityPanic_Histogram(t *testing.T) {
	lh := NewLabeledHistogram("arity_h", "test", []string{"a", "b"})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong arity")
		}
	}()
	lh.Observe(1.0, "only_one")
}

// ====================================================================
// RE-ATTACK: gauge float64 CAS race
// ====================================================================

func TestReattack_GaugeCASRace(t *testing.T) {
	g := NewGauge("race_cas", "test")
	var wg sync.WaitGroup
	const N = 100
	const ops = 1000
	for range N {
		wg.Go(func() {
			for range ops {
				g.Add(1.0)
				g.Sub(1.0)
			}
		})
	}
	wg.Wait()
	v := g.Get()
	if math.Abs(v) > 1.0 {
		t.Errorf("gauge CAS drift: %f (expected ~0)", v)
	}
}

// ====================================================================
// RE-ATTACK: name validation
// ====================================================================

func TestReattack_NameValidation(t *testing.T) {
	invalid := []string{"", "1abc", "a-b", "a.b", "a b"}
	for _, name := range invalid {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for name %q", name)
				}
			}()
			NewCounter(name, "test")
		}()
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for histogram name %q", name)
				}
			}()
			NewHistogram(name, "test")
		}()
	}
}

func TestReattack_NameValidation_Valid(t *testing.T) {
	valid := []string{"a", "_a", ":a", "abc_123", "A:B_c"}
	for _, name := range valid {
		// Should not panic
		NewHistogram(name, "test")
	}
}

// ====================================================================
// RE-ATTACK: content negotiation
// ====================================================================

func TestReattack_NegotiateHandler(t *testing.T) {
	r := NewRegistry("test")
	c := NewCounter("neg_test", "test")
	r.RegisterCounter(c)
	c.Inc()

	handler := r.NegotiateHandler()

	// No Accept → Prometheus
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("expected prometheus format, got %s", rec.Header().Get("Content-Type"))
	}

	// OpenMetrics Accept → OpenMetrics
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	if rec2.Header().Get("Content-Type") != OpenMetricsContentType {
		t.Errorf("expected OM format, got %s", rec2.Header().Get("Content-Type"))
	}
	if !strings.HasSuffix(rec2.Body.String(), "# EOF\n") {
		t.Error("OM response missing EOF")
	}
}

// ====================================================================
// RE-ATTACK: histogram exposition correctness post-refactor
// ====================================================================

func TestReattack_HistogramExposition_BothFormats(t *testing.T) {
	r := NewRegistry("test")
	h := NewHistogram("dur_seconds", "Duration", WithBuckets([]float64{0.1, 0.5, 1.0}))
	r.RegisterHistogram(h)
	h.Observe(0.05) // <= 0.1
	h.Observe(0.3)  // <= 0.5
	h.Observe(2.0)  // > 1.0

	// Prometheus format
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	prom := rec.Body.String()

	if !strings.Contains(prom, `dur_seconds_bucket{le="0.1"} 1`) {
		t.Errorf("prom le=0.1 wrong: %s", prom)
	}
	if !strings.Contains(prom, `dur_seconds_bucket{le="0.5"} 2`) {
		t.Errorf("prom le=0.5 wrong: %s", prom)
	}
	if !strings.Contains(prom, `dur_seconds_bucket{le="1"} 2`) {
		t.Errorf("prom le=1 wrong: %s", prom)
	}
	if !strings.Contains(prom, `dur_seconds_bucket{le="+Inf"} 3`) {
		t.Errorf("prom +Inf wrong: %s", prom)
	}
	if !strings.Contains(prom, "dur_seconds_count 3") {
		t.Errorf("prom count wrong: %s", prom)
	}

	// OpenMetrics format
	rec2 := httptest.NewRecorder()
	r.OpenMetricsHandler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	om := rec2.Body.String()

	if !strings.Contains(om, `dur_seconds_bucket{le="0.1"} 1`) {
		t.Errorf("om le=0.1 wrong: %s", om)
	}
	if !strings.Contains(om, `dur_seconds_bucket{le="+Inf"} 3`) {
		t.Errorf("om +Inf wrong: %s", om)
	}
	if !strings.Contains(om, "dur_seconds_count 3") {
		t.Errorf("om count wrong: %s", om)
	}
}
