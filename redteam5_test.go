package metrics

import (
	"math"
	"strings"
	"sync"
	"testing"
)

// ====================================================================
// ROUND 2 RED-TEAM: Post-refactor verification
// ====================================================================

// --- Round-1 fix verification: nil Option safety ---

func TestR2_NilOption_NewHistogram(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil option panicked NewHistogram: %v", r)
		}
	}()
	h := NewHistogram("r2_nil_h", "test", nil)
	h.Observe(0.5)
	if h.count.Load() != 1 {
		t.Errorf("count = %d, want 1", h.count.Load())
	}
}

func TestR2_NilOption_NewLabeledHistogram(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil option panicked NewLabeledHistogram: %v", r)
		}
	}()
	lh := NewLabeledHistogram("r2_nil_lh", "test", []string{"k"}, nil)
	lh.Observe(0.1, "v")
	if lh.vals[[4]string{"v"}].count.Load() != 1 {
		t.Error("observe failed after nil option")
	}
}

func TestR2_MultipleNilOptions(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("multiple nil options panicked: %v", r)
		}
	}()
	h := NewHistogram("r2_multi_nil", "test", nil, nil, nil)
	// Should use DefaultBuckets
	if len(h.bounds) != len(DefaultBuckets) {
		t.Errorf("bounds len = %d, want %d", len(h.bounds), len(DefaultBuckets))
	}
}

func TestR2_NilBetweenReal(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil between real options panicked: %v", r)
		}
	}()
	h := NewHistogram("r2_nil_between", "test",
		WithBuckets([]float64{1, 2}),
		nil,
		WithBuckets([]float64{10, 20, 30}),
	)
	// Last WithBuckets wins
	if len(h.bounds) != 3 {
		t.Errorf("bounds len = %d, want 3", len(h.bounds))
	}
}

// --- WithBuckets edge values ---

func TestR2_WithBuckets_Zero(t *testing.T) {
	h := NewHistogram("r2_zero_bucket", "test", WithBuckets([]float64{0}))
	h.Observe(0)    // exactly at boundary
	h.Observe(-0.1) // below zero
	h.Observe(0.1)  // above
	if h.count.Load() != 3 {
		t.Errorf("count = %d, want 3", h.count.Load())
	}
	// le="0" should contain obs <= 0: that's 0 and -0.1 = 2
	if h.buckets[0].Load() != 2 {
		t.Errorf("le=0 bucket = %d, want 2", h.buckets[0].Load())
	}
}

func TestR2_WithBuckets_Negative(t *testing.T) {
	h := NewHistogram("r2_neg_bucket", "test", WithBuckets([]float64{-1, 0, 1}))
	h.Observe(-2)  // below all
	h.Observe(-1)  // exactly at -1
	h.Observe(0.5) // between 0 and 1
	h.Observe(2)   // above all

	// Cumulative: le=-1 should have obs ≤ -1: -2 and -1 = 2
	if h.buckets[0].Load() != 2 {
		t.Errorf("le=-1 = %d, want 2", h.buckets[0].Load())
	}
	// le=0: obs ≤ 0: -2, -1 = 2 (no more since 0.5 > 0)
	if h.buckets[1].Load() != 2 {
		t.Errorf("le=0 = %d, want 2", h.buckets[1].Load())
	}
	// le=1: obs ≤ 1: -2, -1, 0.5 = 3
	if h.buckets[2].Load() != 3 {
		t.Errorf("le=1 = %d, want 3", h.buckets[2].Load())
	}
	// +Inf = 4
	if h.buckets[3].Load() != 4 {
		t.Errorf("+Inf = %d, want 4", h.buckets[3].Load())
	}
}

func TestR2_WithBuckets_NegInf(t *testing.T) {
	mustPanicContaining(t, "finite", func() {
		NewHistogram("r2_neginf", "test", WithBuckets([]float64{math.Inf(-1), 0, 1}))
	})
}

func TestR2_WithBuckets_VerySmall(t *testing.T) {
	h := NewHistogram("r2_small", "test", WithBuckets([]float64{1e-300, 1e-100, 1}))
	h.Observe(5e-301) // below 1e-300
	h.Observe(1e-300) // exactly at boundary
	h.Observe(0.5)    // between 1e-100 and 1
	if h.count.Load() != 3 {
		t.Errorf("count = %d, want 3", h.count.Load())
	}
}

// --- Default parity ---

func TestR2_DefaultParity_NoOpts(t *testing.T) {
	h := NewHistogram("r2_parity_none", "test")
	hWithNil := NewHistogram("r2_parity_nil", "test", nil)
	hWithEmpty := NewHistogram("r2_parity_empty", "test") // no options

	if len(h.bounds) != len(DefaultBuckets) {
		t.Errorf("no-opts bounds len = %d", len(h.bounds))
	}
	if len(hWithNil.bounds) != len(DefaultBuckets) {
		t.Errorf("nil-opt bounds len = %d", len(hWithNil.bounds))
	}
	if len(hWithEmpty.bounds) != len(DefaultBuckets) {
		t.Errorf("empty-opts bounds len = %d", len(hWithEmpty.bounds))
	}
}

// --- Labeled histogram -race ---

func TestR2_LabeledHistogram_Race(t *testing.T) {
	lh := NewLabeledHistogram("r2_race_lh", "test", []string{"method", "status"},
		WithBuckets([]float64{0.01, 0.05, 0.1, 0.5, 1.0}))

	var wg sync.WaitGroup
	labels := [][2]string{
		{"GET", "200"}, {"POST", "201"}, {"GET", "404"}, {"DELETE", "500"},
	}
	const N = 50
	const ops = 200
	for _, l := range labels {
		for range N {
			l := l
			wg.Go(func() {
				for range ops {
					lh.Observe(0.03, l[0], l[1])
				}
			})
		}
	}
	wg.Wait()

	// Verify total count
	totalCount := int64(0)
	lh.mu.RLock()
	for _, h := range lh.vals {
		totalCount += h.count.Load()
	}
	lh.mu.RUnlock()
	expected := int64(len(labels) * N * ops)
	if totalCount != expected {
		t.Errorf("total count = %d, want %d", totalCount, expected)
	}
}

func TestR2_LabeledHistogram_ConcurrentExposition(t *testing.T) {
	lh := NewLabeledHistogram("r2_expo_race", "test", []string{"k"},
		WithBuckets([]float64{0.1, 0.5}))

	var wg sync.WaitGroup
	// Writers
	for range 10 {
		wg.Go(func() {
			for i := range 100 {
				lh.Observe(float64(i)*0.01, "a")
			}
		})
	}
	// Readers (exposition)
	for range 5 {
		wg.Go(func() {
			var b strings.Builder
			for range 50 {
				b.Reset()
				WriteLabeledHistogram(&b, lh)
			}
		})
	}
	wg.Wait()
}

// --- Exposition conformance ---

func TestR2_HistogramExposition_EmptyBuckets(t *testing.T) {
	h := NewHistogram("r2_expo_empty", "test", WithBuckets([]float64{}))
	h.Observe(1.0)

	var b strings.Builder
	WriteHistogram(&b, h)
	out := b.String()

	// With empty buckets, only +Inf should appear
	if !strings.Contains(out, `r2_expo_empty_bucket{le="+Inf"} 1`) {
		t.Errorf("empty-bucket exposition wrong: %s", out)
	}
	if !strings.Contains(out, "r2_expo_empty_count 1") {
		t.Errorf("count missing: %s", out)
	}
}

func TestR2_HistogramExposition_NaNBound(t *testing.T) {
	mustPanicContaining(t, "finite", func() {
		NewHistogram("r2_nan_expo", "test", WithBuckets([]float64{math.NaN(), 1.0}))
	})
}

func TestR2_WithBuckets_NilSlice(t *testing.T) {
	// WithBuckets(nil) — nil slice passed to the option
	h := NewHistogram("r2_nil_slice", "test", WithBuckets(nil))
	// nil slice: len(nil) == 0, so bounds is empty
	if len(h.bounds) != 0 {
		t.Errorf("nil slice bounds = %d, want 0", len(h.bounds))
	}
	h.Observe(1.0)
	if h.count.Load() != 1 {
		t.Errorf("count = %d", h.count.Load())
	}
}

// --- Option independence (WithBuckets does not mutate across calls) ---

func TestR2_OptionIndependence(t *testing.T) {
	opt := WithBuckets([]float64{1, 3, 5})
	h1 := NewHistogram("r2_indep1", "test", opt)
	h2 := NewHistogram("r2_indep2", "test", opt)

	// Both should have [1, 3, 5]
	if len(h1.bounds) != 3 || len(h2.bounds) != 3 {
		t.Fatal("bounds length mismatch")
	}
	if h1.bounds[0] != 1 || h1.bounds[1] != 3 || h1.bounds[2] != 5 {
		t.Errorf("h1 bounds = %v", h1.bounds)
	}
	if h2.bounds[0] != 1 || h2.bounds[1] != 3 || h2.bounds[2] != 5 {
		t.Errorf("h2 bounds = %v", h2.bounds)
	}
}

func TestR2_SharedOption_ConcurrentUse(t *testing.T) {
	opt := WithBuckets([]float64{1, 2, 3, 4, 5})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			h := NewHistogram("r2_shared_"+string(rune('a'+i)), "test", opt)
			h.Observe(0.5)
		})
	}
	wg.Wait()
}
