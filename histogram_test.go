package metrics

import (
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewHistogram_defaultBuckets(t *testing.T) {
	h := NewHistogram("default_hist", "test")
	if !slices.Equal(h.bounds, DefaultBuckets) {
		t.Errorf("NewHistogram no-opts bounds = %v, want DefaultBuckets %v", h.bounds, DefaultBuckets)
	}
}

func TestNewLabeledHistogram_defaultBuckets(t *testing.T) {
	lh := NewLabeledHistogram("default_lh", "test", []string{"k"})
	if !slices.Equal(lh.bounds, DefaultBuckets) {
		t.Errorf("NewLabeledHistogram no-opts bounds = %v, want DefaultBuckets %v", lh.bounds, DefaultBuckets)
	}
}

func TestNewHistogram_withBucketsOverrides(t *testing.T) {
	h := NewHistogram("custom_hist", "test", WithBuckets([]float64{1, 2, 3}))
	if !slices.Equal(h.bounds, []float64{1, 2, 3}) {
		t.Errorf("bounds = %v, want [1 2 3]", h.bounds)
	}
}

// TestNewHistogram_invalidBuckets_panic verifies the bucket contract is enforced
// at construction: bounds must be strictly increasing finite values. Each case
// pins a violation that would otherwise emit duplicate or non-monotonic le
// series that parsers reject.
func TestNewHistogram_invalidBuckets_panic(t *testing.T) {
	tests := []struct {
		name   string
		bounds []float64
		want   string
	}{
		{"duplicates", []float64{1, 1, 5, 5}, "strictly increasing"},
		{"unsorted", []float64{2, 1}, "strictly increasing"},
		{"nan", []float64{math.NaN(), 0.5, 1}, "finite"},
		{"pos_inf", []float64{0.5, math.Inf(1)}, "finite"},
		{"neg_inf", []float64{math.Inf(-1), 0, 1}, "finite"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mustPanicContaining(t, tc.want, func() {
				NewHistogram("inv_"+tc.name, "test", WithBuckets(tc.bounds))
			})
		})
	}
}

// TestNewHistogram_nilOptions verifies a nil Option is skipped safely: nils
// alone leave DefaultBuckets in place, and a nil between real options does not
// disturb last-wins ordering.
func TestNewHistogram_nilOptions(t *testing.T) {
	t.Run("single nil uses defaults", func(t *testing.T) {
		h := NewHistogram("nil_h", "test", nil)
		h.Observe(0.5)
		if h.count.Load() != 1 {
			t.Errorf("count = %d, want 1", h.count.Load())
		}
		if len(h.bounds) != len(DefaultBuckets) {
			t.Errorf("bounds len = %d, want %d", len(h.bounds), len(DefaultBuckets))
		}
	})
	t.Run("multiple nils use defaults", func(t *testing.T) {
		h := NewHistogram("multi_nil_h", "test", nil, nil, nil)
		if len(h.bounds) != len(DefaultBuckets) {
			t.Errorf("bounds len = %d, want %d", len(h.bounds), len(DefaultBuckets))
		}
	})
	t.Run("nil between real options, last wins", func(t *testing.T) {
		h := NewHistogram("nil_between_h", "test",
			WithBuckets([]float64{1, 2}),
			nil,
			WithBuckets([]float64{10, 20, 30}),
		)
		if !slices.Equal(h.bounds, []float64{10, 20, 30}) {
			t.Errorf("bounds = %v, want [10 20 30]", h.bounds)
		}
	})
}

func TestNewLabeledHistogram_nilOption(t *testing.T) {
	lh := NewLabeledHistogram("nil_lh", "test", []string{"k"}, nil)
	lh.Observe(0.1, "v")
	if lh.vals[labelKey{"v"}].count.Load() != 1 {
		t.Error("observe failed after nil option")
	}
}

// TestWithBuckets_doesNotMutateInput verifies NewHistogram clones the bound
// slice, so the caller's slice is never modified.
func TestWithBuckets_doesNotMutateInput(t *testing.T) {
	input := []float64{1, 3, 5}
	original := slices.Clone(input)
	NewHistogram("nomutate_hist", "test", WithBuckets(input))
	if !slices.Equal(input, original) {
		t.Errorf("WithBuckets mutated input: got %v, want %v", input, original)
	}
}

func TestWithBuckets_lastWins(t *testing.T) {
	h := NewHistogram("multi_buckets_hist", "test",
		WithBuckets([]float64{1, 2, 3}),
		WithBuckets([]float64{10, 20}),
	)
	if !slices.Equal(h.bounds, []float64{10, 20}) {
		t.Errorf("expected last WithBuckets to win, got %v", h.bounds)
	}
}

// TestNewHistogram_emptyAndNilBuckets covers the degenerate bound sets: an empty
// or nil slice yields a histogram with only the implicit +Inf bucket.
func TestNewHistogram_emptyAndNilBuckets(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		h := NewHistogram("empty_buckets", "test", WithBuckets([]float64{}))
		h.Observe(1.0)
		h.Observe(5.0)

		var b strings.Builder
		WriteHistogram(&b, h)
		if out := b.String(); !strings.Contains(out, `empty_buckets_bucket{le="+Inf"} 2`) {
			t.Errorf("empty buckets wrong: %s", out)
		}
		if h.count.Load() != 2 {
			t.Errorf("count = %d, want 2", h.count.Load())
		}
	})
	t.Run("nil slice", func(t *testing.T) {
		h := NewHistogram("nil_slice_buckets", "test", WithBuckets(nil))
		if len(h.bounds) != 0 {
			t.Errorf("nil slice bounds = %d, want 0", len(h.bounds))
		}
		h.Observe(1.0)
		if h.count.Load() != 1 {
			t.Errorf("count = %d, want 1", h.count.Load())
		}
	})
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

// TestHistogram_zeroBucketBoundary pins le="0" cumulative counting: an
// observation exactly at the zero bound (and a negative one) count as <= 0.
func TestHistogram_zeroBucketBoundary(t *testing.T) {
	h := NewHistogram("zero_bucket_hist", "test", WithBuckets([]float64{0}))
	h.Observe(0)    // exactly at boundary
	h.Observe(-0.1) // below zero
	h.Observe(0.1)  // above
	if h.count.Load() != 3 {
		t.Errorf("count = %d, want 3", h.count.Load())
	}
	// le="0" contains obs <= 0: that's 0 and -0.1 = 2.
	if h.buckets[0].Load() != 2 {
		t.Errorf("le=0 bucket = %d, want 2", h.buckets[0].Load())
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

func TestHistogram_NaNObserve(t *testing.T) {
	h := NewHistogram("nan_obs", "test", WithBuckets([]float64{0.1, 1}))
	h.Observe(math.NaN())
	// NaN should not panic, count should increment.
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
	// +Inf observation lands only in the +Inf bucket; -Inf is <= every bound so
	// it lands everywhere including +Inf. The +Inf bucket therefore counts both.
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

func TestHistogramSnapshot(t *testing.T) {
	h := NewHistogram("snap_test", "snapshot",
		WithBuckets([]float64{0.1, 0.5, 1}))
	h.Observe(0.05) // lands in <=0.1, <=0.5, <=1, +Inf
	h.Observe(0.3)  // lands in <=0.5, <=1, +Inf
	h.Observe(2.0)  // lands in +Inf only
	sum, count, buckets := h.snapshot()
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

func TestWriteHistogram_SumRendering(t *testing.T) {
	t.Run("tiny sum keeps precision", func(t *testing.T) {
		h := NewHistogram("hist_sum_tiny", "test")
		h.Observe(1e-7) // a fixed-precision %.6f would floor this sum to zero
		var b strings.Builder
		WriteHistogram(&b, h)
		if got := b.String(); !strings.Contains(got, "hist_sum_tiny_sum 1e-07\n") {
			t.Errorf("WriteHistogram sum = %q, want line %q", got, "hist_sum_tiny_sum 1e-07")
		}
	})
	t.Run("whole sum renders as bare integer", func(t *testing.T) {
		h := NewHistogram("hist_sum_whole", "test")
		h.Observe(2)
		h.Observe(2) // sum = 4
		var b strings.Builder
		WriteHistogram(&b, h)
		if got := b.String(); !strings.Contains(got, "hist_sum_whole_sum 4\n") {
			t.Errorf("WriteHistogram sum = %q, want line %q", got, "hist_sum_whole_sum 4")
		}
	})
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

func TestLabeledHistogram_ObserveArityPanic(t *testing.T) {
	lh := NewLabeledHistogram("arity_h", "test", []string{"a", "b"})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for wrong arity")
		}
	}()
	lh.Observe(1.0, "only_one")
}

func TestNewLabeledHistogram_TooManyLabelsPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for >4 labels")
		}
	}()
	NewLabeledHistogram("lh_many", "test", []string{"a", "b", "c", "d", "e"})
}

// TestNewLabeledHistogram_ExactlyFourLabelsAllowed pins the arity guard at its
// inclusive maximum: four labels is the legal maximum (the guard is > 4).
func TestNewLabeledHistogram_ExactlyFourLabelsAllowed(t *testing.T) {
	lh := NewLabeledHistogram("mk_lh4", "test", []string{"a", "b", "c", "d"})
	lh.Observe(0.5, "1", "2", "3", "4") // must not panic with four labels

	var b strings.Builder
	WriteLabeledHistogram(&b, lh)
	if out := b.String(); !strings.Contains(out, `a="1",b="2",c="3",d="4"`) {
		t.Errorf("four-label histogram not exposed correctly:\n%s", out)
	}
}

// TestLabeledHistogram_Concurrent asserts every concurrent Observe across
// several label combinations is counted, exercising the per-key lazy histogram
// creation under contention.
func TestLabeledHistogram_Concurrent(t *testing.T) {
	lh := NewLabeledHistogram("conc_lh", "test", []string{"method", "status"},
		WithBuckets([]float64{0.01, 0.05, 0.1, 0.5, 1.0}))

	var wg sync.WaitGroup
	labels := [][2]string{
		{"GET", "200"}, {"POST", "201"}, {"GET", "404"}, {"DELETE", "500"},
	}
	const N = 50
	const ops = 200
	for _, l := range labels {
		for range N {
			wg.Go(func() {
				for range ops {
					lh.Observe(0.03, l[0], l[1])
				}
			})
		}
	}
	wg.Wait()

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

// TestHistogram_nameEndingInCount verifies a histogram whose base name already
// ends in _count still emits its derived _bucket/_sum/_count series correctly
// (the derived suffixes append to the base name without collapsing).
func TestHistogram_nameEndingInCount(t *testing.T) {
	h := NewHistogram("ops_count", "operations", WithBuckets([]float64{1, 5, 10}))
	h.Observe(3)
	var b strings.Builder
	WriteHistogram(&b, h)
	out := b.String()
	for _, want := range []string{
		"# TYPE ops_count histogram",
		"ops_count_bucket",
		"ops_count_sum",
		"ops_count_count 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("histogram named _count missing %q:\n%s", want, out)
		}
	}
}
