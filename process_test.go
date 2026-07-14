package metrics

import (
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseProcStatCPU(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want float64
	}{
		{
			name: "comm with spaces and parens",
			// pid (comm with (nested) spaces) state ppid pgrp ... fields[11]=utime fields[12]=stime
			in:   "1234 (weird (proc) name) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0",
			want: 3.0, // (200+100)/100
		},
		{
			name: "simple comm",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 50 50 0 0",
			want: 1.0, // (50+50)/100
		},
		{
			name: "too few fields",
			in:   "1234 (cat) S 1 1 1 0 0",
			want: -1,
		},
		{
			name: "no closing paren",
			in:   "1234 cat S 1 1",
			want: -1,
		},
		{
			name: "non-numeric utime",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 abc 50 0 0",
			want: -1,
		},
		{
			name: "empty input",
			in:   "",
			want: -1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProcStatCPU([]byte(tc.in))
			if got != tc.want {
				t.Errorf("parseProcStatCPU(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseProcStatStartTime(t *testing.T) {
	// A well-formed /proc/self/stat line: field 22 (starttime) sits at index 19
	// of the after-comm slice. The lines below place 8000000 in that position,
	// among realistic noise for the surrounding fields.
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{
			name: "simple comm",
			//     pid  comm  st ppid pgrp sess tty tpgid flags min cmin maj cmaj ut st cut cst pri ni thr itreal START
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0 8000000",
			want: 8000000,
		},
		{
			name: "comm with spaces and parens",
			in:   "1234 (weird (proc) name) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0 42",
			want: 42,
		},
		{
			name: "too few fields",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 200 100",
			want: -1,
		},
		{
			name: "no closing paren",
			in:   "1234 cat S 1 1",
			want: -1,
		},
		{
			name: "trailing paren guarded",
			in:   "1234 (cat)",
			want: -1,
		},
		{
			name: "non-numeric starttime",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0 20 0 1 0 abc",
			want: -1,
		},
		{
			name: "empty input",
			in:   "",
			want: -1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProcStatStartTime([]byte(tc.in)); got != tc.want {
				t.Errorf("parseProcStatStartTime(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseProcStatBtime(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{
			name: "btime among noise",
			in:   "cpu  100 0 50 900 0 0 0\ncpu0 50 0 25 450\nintr 12345\nbtime 1700000000\nprocesses 4242\n",
			want: 1700000000,
		},
		{
			name: "btime first line",
			in:   "btime 1600000000\n",
			want: 1600000000,
		},
		{
			name: "no btime line",
			in:   "cpu  100 0 50 900\nprocesses 4242\n",
			want: -1,
		},
		{
			name: "malformed value",
			in:   "btime notanumber\n",
			want: -1,
		},
		{
			name: "empty input",
			in:   "",
			want: -1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProcStatBtime([]byte(tc.in)); got != tc.want {
				t.Errorf("parseProcStatBtime(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseProcStatusRSS(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{
			name: "VmRSS with kB suffix",
			in:   "Name:\tcat\nVmRSS:\t  1024 kB\nThreads:\t1\n",
			want: 1024 * 1024,
		},
		{
			name: "VmRSS without suffix",
			in:   "VmRSS:\t2048\n",
			want: 2048 * 1024,
		},
		{
			name: "no VmRSS line",
			in:   "Name:\tcat\nThreads:\t1\n",
			want: 0,
		},
		{
			name: "malformed value",
			in:   "VmRSS:\tnotanumber kB\n",
			want: 0,
		},
		{
			name: "empty input",
			in:   "",
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProcStatusRSS([]byte(tc.in))
			if got != tc.want {
				t.Errorf("parseProcStatusRSS(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestProcMetricsDegraded(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		cpuSeconds float64
		rss        int64
		openFDs    int
		maxFDs     int64
		want       bool
	}{
		{"linux all healthy", "linux", 1.5, 4096, 12, 1024, false},
		{"linux cpu read failed", "linux", -1, 4096, 12, 1024, true},
		// cpuSeconds == 0 and openFDs == 0 are HEALTHY readings: the failure
		// guards are strict (< 0), so a <= 0 mutation would wrongly degrade them.
		{"linux cpu zero healthy", "linux", 0, 4096, 12, 1024, false},
		{"linux rss zero", "linux", 1.5, 0, 12, 1024, true},
		{"linux rss negative", "linux", 1.5, -1, 12, 1024, true},
		{"linux fds read failed", "linux", 1.5, 4096, -1, 1024, true},
		{"linux fds zero healthy", "linux", 1.5, 4096, 0, 1024, false},
		// maxFDs: the unlimited sentinel is a successful reading (not degraded);
		// a non-positive maxFDs that is NOT the sentinel is a failed limits read.
		{"linux max fds unlimited healthy", "linux", 1.5, 4096, 12, unlimitedMaxFDs, false},
		{"linux max fds read failed", "linux", 1.5, 4096, 12, 0, true},
		{"linux all failed", "linux", -1, 0, -1, 0, true},
		{"non-linux all failed stays quiet", "darwin", -1, 0, -1, 0, false},
		{"non-linux healthy", "windows", 1.5, 4096, 12, 1024, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := procMetricsDegraded(tc.goos, tc.cpuSeconds, tc.rss, tc.openFDs, tc.maxFDs)
			if got != tc.want {
				t.Errorf("procMetricsDegraded(%q, %v, %d, %d, %d) = %v, want %v",
					tc.goos, tc.cpuSeconds, tc.rss, tc.openFDs, tc.maxFDs, got, tc.want)
			}
		})
	}
}

func TestOpenFDCount(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  int
	}{
		{"empty listing", nil, 0},
		{"only the dir handle", []string{"3"}, 0},
		{"three real fds plus handle", []string{"0", "1", "2", "3"}, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := openFDCount(tc.names); got != tc.want {
				t.Errorf("openFDCount(%v) = %d, want %d", tc.names, got, tc.want)
			}
		})
	}
}

func TestProcDegradedTransition(t *testing.T) {
	tests := []struct {
		name     string
		initial  bool
		degraded bool
		want     bool
		wantNext bool
	}{
		{"healthy->healthy (no edge)", false, false, false, false},
		{"healthy->degraded (edge)", false, true, true, true},
		{"degraded->degraded (no edge)", true, true, false, true},
		{"degraded->healthy (edge)", true, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s atomic.Bool
			s.Store(tt.initial)
			if got := procDegradedTransition(&s, tt.degraded); got != tt.want {
				t.Errorf("procDegradedTransition(%v, %v) = %v, want %v",
					tt.initial, tt.degraded, got, tt.want)
			}
			if got := s.Load(); got != tt.wantNext {
				t.Errorf("state after transition = %v, want %v", got, tt.wantNext)
			}
		})
	}
}

// TestProcPresencePredicates_Boundaries pins each series-presence predicate at
// the value where its `>`/`>=` comparison diverges: a zero CPU/openFDs reading
// is present, a zero RSS/maxFDs reading is absent.
func TestProcPresencePredicates_Boundaries(t *testing.T) {
	if !(&processMetricsData{cpuSeconds: 0}).hasCPU() {
		t.Error("hasCPU(cpuSeconds=0) = false, want true (>= 0 boundary)")
	}
	if (&processMetricsData{cpuSeconds: -1}).hasCPU() {
		t.Error("hasCPU(cpuSeconds=-1) = true, want false")
	}

	if (&processMetricsData{rss: 0}).hasRSS() {
		t.Error("hasRSS(rss=0) = true, want false (> 0 boundary)")
	}
	if !(&processMetricsData{rss: 1}).hasRSS() {
		t.Error("hasRSS(rss=1) = false, want true")
	}

	if !(&processMetricsData{openFDs: 0}).hasOpenFDs() {
		t.Error("hasOpenFDs(openFDs=0) = false, want true (>= 0 boundary)")
	}
	if (&processMetricsData{openFDs: -1}).hasOpenFDs() {
		t.Error("hasOpenFDs(openFDs=-1) = true, want false")
	}

	if (&processMetricsData{maxFDs: 0}).hasMaxFDs() {
		t.Error("hasMaxFDs(maxFDs=0) = true, want false (> 0 boundary)")
	}
	if !(&processMetricsData{maxFDs: 1}).hasMaxFDs() {
		t.Error("hasMaxFDs(maxFDs=1) = false, want true")
	}
	// The unlimited sentinel (-1) is a present, successful reading despite being
	// non-positive, so hasMaxFDs must accept it via the sentinel branch.
	if !(&processMetricsData{maxFDs: unlimitedMaxFDs}).hasMaxFDs() {
		t.Error("hasMaxFDs(maxFDs=unlimitedMaxFDs) = false, want true (sentinel branch)")
	}
}

// A single leading ')' makes strings.LastIndex return 0; the guard is `idx < 0`,
// so idx == 0 must still parse.
func TestParseProcStatCPU_LeadingParenIdxZero(t *testing.T) {
	in := []byte(") 0 0 0 0 0 0 0 0 0 0 0 200 100")
	if got := parseProcStatCPU(in); got != 3.0 {
		t.Errorf("parseProcStatCPU(%q) = %v, want 3.0 (idx == 0 must parse)", in, got)
	}
}

// A trailing ')' makes idx+2 exceed len(s); the bounds guard must return -1
// rather than slicing out of range.
func TestParseProcStatCPU_TrailingParenGuarded(t *testing.T) {
	in := []byte("1234 (cat)")
	if got := parseProcStatCPU(in); got != -1 {
		t.Errorf("parseProcStatCPU(%q) = %v, want -1 (trailing ')' guarded)", in, got)
	}
}

func TestLinuxUserHZ_Value(t *testing.T) {
	if linuxUserHZ != 100.0 {
		t.Errorf("linuxUserHZ = %v, want 100 (the near-universal Linux _SC_CLK_TCK)", linuxUserHZ)
	}
}

// TestParseProcStatCPU_DivisorIsLinuxUserHZ verifies parseProcStatCPU scales
// utime+stime by the linuxUserHZ constant: the result equals
// (utime+stime)/linuxUserHZ exactly.
func TestParseProcStatCPU_DivisorIsLinuxUserHZ(t *testing.T) {
	const utime, stime = 200, 100
	stat := []byte("1 (test proc) S 0 0 0 0 0 0 0 0 0 0 200 100")

	got := parseProcStatCPU(stat)
	want := float64(utime+stime) / linuxUserHZ
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("parseProcStatCPU = %v, want (utime+stime)/linuxUserHZ = %v", got, want)
	}
	if math.Abs(got-3.0) > 1e-12 {
		t.Errorf("parseProcStatCPU = %v, want 3.0 (300 ticks / 100)", got)
	}
}

// TestParseProcLimitsMaxFDs pins the "single field" boundary: with exactly one
// field after the "Max open files" label, the parser reports it (the guard is
// len(fields) >= 1, not > 1).
func TestParseProcLimitsMaxFDs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		// Real kernel shape: soft, hard, then the "files" unit. fields[0] is the
		// soft limit this parser reports.
		{"soft hard unit", "Max open files            1024                 4096                 files\n", 1024},
		{"single field", "Max open files 4096\n", 4096},
		{"label only", "Max open files\n", 0},
		{"absent", "Max locked memory         0                    0                    bytes\n", 0},
		// "unlimited" is a valid, successful reading, mapped to the sentinel.
		{"unlimited soft", "Max open files            unlimited            unlimited            files\n", unlimitedMaxFDs},
		{"unlimited mixed case", "Max open files Unlimited\n", unlimitedMaxFDs},
		// A malformed (non-numeric, non-"unlimited") value is a failed read.
		{"malformed value", "Max open files notanumber\n", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProcLimitsMaxFDs([]byte(tc.in)); got != tc.want {
				t.Errorf("parseProcLimitsMaxFDs(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestFormatMaxFDs pins the exposition rendering: the unlimited sentinel becomes
// float64(math.MaxUint64) ("1.8446744073709552e+19", what client_golang emits
// for an unlimited limit); every other value is decimal.
func TestFormatMaxFDs(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{"unlimited sentinel", unlimitedMaxFDs, "1.8446744073709552e+19"},
		{"finite limit", 1024, "1024"},
		{"zero", 0, "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatMaxFDs(tc.in); got != tc.want {
				t.Errorf("formatMaxFDs(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestReadProcFDs_readsAllEntries verifies readProcFDs reads every fd entry, not
// just one: a live process always has at least its standard descriptors plus the
// directory handle open, so the open count is >= 1.
func TestReadProcFDs_readsAllEntries(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("process fd metrics are Linux-only (/proc/self/fd)")
	}
	open, _ := readProcFDs()
	if open < 1 {
		t.Fatalf("readProcFDs() open = %d, want >= 1 (must read all fd entries)", open)
	}
}

func TestProcGCPauseSeconds_ScaledToSeconds(t *testing.T) {
	runtime.GC()
	runtime.GC() // guarantee PauseTotalNs > 0

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
	// Cumulative GC pause for a test process is far below a second; a value
	// scaled in nanoseconds (no /1e9) would be >= 1e9. Any threshold between
	// the two distinguishes the seconds-scaled value from an unscaled one.
	if val < 0 || val > 1e6 {
		t.Errorf("process_gc_pause_seconds_total = %v, want a small non-negative seconds value", val)
	}
}

func TestWriteProcessMetrics(t *testing.T) {
	var b strings.Builder
	WriteProcessMetrics(&b)
	out := b.String()
	for _, want := range []string{
		"go_goroutines",
		"go_memstats_heap_alloc_bytes",
		"process_gc_pause_seconds_total",
		"process_uptime_seconds",
		"process_start_time_seconds",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteProcessMetrics missing %q", want)
		}
	}
}

func TestWriteProcessMetrics_uptimeAndStartTimeReconcile(t *testing.T) {
	// process_uptime_seconds is derived as now - process_start_time_seconds (the
	// start time coming from the kernel on Linux, or the package-init fallback
	// otherwise), so start + uptime must reconcile with now regardless of source.
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

// TestWriteProcessMetrics_LinuxFDsEmitted pins the Linux-only fd emit wiring in
// processFamilies: when /proc/self/fd and /proc/self/limits are readable,
// WriteProcessMetrics emits process_open_fds and process_max_fds, the latter
// rendered through formatMaxFDs (a positive integer, or the
// float64(math.MaxUint64) sentinel for an unlimited soft limit). The
// always-present-metric assertions in TestWriteProcessMetrics
// never check these two series, so the hasOpenFDs/hasMaxFDs emit composition and
// the formatMaxFDs call site are otherwise unexercised end-to-end.
func TestWriteProcessMetrics_LinuxFDsEmitted(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("process fd metrics are Linux-only (/proc/self/fd, /proc/self/limits)")
	}
	var b strings.Builder
	WriteProcessMetrics(&b)

	var openVal, maxVal string
	var gotOpen, gotMax bool
	for line := range strings.SplitSeq(b.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "process_open_fds "); ok {
			openVal, gotOpen = v, true
		}
		if v, ok := strings.CutPrefix(line, "process_max_fds "); ok {
			maxVal, gotMax = v, true
		}
	}
	if !gotOpen {
		t.Fatal("missing process_open_fds line")
	}
	if n, err := strconv.Atoi(openVal); err != nil || n < 0 {
		t.Errorf("process_open_fds = %q, want a non-negative integer", openVal)
	}
	if !gotMax {
		t.Fatal("missing process_max_fds line")
	}
	// process_max_fds is either a positive integer (a finite limit) or the
	// unlimited sentinel rendered as float64(math.MaxUint64)
	// ("1.8446744073709552e+19"), so parse it as a float rather than an int.
	if n, err := strconv.ParseFloat(maxVal, 64); err != nil || n <= 0 {
		t.Errorf("process_max_fds = %q, want a positive number", maxVal)
	}
}
