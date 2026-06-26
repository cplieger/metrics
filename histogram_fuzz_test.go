package metrics

import (
	"math"
	"testing"
)

// FuzzHistogram_BucketPlacementInvariant checks the cumulative-bucket invariant
// for arbitrary strictly-increasing finite bounds and an arbitrary observation:
// bucket[i] is 1 iff obs <= bounds[i], and the +Inf bucket always counts it.
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

// FuzzHistogramObserve checks observe-side invariants for any value: the count
// increments by one, a finite input never produces a NaN sum, and the
// cumulative bucket counts stay monotonically non-decreasing.
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
