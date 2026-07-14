package metrics

import (
	"log/slog"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Timer measures elapsed time and reports to a Histogram.
type Timer struct {
	start   time.Time
	observe func(float64)
}

// NewTimer starts a timer that will observe into the given histogram.
func NewTimer(h *Histogram) *Timer {
	return &Timer{start: time.Now(), observe: h.Observe}
}

// ObserveDuration records the elapsed time since the timer was created.
func (t *Timer) ObserveDuration() time.Duration {
	d := time.Since(t.start)
	t.observe(d.Seconds())
	return d
}

// processStartTime is captured at package init as the fallback anchor for
// process_start_time_seconds (and, via it, process_uptime_seconds) when the
// kernel-derived start time cannot be read. On Linux the true start time comes
// from /proc (btime + starttime/linuxUserHZ, see readProcStartTime); on other
// platforms or on any /proc parse failure this package-init instant is used
// instead. Uptime is always reconciled as now - start so the documented
// "start + uptime == now" invariant holds regardless of which source wins.
var processStartTime = time.Now()

// goosLinux is the runtime.GOOS value for Linux, the only platform where
// /proc-based process metrics are expected to succeed.
const goosLinux = "linux"

// linuxUserHZ is the assumed value of sysconf(_SC_CLK_TCK), the unit in which
// the Linux kernel reports per-process CPU time in /proc/self/stat (utime and
// stime are counted in "clock ticks" of 1/USER_HZ seconds). It is 100 on the
// overwhelming majority of Linux kernels (the CONFIG_HZ=100 default), so
// process_cpu_seconds_total is computed as (utime+stime)/linuxUserHZ.
//
// This is deliberately a hardcoded constant rather than a runtime lookup:
// reading the real sysconf(_SC_CLK_TCK) requires either cgo or
// golang.org/x/sys/unix, and BOTH would violate this library's core "zero
// runtime dependencies, standard library only" contract (go.mod has no
// require). There is no pure-stdlib way to read _SC_CLK_TCK. The only
// observable consequence of the assumption is that process_cpu_seconds_total is
// scaled incorrectly on the rare kernel built with a non-100 CONFIG_HZ (e.g.
// 250 or 1000); every other metric is unaffected.
const linuxUserHZ = 100.0

// unlimitedMaxFDs is the sentinel stored in processMetricsData.maxFDs when
// /proc/self/limits reports the soft "Max open files" limit as the literal
// token "unlimited". It is distinct from 0 (the absent/read-failure value) so
// an unlimited limit is treated as a successful reading and exposed as +Inf
// rather than silently dropped.
const unlimitedMaxFDs int64 = -1

var procDegraded atomic.Bool

// processMetricsData holds collected process metrics for shared use.
type processMetricsData struct {
	goroutines int
	heapAlloc  uint64
	gcPause    float64
	startTime  float64
	uptime     float64
	cpuSeconds float64
	rss        int64
	openFDs    int
	maxFDs     int64
}

// Series-presence predicates used by processFamilies when materialising the
// process-metric IR. Gating them in one place keeps both exposition formats
// (encodePrometheus / encodeOpenMetrics) emitting the same optional series set.
func (d *processMetricsData) hasCPU() bool     { return d.cpuSeconds >= 0 }
func (d *processMetricsData) hasRSS() bool     { return d.rss > 0 }
func (d *processMetricsData) hasOpenFDs() bool { return d.openFDs >= 0 }
func (d *processMetricsData) hasMaxFDs() bool  { return d.maxFDs > 0 || d.maxFDs == unlimitedMaxFDs }

// procMetricsDegraded reports whether process metric collection partially
// failed on a platform where /proc-based metrics are expected (Linux).
// cpuSeconds < 0, rss <= 0, openFDs < 0, or a non-positive maxFDs (excluding
// the unlimitedMaxFDs sentinel, which is a successful "unlimited" reading) each
// signal a failed read.
func procMetricsDegraded(goos string, cpuSeconds float64, rss int64, openFDs int, maxFDs int64) bool {
	return goos == goosLinux && (cpuSeconds < 0 || rss <= 0 || openFDs < 0 ||
		(maxFDs <= 0 && maxFDs != unlimitedMaxFDs))
}

// procDegradedTransition stores the new degraded state in s and reports
// whether this call changed it. Callers log only when it returns true.
func procDegradedTransition(s *atomic.Bool, degraded bool) bool {
	if degraded {
		return s.CompareAndSwap(false, true)
	}
	return s.CompareAndSwap(true, false)
}

// collectProcessMetrics gathers all process metrics into a struct.
func collectProcessMetrics(d *processMetricsData) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	d.goroutines = runtime.NumGoroutine()
	d.heapAlloc = m.HeapAlloc
	d.gcPause = float64(m.PauseTotalNs) / 1e9
	start := resolvedProcStartTime()
	if start < 0 {
		start = float64(processStartTime.Unix())
	}
	now := float64(time.Now().UnixNano()) / 1e9
	d.startTime = start
	d.uptime = now - start
	d.cpuSeconds = readProcCPUSeconds()
	d.rss = readProcRSS()
	d.openFDs, d.maxFDs = readProcFDs()
	degraded := procMetricsDegraded(runtime.GOOS, d.cpuSeconds, d.rss, d.openFDs, d.maxFDs)
	if procDegradedTransition(&procDegraded, degraded) {
		if degraded {
			slog.Warn("process metric collection partially failed; some process_* metrics will be omitted",
				"cpu_ok", d.cpuSeconds >= 0, "rss_ok", d.rss > 0, "fds_ok", d.openFDs >= 0,
				"max_fds_ok", d.hasMaxFDs())
		} else {
			slog.Info("process metric collection recovered; process_* metrics restored")
		}
	}
}

// processFamilies materialises Go runtime and standard process metrics into the
// neutral IR. go_goroutines, go_memstats_heap_alloc_bytes,
// process_gc_pause_seconds_total, process_uptime_seconds and
// process_start_time_seconds are emitted on every platform.
// process_cpu_seconds_total, process_resident_memory_bytes,
// process_open_fds and process_max_fds are sourced from /proc and are
// Linux-only; on other platforms they are silently omitted. CPU time is divided
// by linuxUserHZ (see process_cpu_seconds_total's caveat).
//
// The two process counters (GC pause, CPU) carry their _total-suffixed names
// verbatim, so the Prometheus encoder emits them unchanged while the
// OpenMetrics encoder derives the _total-stripped base for the family lines —
// the same dual rendering user counters get.
func processFamilies() []metricFamily {
	var d processMetricsData
	collectProcessMetrics(&d)

	gauge := func(name, help, value string) metricFamily {
		return metricFamily{name: name, typ: typeGauge, help: help, samples: []sample{{value: value}}}
	}
	counter := func(name, help, value string) metricFamily {
		return metricFamily{name: name, typ: typeCounter, help: help, samples: []sample{{value: value}}}
	}

	fams := make([]metricFamily, 0, len(processFamilyNames))
	fams = append(fams,
		gauge(pmGoroutines, helpGoroutines, strconv.Itoa(d.goroutines)),
		gauge(pmHeapAllocBytes, helpHeapAlloc, strconv.FormatUint(d.heapAlloc, 10)),
		counter(pmGCPauseTotal, helpGCPause, formatValue(d.gcPause)),
		gauge(pmUptime, helpUptime, formatValue(d.uptime)),
		gauge(pmStartTime, helpStartTime, formatValue(d.startTime)),
	)
	if d.hasCPU() {
		fams = append(fams, counter(pmCPUTotal, helpCPU, formatValue(d.cpuSeconds)))
	}
	if d.hasRSS() {
		fams = append(fams, gauge(pmResidentBytes, helpResident, strconv.FormatInt(d.rss, 10)))
	}
	if d.hasOpenFDs() {
		fams = append(fams, gauge(pmOpenFDs, helpOpenFDs, strconv.Itoa(d.openFDs)))
		if d.hasMaxFDs() {
			fams = append(fams, gauge(pmMaxFDs, helpMaxFDs, formatMaxFDs(d.maxFDs)))
		}
	}
	return fams
}

// WriteProcessMetrics writes Go runtime and standard process metrics in
// Prometheus text format. It is a thin shim over the neutral IR
// (processFamilies) and the Prometheus encoder, preserved as part of the
// package's exported surface.
func WriteProcessMetrics(b *strings.Builder) {
	appendPrometheus(b, processFamilies())
}

// readProcCPUSeconds reads /proc/self/stat for utime+stime in seconds. Returns -1 on failure.
func readProcCPUSeconds() float64 {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("metrics: reading /proc/self/stat failed", "error", err)
		}
		return -1
	}
	return parseProcStatCPU(data)
}

// parseProcStatCPU parses /proc/self/stat content for utime+stime in seconds. Returns -1 on failure.
func parseProcStatCPU(data []byte) float64 {
	// Fields after the comm (which may contain spaces/parens): find last ')'
	s := string(data)
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+2 >= len(s) {
		return -1
	}
	fields := strings.Fields(s[idx+2:])
	// utime is field index 11, stime is 12 (0-indexed from after comm)
	if len(fields) < 13 {
		return -1
	}
	utime, err1 := strconv.ParseInt(fields[11], 10, 64)
	stime, err2 := strconv.ParseInt(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return -1
	}
	return float64(utime+stime) / linuxUserHZ
}

// resolvedProcStartTime memoizes the kernel-derived process start time. Because
// the start time is immutable for the process lifetime, it is resolved once via
// readProcStartTime and reused on every subsequent scrape, avoiding the
// redundant per-scrape reads of /proc/self/stat and /proc/stat. The result
// (including a -1 read failure) is captured on the first call; the caller
// applies the package-init fallback when it is negative.
var (
	procStartTimeOnce sync.Once
	procStartTimeVal  float64
)

func resolvedProcStartTime() float64 {
	procStartTimeOnce.Do(func() {
		procStartTimeVal = readProcStartTime()
	})
	return procStartTimeVal
}

// readProcStartTime returns the true process start time in seconds since the
// Unix epoch, derived from the kernel: the system boot time (btime, from
// /proc/stat) plus the process starttime (field 22 of /proc/self/stat, in clock
// ticks after boot) divided by linuxUserHZ. This matches how client_golang and
// procfs compute process_start_time_seconds. Returns -1 on any read or parse
// failure (including non-Linux platforms, where the /proc files are absent) so
// the caller can fall back to the package-init anchor.
func readProcStartTime() float64 {
	statData, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("metrics: reading /proc/self/stat for start time failed", "error", err)
		}
		return -1
	}
	ticks := parseProcStatStartTime(statData)
	if ticks < 0 {
		return -1
	}
	btimeData, err := os.ReadFile("/proc/stat")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("metrics: reading /proc/stat for btime failed", "error", err)
		}
		return -1
	}
	btime := parseProcStatBtime(btimeData)
	if btime < 0 {
		return -1
	}
	return float64(btime) + float64(ticks)/linuxUserHZ
}

// parseProcStatStartTime parses /proc/self/stat content for the process
// starttime (field 22, 1-indexed), in clock ticks since boot. Returns -1 on
// failure. Like parseProcStatCPU it locates the fields after the comm via the
// final ')', since the comm may itself contain spaces and parens; counting from
// field 3 (state) at slice index 0, starttime sits at index 22-3 = 19.
func parseProcStatStartTime(data []byte) int64 {
	s := string(data)
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+2 >= len(s) {
		return -1
	}
	fields := strings.Fields(s[idx+2:])
	const startTimeIdx = 19
	if len(fields) <= startTimeIdx {
		return -1
	}
	ticks, err := strconv.ParseInt(fields[startTimeIdx], 10, 64)
	if err != nil || ticks < 0 {
		return -1
	}
	return ticks
}

// parseProcStatBtime parses /proc/stat content for the "btime <epoch>" line,
// the system boot time in seconds since the Unix epoch. Returns -1 when the
// line is absent or carries a malformed value.
func parseProcStatBtime(data []byte) int64 {
	for line := range strings.SplitSeq(string(data), "\n") {
		after, ok := strings.CutPrefix(line, "btime ")
		if !ok {
			continue
		}
		btime, err := strconv.ParseInt(strings.TrimSpace(after), 10, 64)
		if err != nil || btime < 0 {
			return -1
		}
		return btime
	}
	return -1
}

// readProcRSS reads /proc/self/status for VmRSS in bytes. Returns 0 on failure.
func readProcRSS() int64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("metrics: reading /proc/self/status failed", "error", err)
		}
		return 0
	}
	return parseProcStatusRSS(data)
}

// parseProcStatusRSS parses /proc/self/status content for VmRSS in bytes. Returns 0 on failure.
func parseProcStatusRSS(data []byte) int64 {
	for line := range strings.SplitSeq(string(data), "\n") {
		after, ok := strings.CutPrefix(line, "VmRSS:")
		if !ok {
			continue
		}
		val := strings.TrimSpace(after)
		val, _ = strings.CutSuffix(val, " kB")
		val = strings.TrimSpace(val)
		kb, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// readProcFDs returns (open, maxFDs) file descriptors. Returns (-1, 0) on failure.
func readProcFDs() (open int, maxFDs int64) {
	f, err := os.Open("/proc/self/fd")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("metrics: opening /proc/self/fd failed", "error", err)
		}
		return -1, 0
	}
	names, err := f.Readdirnames(-1)
	_ = f.Close()
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("metrics: reading /proc/self/fd entries failed", "error", err)
		}
		return -1, 0
	}
	// Subtract the fd held by the open directory handle itself, which
	// appears in its own /proc/self/fd listing.
	open = openFDCount(names)
	// Read max from /proc/self/limits
	data, err := os.ReadFile("/proc/self/limits")
	if err != nil {
		if runtime.GOOS == goosLinux {
			slog.Debug("metrics: reading /proc/self/limits failed", "error", err)
		}
		return open, maxFDs
	}
	return open, parseProcLimitsMaxFDs(data)
}

// parseProcLimitsMaxFDs parses /proc/self/limits content for the soft "Max open
// files" limit in descriptors. Returns the unlimitedMaxFDs sentinel for the
// literal "unlimited" token (a successful reading), and 0 when the line is
// absent, carries no value, or holds a malformed number. Like parseProcStatCPU
// and parseProcStatusRSS, this is the pure-parse counterpart to its I/O reader
// (readProcFDs), so the limits parsing is unit testable in isolation.
func parseProcLimitsMaxFDs(data []byte) int64 {
	for line := range strings.SplitSeq(string(data), "\n") {
		after, ok := strings.CutPrefix(line, "Max open files")
		if !ok {
			continue
		}
		fields := strings.Fields(after)
		if len(fields) >= 1 {
			if strings.EqualFold(fields[0], "unlimited") {
				return unlimitedMaxFDs
			}
			n, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil {
				return 0
			}
			return n
		}
		return 0
	}
	return 0
}

// formatMaxFDs renders the soft "Max open files" limit as an
// OpenMetrics/Prometheus gauge value. The unlimitedMaxFDs sentinel renders as
// float64(math.MaxUint64) (the token "1.8446744073709552e+19"), matching what
// client_golang emits for an unlimited limit; every other value is its decimal
// form.
func formatMaxFDs(maxFDs int64) string {
	if maxFDs == unlimitedMaxFDs {
		return formatValue(float64(math.MaxUint64))
	}
	return strconv.FormatInt(maxFDs, 10)
}

// openFDCount converts the raw /proc/self/fd entry list into the number of
// descriptors actually open by the process. The directory handle opened to
// read /proc/self/fd appears in its own listing, so it is subtracted; the
// result is floored at 0.
func openFDCount(names []string) int {
	return max(len(names)-1, 0)
}
