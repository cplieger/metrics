package metrics

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// This file targets process.go mutants that survived the broader suite. Every
// predicate here is an unexported pure function or method, tested directly at
// the exact boundary the mutation would flip.

// --- process.go series-presence predicates (boundaries). ---
// hasCPU: cpuSeconds >= 0  (0 is present)
// hasRSS: rss > 0          (0 is absent)
// hasOpenFDs: openFDs >= 0 (0 is present)
// hasMaxFDs: maxFDs > 0    (0 is absent)
// Each pair pins the value where `>`/`>=` diverge.
func TestProcPresencePredicates_Boundaries(t *testing.T) {
	// hasCPU `>= 0`: zero CPU time is a valid reading, so it is present.
	if !(&processMetricsData{cpuSeconds: 0}).hasCPU() {
		t.Error("hasCPU(cpuSeconds=0) = false, want true (>= 0 boundary)")
	}
	if (&processMetricsData{cpuSeconds: -1}).hasCPU() {
		t.Error("hasCPU(cpuSeconds=-1) = true, want false")
	}

	// hasRSS `> 0`: zero RSS signals a failed read, so it is absent.
	if (&processMetricsData{rss: 0}).hasRSS() {
		t.Error("hasRSS(rss=0) = true, want false (> 0 boundary)")
	}
	if !(&processMetricsData{rss: 1}).hasRSS() {
		t.Error("hasRSS(rss=1) = false, want true")
	}

	// hasOpenFDs `>= 0`: zero open fds is a valid reading, so it is present.
	if !(&processMetricsData{openFDs: 0}).hasOpenFDs() {
		t.Error("hasOpenFDs(openFDs=0) = false, want true (>= 0 boundary)")
	}
	if (&processMetricsData{openFDs: -1}).hasOpenFDs() {
		t.Error("hasOpenFDs(openFDs=-1) = true, want false")
	}

	// hasMaxFDs `> 0`: zero max fds signals an unread limit, so it is absent.
	if (&processMetricsData{maxFDs: 0}).hasMaxFDs() {
		t.Error("hasMaxFDs(maxFDs=0) = true, want false (> 0 boundary)")
	}
	if !(&processMetricsData{maxFDs: 1}).hasMaxFDs() {
		t.Error("hasMaxFDs(maxFDs=1) = false, want true")
	}
}

// --- process.go procMetricsDegraded: `cpuSeconds < 0` and `openFDs < 0`
// failure boundaries. ---
// The existing table covers rss <= 0 at zero, but not the strict `< 0` edges
// for cpuSeconds and openFDs. cpuSeconds == 0 and openFDs == 0 are HEALTHY
// readings: a `<= 0` mutation would wrongly mark them degraded.
func TestProcMetricsDegraded_FailureBoundaries(t *testing.T) {
	// cpuSeconds == 0 with healthy rss/fds: not degraded (cpu < 0 boundary).
	if procMetricsDegraded("linux", 0, 4096, 12) {
		t.Error("procMetricsDegraded(linux, cpu=0, ...) = true, want false (cpuSeconds < 0 boundary)")
	}
	// openFDs == 0 with healthy cpu/rss: not degraded (openFDs < 0 boundary).
	if procMetricsDegraded("linux", 1.5, 4096, 0) {
		t.Error("procMetricsDegraded(linux, ..., openFDs=0) = true, want false (openFDs < 0 boundary)")
	}
}

// --- process.go parseProcStatCPU: `idx < 0` boundary and `idx+2` arithmetic. ---

// A single leading ')' makes strings.LastIndex return 0. The guard is `idx < 0`,
// so idx == 0 must proceed to parse (an `idx <= 0` mutation would reject it).
func TestParseProcStatCPU_LeadingParenIdxZero(t *testing.T) {
	// ") <11 fillers> 200 100": LastIndex(')') == 0, then 13 fields follow.
	in := []byte(") 0 0 0 0 0 0 0 0 0 0 0 200 100")
	if got := parseProcStatCPU(in); got != 3.0 {
		t.Errorf("parseProcStatCPU(%q) = %v, want 3.0 (idx == 0 must parse)", in, got)
	}
}

// A trailing ')' makes idx+2 exceed len(s); the `idx+2 >= len(s)` guard returns
// -1 safely. An `idx-2` arithmetic mutation defeats the guard and would slice
// out of range (panic), so the safe -1 result kills it.
func TestParseProcStatCPU_TrailingParenGuarded(t *testing.T) {
	in := []byte("1234 (cat)")
	if got := parseProcStatCPU(in); got != -1 {
		t.Errorf("parseProcStatCPU(%q) = %v, want -1 (trailing ')' guarded)", in, got)
	}
}

// --- process.go collectProcessMetrics: gcPause = PauseTotalNs / 1e9 (arithmetic). ---
// The division converts nanoseconds to seconds. A `*` mutation would multiply
// by 1e9 instead, producing a value >= 1e9 once any GC pause has been recorded.
// A short-lived test process accrues only a tiny fraction of a second of total
// GC pause, so bounding the exposed value kills the multiply mutant.
func TestProcGCPauseSeconds_ScaledToSeconds(t *testing.T) {
	runtime.GC()
	runtime.GC() // guarantee PauseTotalNs > 0 so the multiply mutant diverges

	var b strings.Builder
	WriteProcessMetrics(&b)

	var val float64
	var found bool
	for line := range strings.SplitSeq(b.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "process_gc_pause_seconds_total "); ok {
			parsed, err := strconv.ParseFloat(v, 64)
			if err != nil {
				t.Fatalf("process_gc_pause_seconds_total value %q not a float: %v", v, err)
			}
			val, found = parsed, true
		}
	}
	if !found {
		t.Fatal("missing process_gc_pause_seconds_total line")
	}
	// Real cumulative GC pause for a test process is far below a second; the
	// `* 1e9` mutant yields >= 1e9. Any threshold between the two kills it.
	if val < 0 || val > 1e6 {
		t.Errorf("process_gc_pause_seconds_total = %v, want a small non-negative seconds value (PauseTotalNs / 1e9)", val)
	}
}

func FuzzParseProcStatusRSS_ValueAmongNoise(f *testing.F) {
	f.Add(uint16(1024), "Threads:\t1\n")
	f.Add(uint16(0), "")
	f.Add(uint16(65535), "VmHWM:\t100 kB\nName:\tcat\n")
	f.Fuzz(func(t *testing.T, kb uint16, noise string) {
		// parseProcStatusRSS returns the first "VmRSS:" line's value * 1024.
		// Placing a known VmRSS line first means arbitrary trailing status noise
		// must not change the parsed bytes. kb is bounded to uint16 so kB*1024
		// cannot overflow int64.
		data := []byte("VmRSS:\t" + strconv.FormatUint(uint64(kb), 10) + " kB\n" + noise)
		want := int64(kb) * 1024
		if got := parseProcStatusRSS(data); got != want {
			t.Errorf("parseProcStatusRSS kb=%d noise=%q = %d, want %d", kb, noise, got, want)
		}
	})
}
