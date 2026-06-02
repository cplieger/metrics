package metrics

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Timer measures elapsed time and reports to a Histogram.
type Timer struct {
	start time.Time
	hist  *Histogram
}

// NewTimer starts a timer that will observe into the given histogram.
func NewTimer(h *Histogram) *Timer {
	return &Timer{start: time.Now(), hist: h}
}

// ObserveDuration records the elapsed time since the timer was created.
func (t *Timer) ObserveDuration() time.Duration {
	d := time.Since(t.start)
	t.hist.Observe(d.Seconds())
	return d
}

// processStartTime is captured at package init for process_start_time_seconds.
var processStartTime = float64(time.Now().Unix())

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

// collectProcessMetrics gathers all process metrics into a struct.
func collectProcessMetrics(d *processMetricsData, startTime time.Time) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	d.goroutines = runtime.NumGoroutine()
	d.heapAlloc = m.HeapAlloc
	d.gcPause = float64(m.PauseTotalNs) / 1e9
	d.uptime = time.Since(startTime).Seconds()
	d.cpuSeconds = readProcCPUSeconds()
	d.rss = readProcRSS()
	d.openFDs, d.maxFDs = readProcFDs()
}

// WriteProcessMetrics writes Go runtime and standard process metrics.
func WriteProcessMetrics(b *strings.Builder, startTime time.Time) {
	var d processMetricsData
	collectProcessMetrics(&d, startTime)
	fmt.Fprintf(b, "# HELP process_goroutines Number of goroutines\n# TYPE process_goroutines gauge\nprocess_goroutines %d\n", d.goroutines)
	fmt.Fprintf(b, "# HELP process_heap_bytes Heap memory in use\n# TYPE process_heap_bytes gauge\nprocess_heap_bytes %d\n", d.heapAlloc)
	fmt.Fprintf(b, "# HELP process_gc_pause_seconds_total Total GC pause time\n# TYPE process_gc_pause_seconds_total counter\nprocess_gc_pause_seconds_total %.6f\n", d.gcPause)
	fmt.Fprintf(b, "# HELP process_uptime_seconds Process uptime\n# TYPE process_uptime_seconds gauge\nprocess_uptime_seconds %.3f\n", d.uptime)
	fmt.Fprintf(b, "# HELP process_start_time_seconds Start time of the process since unix epoch in seconds\n# TYPE process_start_time_seconds gauge\nprocess_start_time_seconds %.0f\n", processStartTime)

	if d.cpuSeconds >= 0 {
		fmt.Fprintf(b, "# HELP process_cpu_seconds_total Total user and system CPU time spent in seconds\n# TYPE process_cpu_seconds_total counter\nprocess_cpu_seconds_total %.6f\n", d.cpuSeconds)
	}
	if d.rss > 0 {
		fmt.Fprintf(b, "# HELP process_resident_memory_bytes Resident memory size in bytes\n# TYPE process_resident_memory_bytes gauge\nprocess_resident_memory_bytes %d\n", d.rss)
	}
	if d.openFDs >= 0 {
		fmt.Fprintf(b, "# HELP process_open_fds Number of open file descriptors\n# TYPE process_open_fds gauge\nprocess_open_fds %d\n", d.openFDs)
		if d.maxFDs > 0 {
			fmt.Fprintf(b, "# HELP process_max_fds Maximum number of open file descriptors\n# TYPE process_max_fds gauge\nprocess_max_fds %d\n", d.maxFDs)
		}
	}
}

// readProcCPUSeconds reads /proc/self/stat for utime+stime in seconds. Returns -1 on failure.
func readProcCPUSeconds() float64 {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return -1
	}
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
		return 0
	}
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
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1, 0
	}
	open = len(entries)
	// Read max from /proc/self/limits
	data, err := os.ReadFile("/proc/self/limits")
	if err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.HasPrefix(line, "Max open files") {
				fields := strings.Fields(line[len("Max open files"):])
				if len(fields) >= 1 {
					maxFDs, _ = strconv.ParseInt(fields[0], 10, 64)
				}
				break
			}
		}
	}
	return open, maxFDs
}
