package metrics

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
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

// processStartTime is captured at package init as the single anchor for both
// process_start_time_seconds and process_uptime_seconds, so the two always
// reconcile (start + uptime == now) regardless of when a Registry is created.
var processStartTime = time.Now()

// goosLinux is the runtime.GOOS value for Linux, the only platform where
// /proc-based process metrics are expected to succeed.
const goosLinux = "linux"

var procDegraded atomic.Bool

// processMetricsData holds collected process metrics for shared use.
type processMetricsData struct {
	goroutines int
	heapAlloc  uint64
	gcPause    float64
	uptime     float64
	cpuSeconds float64
	rss        int64
	openFDs    int
	maxFDs     int64
}

// Series-presence predicates shared by both process-metric writers
// (WriteProcessMetrics and writeOMProcessMetrics). Single-sourcing them here
// keeps the two exposition formats emitting the same optional series set.
func (d *processMetricsData) hasCPU() bool     { return d.cpuSeconds >= 0 }
func (d *processMetricsData) hasRSS() bool     { return d.rss > 0 }
func (d *processMetricsData) hasOpenFDs() bool { return d.openFDs >= 0 }
func (d *processMetricsData) hasMaxFDs() bool  { return d.maxFDs > 0 }

// procMetricsDegraded reports whether process metric collection partially
// failed on a platform where /proc-based metrics are expected (Linux).
// cpuSeconds < 0, rss <= 0, or openFDs < 0 each signal a failed read.
func procMetricsDegraded(goos string, cpuSeconds float64, rss int64, openFDs int) bool {
	return goos == goosLinux && (cpuSeconds < 0 || rss <= 0 || openFDs < 0)
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
	d.uptime = time.Since(processStartTime).Seconds()
	d.cpuSeconds = readProcCPUSeconds()
	d.rss = readProcRSS()
	d.openFDs, d.maxFDs = readProcFDs()
	degraded := procMetricsDegraded(runtime.GOOS, d.cpuSeconds, d.rss, d.openFDs)
	if procDegradedTransition(&procDegraded, degraded) {
		if degraded {
			slog.Warn("process metric collection partially failed; some process_* metrics will be omitted",
				"cpu_ok", d.cpuSeconds >= 0, "rss_ok", d.rss > 0, "fds_ok", d.openFDs >= 0)
		} else {
			slog.Info("process metric collection recovered; process_* metrics restored")
		}
	}
}

// WriteProcessMetrics writes Go runtime and standard process metrics.
// process_goroutines/heap/gc/uptime/start_time are emitted on every platform.
// process_cpu_seconds_total, process_resident_memory_bytes, process_open_fds
// and process_max_fds are sourced from /proc and are Linux-only; on other
// platforms they are silently omitted. CPU time assumes USER_HZ=100 (Linux).
func WriteProcessMetrics(b *strings.Builder) {
	var d processMetricsData
	collectProcessMetrics(&d)
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", pmGoroutines, helpGoroutines, pmGoroutines, pmGoroutines, d.goroutines)
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", pmHeapBytes, helpHeapBytes, pmHeapBytes, pmHeapBytes, d.heapAlloc)
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n%s %s\n", pmGCPauseTotal, helpGCPause, pmGCPauseTotal, pmGCPauseTotal, formatValue(d.gcPause))
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", pmUptime, helpUptime, pmUptime, pmUptime, formatValue(d.uptime))
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", pmStartTime, helpStartTime, pmStartTime, pmStartTime, processStartTime.Unix())

	if d.hasCPU() {
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n%s %s\n", pmCPUTotal, helpCPU, pmCPUTotal, pmCPUTotal, formatValue(d.cpuSeconds))
	}
	if d.hasRSS() {
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", pmResidentBytes, helpResident, pmResidentBytes, pmResidentBytes, d.rss)
	}
	if d.hasOpenFDs() {
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", pmOpenFDs, helpOpenFDs, pmOpenFDs, pmOpenFDs, d.openFDs)
		if d.hasMaxFDs() {
			fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", pmMaxFDs, helpMaxFDs, pmMaxFDs, pmMaxFDs, d.maxFDs)
		}
	}
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
	clkTck := 100.0 // sysconf(_SC_CLK_TCK) is 100 on Linux
	return float64(utime+stime) / clkTck
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
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "Max open files") {
			fields := strings.Fields(line[len("Max open files"):])
			if len(fields) >= 1 {
				maxFDs, _ = strconv.ParseInt(fields[0], 10, 64)
			}
			break
		}
	}
	return open, maxFDs
}

// openFDCount converts the raw /proc/self/fd entry list into the number of
// descriptors actually open by the process. The directory handle opened to
// read /proc/self/fd appears in its own listing, so it is subtracted; the
// result is floored at 0.
func openFDCount(names []string) int {
	return max(len(names)-1, 0)
}
