package metrics

// Mutant-killing tests for unit metrics-u1 (package ".").
// Each test targets a specific surviving gremlins mutant by name in a comment.
// Tests use the internal package so they can reach unexported symbols
// (collect, serveOpenMetrics, readProcFDs). All new identifiers are prefixed
// gk_metrics_u1_ to avoid collisions with sibling units sharing the package.

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// gk_metrics_u1_failWriter is an http.ResponseWriter whose Write always fails,
// used to drive the `err != nil` write-error branches of Handler and
// serveOpenMetrics.
type gk_metrics_u1_failWriter struct {
	hdr http.Header
}

func (w *gk_metrics_u1_failWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}

func (w *gk_metrics_u1_failWriter) Write([]byte) (int, error) {
	return 0, errors.New("gk_metrics_u1: forced write failure")
}

func (w *gk_metrics_u1_failWriter) WriteHeader(int) {}

// gk_metrics_u1_captureDebug redirects the default slog logger to an in-memory
// buffer at Debug level for the duration of the test and returns that buffer.
func gk_metrics_u1_captureDebug(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

// gk_metrics_u1_countFamilies counts collected families whose name has prefix.
func gk_metrics_u1_countFamilies(fams []metricFamily, prefix string) int {
	t := 0
	for i := range fams {
		if strings.HasPrefix(fams[i].name, prefix) {
			t++
		}
	}
	return t
}

// Kills metrics.go:228:70 CONDITIONALS_NEGATION (`err != nil` -> `err == nil`).
// With a failing writer, io.WriteString returns a non-nil error. The original
// `err != nil` branch logs the failure; the mutated `err == nil` branch does
// not. Asserting the failure was logged distinguishes the two.
func TestGkMetricsU1_HandlerLogsOnWriteError(t *testing.T) {
	buf := gk_metrics_u1_captureDebug(t)
	reg := NewRegistry("")
	reg.RegisterCounter(NewCounter("gk_metrics_u1_neg_total", "h"))

	w := &gk_metrics_u1_failWriter{}
	reg.Handler()(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if got := buf.String(); !strings.Contains(got, "writing prometheus exposition failed") {
		t.Fatalf("Handler() with failing writer: debug log = %q, want it to contain the write-failure message (the err != nil branch must run)", got)
	}
}

// Kills openmetrics.go:43:70 CONDITIONALS_NEGATION (`err != nil` -> `err == nil`).
// Same mechanism as above for serveOpenMetrics.
func TestGkMetricsU1_ServeOpenMetricsLogsOnWriteError(t *testing.T) {
	buf := gk_metrics_u1_captureDebug(t)
	reg := NewRegistry("")
	reg.RegisterCounter(NewCounter("gk_metrics_u1_omneg_total", "h"))

	w := &gk_metrics_u1_failWriter{}
	reg.serveOpenMetrics(w)

	if got := buf.String(); !strings.Contains(got, "writing openmetrics exposition failed") {
		t.Fatalf("serveOpenMetrics() with failing writer: debug log = %q, want it to contain the write-failure message (the err != nil branch must run)", got)
	}
}

// Kills exposition.go:201:25 ARITHMETIC_BASE (`len(labeledCounters) + len(counters)`
// -> `- len(counters)`) in the collect() make-capacity sum. Registering 20 (> the
// constant len(processFamilyNames)==11) counters keeps the real capacity positive
// (cap = 0 + 20 + ... + 11 = 31) but makes the mutated capacity
// (0 - 20 + ... + 11 = -9) negative, so make([]metricFamily, 0, -9) panics.
func TestGkMetricsU1_CollectCapacityCounters(t *testing.T) {
	const prefix = "gk_metrics_u1_capctr_"
	reg := NewRegistry("")
	for i := 0; i < 20; i++ {
		reg.RegisterCounter(NewCounter(prefix+strconv.Itoa(i)+"_total", "h"))
	}

	fams := reg.collect() // panics under the mutation: make with negative cap

	if got := gk_metrics_u1_countFamilies(fams, prefix); got != 20 {
		t.Fatalf("collect() emitted %d %q families, want 20", got, prefix)
	}
}

// Kills exposition.go:201:41 ARITHMETIC_BASE (`len(counters) + len(labeledGauges)`
// -> `- len(labeledGauges)`). Registering 20 labeled gauges keeps real cap
// positive (0 + 0 + 20 + ... + 11) but drives the mutated cap negative
// (... - 20 ... + 11 = -9) -> make panics.
func TestGkMetricsU1_CollectCapacityLabeledGauges(t *testing.T) {
	const prefix = "gk_metrics_u1_caplg_"
	reg := NewRegistry("")
	for i := 0; i < 20; i++ {
		lg := NewLabeledGauge(prefix+strconv.Itoa(i), "h", []string{"k"})
		reg.RegisterLabeledGauge(lg)
		lg.Set(1, "v") // a labeled metric only emits a family once a combo is set
	}

	fams := reg.collect()

	if got := gk_metrics_u1_countFamilies(fams, prefix); got != 20 {
		t.Fatalf("collect() emitted %d %q families, want 20", got, prefix)
	}
}

// Kills exposition.go:202:17 ARITHMETIC_BASE (`len(gauges) + len(histograms)`
// -> `- len(histograms)`). Registering 20 histograms keeps real cap positive
// but drives the mutated cap negative -> make panics.
func TestGkMetricsU1_CollectCapacityHistograms(t *testing.T) {
	const prefix = "gk_metrics_u1_caphist_"
	reg := NewRegistry("")
	for i := 0; i < 20; i++ {
		reg.RegisterHistogram(NewHistogram(prefix+strconv.Itoa(i), "h"))
	}

	fams := reg.collect()

	if got := gk_metrics_u1_countFamilies(fams, prefix); got != 20 {
		t.Fatalf("collect() emitted %d %q families, want 20", got, prefix)
	}
}

// Kills exposition.go:202:35 ARITHMETIC_BASE (`len(histograms) + len(labeledHistograms)`
// -> `- len(labeledHistograms)`). Registering 20 labeled histograms keeps real
// cap positive but drives the mutated cap negative -> make panics.
func TestGkMetricsU1_CollectCapacityLabeledHistograms(t *testing.T) {
	const prefix = "gk_metrics_u1_caplh_"
	reg := NewRegistry("")
	for i := 0; i < 20; i++ {
		lh := NewLabeledHistogram(prefix+strconv.Itoa(i), "h", []string{"k"})
		reg.RegisterLabeledHistogram(lh)
		lh.Observe(0.1, "v")
	}

	fams := reg.collect()

	if got := gk_metrics_u1_countFamilies(fams, prefix); got != 20 {
		t.Fatalf("collect() emitted %d %q families, want 20", got, prefix)
	}
}

// Kills process.go:242:31 INVERT_NEGATIVES and ARITHMETIC_BASE on the literal
// in f.Readdirnames(-1). Both mutators change -1 to 1, so Readdirnames reads at
// most ONE fd entry instead of ALL of them; openFDCount(1 name) = max(1-1,0) = 0.
// A live process always has at least stdin/stdout/stderr plus the directory
// handle open, so the unmutated open count is >= 1 (in practice >= 3), while the
// mutated count is exactly 0.
func TestGkMetricsU1_ReadProcFDsReadsAllEntries(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("process fd metrics are Linux-only (/proc/self/fd)")
	}
	open, _ := readProcFDs()
	if open < 1 {
		t.Fatalf("readProcFDs() open = %d, want >= 1 (Readdirnames(-1) must read all fd entries; the mutation reads only 1 -> 0)", open)
	}
}
