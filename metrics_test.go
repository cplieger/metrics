package metrics

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegistryHandler(t *testing.T) {
	r := NewRegistry("")

	httpReqs := NewLabeledCounter("test_http_requests_total", "Total HTTP requests", []string{"method", "path", "status"})
	activeConns := NewGauge("test_active_connections", "Active connection count")
	tasks := NewCounter("test_tasks_total", "Total tasks")
	events := NewCounter("test_events_total", "Total events")
	httpDur := NewHistogram("test_http_request_duration_seconds", "HTTP request latency")

	r.RegisterLabeledCounter(httpReqs)
	r.RegisterGauge(activeConns)
	r.RegisterCounter(tasks)
	r.RegisterCounter(events)
	r.RegisterHistogram(httpDur)

	httpReqs.Inc("GET", "/api", "200")
	httpDur.Observe(0.05)
	tasks.Inc()

	h := r.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	out := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("unexpected Content-Type: %s", ct)
	}
	for _, want := range []string{
		"test_http_request_duration_seconds",
		"test_tasks_total",
		"test_events_total",
		"test_active_connections",
		"process_goroutines",
		"process_heap_bytes",
		"process_uptime_seconds",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if !strings.Contains(out, "# HELP") {
		t.Error("output missing # HELP lines")
	}
	if !strings.Contains(out, "# TYPE") {
		t.Error("output missing # TYPE lines")
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		want string
		in   float64
	}{
		// Whole finite values render as bare integers (valid in both formats).
		{in: 1.0, want: "1"},
		{in: 0, want: "0"},
		{in: -1, want: "-1"},
		{in: 42, want: "42"},
		{in: 1e15, want: "1000000000000000"},
		// The exact lower bound of the int64-exact range also renders bare.
		{in: -1e15, want: "-1000000000000000"},
		// Beyond the int64-exact range, fall back to shortest 'g'.
		{in: 1e16, want: "1e+16"},
		{in: -1e16, want: "-1e+16"},
		// Fractional values keep full precision (shortest round-trip).
		{in: 0.005, want: "0.005"},
		{in: 0.5, want: "0.5"},
		{in: 0.025, want: "0.025"},
		{in: 3.14, want: "3.14"},
		{in: 1e-7, want: "1e-07"},
		// Non-finite spec tokens (accepted case-insensitively by both formats).
		{in: math.Inf(1), want: "+Inf"},
		{in: math.Inf(-1), want: "-Inf"},
		{in: math.NaN(), want: "NaN"},
	}
	for _, tt := range tests {
		if got := formatValue(tt.in); got != tt.want {
			t.Errorf("formatValue(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRegistryAutoPrefix(t *testing.T) {
	r := NewRegistry("app")
	c := NewCounter("widgets_total", "Widgets")
	r.RegisterCounter(c)
	c.Inc()
	out := body(t, r)
	if !strings.Contains(out, "app_widgets_total 1") {
		t.Errorf("RegisterCounter on prefixed registry = %q, want app_widgets_total", out)
	}
	if strings.Contains(out, "\nwidgets_total") || strings.Contains(out, "app_app_") {
		t.Errorf("name not prefixed exactly once:\n%s", out)
	}
	if !strings.Contains(out, "process_uptime_seconds") || strings.Contains(out, "app_process_") {
		t.Errorf("process_* must not be prefixed:\n%s", out)
	}
}

func TestRegistryEmptyPrefixUnchanged(t *testing.T) {
	r := NewRegistry("")
	c := NewCounter("widgets_total", "Widgets")
	r.RegisterCounter(c)
	c.Inc()
	if out := body(t, r); !strings.Contains(out, "\nwidgets_total 1") {
		t.Errorf("empty prefix should leave name unchanged:\n%s", out)
	}
}

func TestRegistryInvalidPrefixPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewRegistry with invalid prefix should panic")
		}
	}()
	NewRegistry("bad-prefix!")
}

func TestRegistry_DuplicateRegistrationPanics(t *testing.T) {
	tests := []struct {
		register func(r *Registry)
		name     string
	}{
		{name: "two counters, identical name", register: func(r *Registry) {
			r.RegisterCounter(NewCounter("dup_total", "first"))
			r.RegisterCounter(NewCounter("dup_total", "second"))
		}},
		{name: "counter base-name collision (reqs vs reqs_total)", register: func(r *Registry) {
			r.RegisterCounter(NewCounter("reqs", "first"))
			r.RegisterCounter(NewCounter("reqs_total", "second"))
		}},
		{name: "counter vs gauge, same family", register: func(r *Registry) {
			r.RegisterCounter(NewCounter("widgets", "first"))
			r.RegisterGauge(NewGauge("widgets", "second"))
		}},
		{name: "counter vs labeled counter, same base", register: func(r *Registry) {
			r.RegisterCounter(NewCounter("hits_total", "first"))
			r.RegisterLabeledCounter(NewLabeledCounter("hits", "second", []string{"x"}))
		}},
		{name: "counter _total base collides with plain gauge", register: func(r *Registry) {
			r.RegisterCounter(NewCounter("http_total", "first"))
			r.RegisterGauge(NewGauge("http", "second"))
		}},
		{name: "plain gauge collides with later counter _total base", register: func(r *Registry) {
			r.RegisterGauge(NewGauge("http", "first"))
			r.RegisterCounter(NewCounter("http_total", "second"))
		}},
		{name: "gauge vs histogram, same name", register: func(r *Registry) {
			r.RegisterGauge(NewGauge("latency", "first"))
			r.RegisterHistogram(NewHistogram("latency", "second"))
		}},
		{name: "labeled gauge vs labeled histogram, same name", register: func(r *Registry) {
			r.RegisterLabeledGauge(NewLabeledGauge("size", "first", []string{"x"}))
			r.RegisterLabeledHistogram(NewLabeledHistogram("size", "second", []string{"x"}))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustPanicContaining(t, "collides", func() { tt.register(NewRegistry("")) })
		})
	}
}

func TestRegistry_DistinctNamesAcrossAllTypes(t *testing.T) {
	// Distinct family names across every metric type must register cleanly.
	r := NewRegistry("")
	r.RegisterCounter(NewCounter("c_total", "c"))
	r.RegisterGauge(NewGauge("g", "g"))
	r.RegisterLabeledCounter(NewLabeledCounter("lc", "lc", []string{"x"}))
	r.RegisterLabeledGauge(NewLabeledGauge("lg", "lg", []string{"x"}))
	r.RegisterHistogram(NewHistogram("h", "h"))
	r.RegisterLabeledHistogram(NewLabeledHistogram("lh", "lh", []string{"x"}))
}

func TestRegistry_SameNameDifferentRegistries(t *testing.T) {
	// Each registry owns an independent name space; the same name in two
	// registries is not a collision.
	r1 := NewRegistry("")
	r2 := NewRegistry("")
	r1.RegisterCounter(NewCounter("shared_total", "first"))
	r2.RegisterCounter(NewCounter("shared_total", "second"))
}

func TestRegistry_PrefixScopesFamilyNames(t *testing.T) {
	// The same bare name under different prefixes yields different families.
	a := NewRegistry("app_a")
	b := NewRegistry("app_b")
	a.RegisterCounter(NewCounter("reqs", "x"))
	b.RegisterCounter(NewCounter("reqs", "x"))

	// Within a single prefixed registry, a base-name collision still panics:
	// app_reqs (from "reqs") vs app_reqs (the base of "reqs_total").
	mustPanicContaining(t, "collides", func() {
		r := NewRegistry("app")
		r.RegisterCounter(NewCounter("reqs", "x"))
		r.RegisterCounter(NewCounter("reqs_total", "y"))
	})
}

func TestRegistry_ReRegistrationPanics(t *testing.T) {
	// Registering the same metric object twice must fail fast regardless of
	// prefix: RegisterX reserves the family before prefixing mutates the name,
	// so a second registration under a non-empty prefix cannot double-prefix
	// into a fresh family and silently re-append.
	t.Run("same registry, non-empty prefix", func(t *testing.T) {
		r := NewRegistry("app")
		c := NewCounter("reqs", "x")
		r.RegisterCounter(c)
		mustPanicContaining(t, "already registered", func() { r.RegisterCounter(c) })
	})
	t.Run("two registries", func(t *testing.T) {
		c := NewCounter("reqs", "x")
		NewRegistry("a").RegisterCounter(c)
		mustPanicContaining(t, "already registered", func() { NewRegistry("b").RegisterCounter(c) })
	})
	t.Run("gauge", func(t *testing.T) {
		r := NewRegistry("")
		g := NewGauge("temp", "x")
		r.RegisterGauge(g)
		mustPanicContaining(t, "already registered", func() { r.RegisterGauge(g) })
	})
	t.Run("histogram", func(t *testing.T) {
		r := NewRegistry("")
		h := NewHistogram("lat", "x")
		r.RegisterHistogram(h)
		mustPanicContaining(t, "already registered", func() { r.RegisterHistogram(h) })
	})
	t.Run("labeled counter", func(t *testing.T) {
		r := NewRegistry("")
		lc := NewLabeledCounter("hits", "x", []string{"m"})
		r.RegisterLabeledCounter(lc)
		mustPanicContaining(t, "already registered", func() { r.RegisterLabeledCounter(lc) })
	})
	t.Run("labeled gauge", func(t *testing.T) {
		r := NewRegistry("")
		lg := NewLabeledGauge("sizes", "x", []string{"m"})
		r.RegisterLabeledGauge(lg)
		mustPanicContaining(t, "already registered", func() { r.RegisterLabeledGauge(lg) })
	})
	t.Run("labeled histogram", func(t *testing.T) {
		r := NewRegistry("")
		lh := NewLabeledHistogram("durations", "x", []string{"m"})
		r.RegisterLabeledHistogram(lh)
		mustPanicContaining(t, "already registered", func() { r.RegisterLabeledHistogram(lh) })
	})
}

// TestRegistry_ProcessFamilyNamesAreGuarded asserts NewRegistry pre-reserves the
// built-in process_* family names, so a user metric colliding with one fails
// fast instead of silently emitting a duplicate "# TYPE" line.
func TestRegistry_ProcessFamilyNamesAreGuarded(t *testing.T) {
	mustPanicContaining(t, "collides", func() {
		r := NewRegistry("")
		r.RegisterGauge(NewGauge("process_goroutines", "user gauge colliding with the built-in process metric"))
	})
}

// TestRegisterCounter_ReservesDerivedTotalSeries verifies a counter NOT named
// with _total reserves both its base name and the derived _total sample series,
// so a later metric colliding with that series fails fast.
func TestRegisterCounter_ReservesDerivedTotalSeries(t *testing.T) {
	r := NewRegistry("")
	r.RegisterCounter(NewCounter("mk_events", "events")) // reserves mk_events AND mk_events_total
	mustPanicContaining(t, "collides", func() {
		r.RegisterGauge(NewGauge("mk_events_total", "collides with the counter's _total series"))
	})
}

func TestRegisterLabeledCounter_ReservesDerivedTotalSeries(t *testing.T) {
	r := NewRegistry("")
	r.RegisterLabeledCounter(NewLabeledCounter("mk_hits", "hits", []string{"m"}))
	mustPanicContaining(t, "collides", func() {
		r.RegisterGauge(NewGauge("mk_hits_total", "collides with the labeled counter's _total series"))
	})
}

// TestHandler_logsOnWriteError verifies a failed Prometheus exposition write is
// logged at debug level rather than silently swallowed.
func TestHandler_logsOnWriteError(t *testing.T) {
	buf := captureDebugLogs(t)
	reg := NewRegistry("")
	reg.RegisterCounter(NewCounter("writeerr_total", "h"))

	reg.Handler()(&failWriter{}, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if got := buf.String(); !strings.Contains(got, "writing prometheus exposition failed") {
		t.Fatalf("Handler() with failing writer: debug log = %q, want the write-failure message", got)
	}
}

// TestRegistry_FullHandler_ResetDelete_Concurrent races both handlers against
// concurrent Set/Reset/Delete and counter/histogram writes, exercising the
// registry read lock and the labeled-metric snapshot paths under contention.
func TestRegistry_FullHandler_ResetDelete_Concurrent(t *testing.T) {
	r := NewRegistry("")
	lg := NewLabeledGauge("rt6_full_gauge", "test", []string{"host"})
	c := NewCounter("rt6_full_counter", "test")
	h := NewHistogram("rt6_full_hist", "test")
	r.RegisterLabeledGauge(lg)
	r.RegisterCounter(c)
	r.RegisterHistogram(h)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				lg.Set(float64(i), "host"+strconv.Itoa(i%5))
				c.Inc()
				h.Observe(float64(i%10) * 0.01)
			}
		}
	})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				lg.Reset()
			}
		}
	})
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				lg.Delete("host" + strconv.Itoa(i%5))
			}
		}
	})

	handler := r.Handler()
	omHandler := r.OpenMetricsHandler()
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			}
		}
	})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				rec := httptest.NewRecorder()
				omHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			}
		}
	})

	time.Sleep(50 * time.Millisecond) // let producers/scrapers actually overlap
	close(stop)
	wg.Wait()
}

func BenchmarkRegistryHandler(b *testing.B) {
	r := NewRegistry("")
	httpReqs := NewLabeledCounter("bench_http_requests_total", "Total HTTP requests", []string{"method", "path", "status"})
	httpDur := NewHistogram("bench_http_request_duration_seconds", "HTTP request latency")
	tasks := NewCounter("bench_tasks_total", "Total tasks")
	r.RegisterLabeledCounter(httpReqs)
	r.RegisterHistogram(httpDur)
	r.RegisterCounter(tasks)

	httpReqs.Inc("GET", "/api", "200")
	httpDur.Observe(0.05)
	tasks.Inc()

	h := r.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}
