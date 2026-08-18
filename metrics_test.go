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

	r.MustRegister(httpReqs, activeConns, tasks, events, httpDur)

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
		"go_goroutines",
		"go_memstats_heap_alloc_bytes",
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
	r.MustRegister(c)
	c.Inc()
	out := body(t, r)
	if !strings.Contains(out, "app_widgets_total 1") {
		t.Errorf("Register on prefixed registry = %q, want app_widgets_total", out)
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
	r.MustRegister(c)
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

func TestRegistry_DuplicateRegistrationErrors(t *testing.T) {
	tests := []struct {
		register func(r *Registry) error
		name     string
	}{
		{name: "two counters, identical name", register: func(r *Registry) error {
			r.MustRegister(NewCounter("dup_total", "first"))
			return r.Register(NewCounter("dup_total", "second"))
		}},
		{name: "counter vs labeled counter, identical name", register: func(r *Registry) error {
			r.MustRegister(NewCounter("hits_total", "first"))
			return r.Register(NewLabeledCounter("hits_total", "second", []string{"x"}))
		}},
		{name: "counter vs gauge, same family", register: func(r *Registry) error {
			r.MustRegister(NewCounter("widgets", "first"))
			return r.Register(NewGauge("widgets", "second"))
		}},
		{name: "gauge vs histogram, same name", register: func(r *Registry) error {
			r.MustRegister(NewGauge("latency", "first"))
			return r.Register(NewHistogram("latency", "second"))
		}},
		{name: "labeled gauge vs labeled histogram, same name", register: func(r *Registry) error {
			r.MustRegister(NewLabeledGauge("size", "first", []string{"x"}))
			return r.Register(NewLabeledHistogram("size", "second", []string{"x"}))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.register(NewRegistry(""))
			if err == nil || !strings.Contains(err.Error(), "collides") {
				t.Errorf("Register of colliding family = %v, want collision error", err)
			}
		})
	}
}

// TestRegistry_MustRegisterPanicsOnError pins the panicking door: MustRegister
// panics with the same error Register would return, both for a name collision
// and for a captured construction error.
func TestRegistry_MustRegisterPanicsOnError(t *testing.T) {
	t.Run("collision", func(t *testing.T) {
		r := NewRegistry("")
		r.MustRegister(NewCounter("dup_total", "first"))
		mustPanicContaining(t, "collides", func() { r.MustRegister(NewCounter("dup_total", "second")) })
	})
	t.Run("construction error", func(t *testing.T) {
		mustPanicContaining(t, "invalid metric name", func() {
			NewRegistry("").MustRegister(NewCounter("bad-name", "x"))
		})
	})
}

// TestRegistry_MustRegisterVariadicStopsAtFirstError verifies the variadic
// door registers metrics in order and panics on the FIRST error, leaving the
// earlier metrics registered and the later ones untouched.
func TestRegistry_MustRegisterVariadicStopsAtFirstError(t *testing.T) {
	r := NewRegistry("")
	ok := NewCounter("vararg_ok_total", "x")
	bad := NewCounter("bad-name", "x")
	after := NewCounter("vararg_after_total", "x")
	mustPanicContaining(t, "invalid metric name", func() { r.MustRegister(ok, bad, after) })

	ok.Inc()
	out := body(t, r)
	if !strings.Contains(out, "vararg_ok_total 1") {
		t.Errorf("metric registered before the panicking one is missing:\n%s", out)
	}
	if strings.Contains(out, "vararg_after_total") {
		t.Errorf("metric after the panicking one must not be registered:\n%s", out)
	}
	// The metric after the panic was never attached, so it stays registrable.
	if err := NewRegistry("").Register(after); err != nil {
		t.Errorf("Register of the never-reached metric = %v, want nil", err)
	}
}

func TestRegistry_DistinctNamesAcrossAllTypes(t *testing.T) {
	// Distinct family names across every metric type must register cleanly.
	NewRegistry("").MustRegister(
		NewCounter("c_total", "c"),
		NewGauge("g", "g"),
		NewLabeledCounter("lc", "lc", []string{"x"}),
		NewLabeledGauge("lg", "lg", []string{"x"}),
		NewHistogram("h", "h"),
		NewLabeledHistogram("lh", "lh", []string{"x"}),
	)
}

func TestRegistry_SameNameDifferentRegistries(t *testing.T) {
	// Each registry owns an independent name space; the same name in two
	// registries is not a collision.
	NewRegistry("").MustRegister(NewCounter("shared_total", "first"))
	NewRegistry("").MustRegister(NewCounter("shared_total", "second"))
}

func TestRegistry_PrefixScopesFamilyNames(t *testing.T) {
	// The same bare name under different prefixes yields different families.
	NewRegistry("app_a").MustRegister(NewCounter("reqs", "x"))
	NewRegistry("app_b").MustRegister(NewCounter("reqs", "x"))

	// Within a single prefixed registry, an identical prefixed name still
	// collides: app_reqs vs app_reqs.
	r := NewRegistry("app")
	r.MustRegister(NewCounter("reqs", "x"))
	mustRegisterError(t, r, NewCounter("reqs", "y"), "collides")
}

func TestRegistry_ReRegistrationErrors(t *testing.T) {
	// Registering the same metric object twice must fail regardless of
	// prefix: the registered flag is claimed before the name is prefixed, so
	// a second registration under a non-empty prefix cannot double-prefix
	// into a fresh family and silently re-append.
	t.Run("same registry, non-empty prefix", func(t *testing.T) {
		r := NewRegistry("app")
		c := NewCounter("reqs", "x")
		r.MustRegister(c)
		mustRegisterError(t, r, c, "already registered")
	})
	t.Run("two registries", func(t *testing.T) {
		c := NewCounter("reqs", "x")
		NewRegistry("a").MustRegister(c)
		mustRegisterError(t, NewRegistry("b"), c, "already registered")
	})
	t.Run("gauge", func(t *testing.T) {
		r := NewRegistry("")
		g := NewGauge("temp", "x")
		r.MustRegister(g)
		mustRegisterError(t, r, g, "already registered")
	})
	t.Run("histogram", func(t *testing.T) {
		r := NewRegistry("")
		h := NewHistogram("lat", "x")
		r.MustRegister(h)
		mustRegisterError(t, r, h, "already registered")
	})
	t.Run("labeled counter", func(t *testing.T) {
		r := NewRegistry("")
		lc := NewLabeledCounter("hits", "x", []string{"m"})
		r.MustRegister(lc)
		mustRegisterError(t, r, lc, "already registered")
	})
	t.Run("labeled gauge", func(t *testing.T) {
		r := NewRegistry("")
		lg := NewLabeledGauge("sizes", "x", []string{"m"})
		r.MustRegister(lg)
		mustRegisterError(t, r, lg, "already registered")
	})
	t.Run("labeled histogram", func(t *testing.T) {
		r := NewRegistry("")
		lh := NewLabeledHistogram("durations", "x", []string{"m"})
		r.MustRegister(lh)
		mustRegisterError(t, r, lh, "already registered")
	})
}

// registerConcurrently races two Register calls of the SAME metric value into
// two independent registries (independent r.mu, so nothing serializes them)
// and asserts exactly one wins the registered CAS while the loser reports
// already-registered. Under -race this pins the v4 registration contract that
// only the CAS winner may read or write the metric's name field: the loser
// must return without touching it, or the read races the winner's rename in
// attach.
func registerConcurrently(t *testing.T, m Metric) {
	t.Helper()
	registries := []*Registry{NewRegistry("a"), NewRegistry("b")}
	errs := make([]error, len(registries))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, r := range registries {
		wg.Go(func() {
			<-start
			errs[i] = r.Register(m)
		})
	}
	close(start)
	wg.Wait()

	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("concurrent Register results = [%v, %v], want exactly one success", errs[0], errs[1])
	}
	for _, err := range errs {
		if err != nil && !strings.Contains(err.Error(), "already registered") {
			t.Fatalf("concurrent Register loser error = %v, want already-registered", err)
		}
	}
}

// TestRegistry_ConcurrentCrossRegistryRegister registers one metric value
// into two registries concurrently, repeatedly, for an unlabeled and a
// labeled type (their attach closures write the name field differently:
// bare vs under the metric mutex). The registries are distinct on purpose —
// same-registry registrations are serialized by r.mu, so cross-registry is
// the only interleaving where the CAS is the sole synchronization point.
func TestRegistry_ConcurrentCrossRegistryRegister(t *testing.T) {
	t.Run("counter", func(t *testing.T) {
		for range 100 {
			registerConcurrently(t, NewCounter("xreg_total", "x"))
		}
	})
	t.Run("labeled counter", func(t *testing.T) {
		for range 100 {
			registerConcurrently(t, NewLabeledCounter("xreg_labeled_total", "x", []string{"m"}))
		}
	})
}

// TestRegistry_RegisterNilMetricErrors pins the nil guard on the registration
// doors: a nil interface value and a typed-nil pointer of every metric type
// return the named error from Register (the error door must not panic on a
// bad argument), and MustRegister panics with that same error.
func TestRegistry_RegisterNilMetricErrors(t *testing.T) {
	const want = "metrics: Register called with nil metric"
	cases := []struct {
		name string
		m    Metric
	}{
		{"nil interface", nil},
		{"typed-nil Counter", (*Counter)(nil)},
		{"typed-nil Gauge", (*Gauge)(nil)},
		{"typed-nil LabeledCounter", (*LabeledCounter)(nil)},
		{"typed-nil LabeledGauge", (*LabeledGauge)(nil)},
		{"typed-nil Histogram", (*Histogram)(nil)},
		{"typed-nil LabeledHistogram", (*LabeledHistogram)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NewRegistry("").Register(tc.m)
			if err == nil || err.Error() != want {
				t.Errorf("Register(%s) = %v, want %q", tc.name, err, want)
			}
		})
	}
	t.Run("MustRegister panics", func(t *testing.T) {
		mustPanicContaining(t, want, func() { NewRegistry("").MustRegister(nil) })
	})
}

// TestRegistry_FailedRegistrationLeavesMetricRegistrable pins the rollback in
// the registration flow: a metric refused for a family-name collision is not
// attached and not marked registered, so the caller can register it with a
// different registry afterwards.
func TestRegistry_FailedRegistrationLeavesMetricRegistrable(t *testing.T) {
	r1 := NewRegistry("")
	r1.MustRegister(NewCounter("rollback_total", "first"))
	c := NewCounter("rollback_total", "second")
	mustRegisterError(t, r1, c, "collides")

	r2 := NewRegistry("")
	if err := r2.Register(c); err != nil {
		t.Fatalf("Register after a collision elsewhere = %v, want nil", err)
	}
	c.Inc()
	if out := body(t, r2); !strings.Contains(out, "rollback_total 1") {
		t.Errorf("metric re-registered after rollback missing from exposition:\n%s", out)
	}
}

// TestRegistry_HistogramCollisionReleasesDerivedNames pins the transactional
// reservation in reserveHistogramFamily: a histogram refused because one of
// its four family names collides must release the names it already claimed,
// so a later metric can still use them.
func TestRegistry_HistogramCollisionReleasesDerivedNames(t *testing.T) {
	r := NewRegistry("")
	// Occupy the _sum name, so the histogram's base and _bucket reservations
	// succeed before the _sum one fails.
	r.MustRegister(NewGauge("txn_sum", "occupies the histogram's _sum name"))
	mustRegisterError(t, r, NewHistogram("txn", "x"), "collides")

	// The rolled-back base and _bucket names must be free again.
	r.MustRegister(NewGauge("txn", "base name released"))
	r.MustRegister(NewGauge("txn_bucket", "bucket name released"))
}

// TestRegistry_ProcessFamilyNamesAreGuarded asserts NewRegistry pre-reserves the
// built-in process_* family names, so a user metric colliding with one fails
// at registration instead of silently emitting a duplicate "# TYPE" line.
func TestRegistry_ProcessFamilyNamesAreGuarded(t *testing.T) {
	r := NewRegistry("")
	mustRegisterError(t, r,
		NewGauge("go_goroutines", "user gauge colliding with the built-in process metric"), "collides")
}

// TestRegisterCounter_VerbatimNameReservation locks the name-reservation
// semantics (unchanged from v3): a counter reserves ONLY its registered name
// (no derived _total normalization), so a base name and its _total-suffixed
// sibling are distinct families across all metric types.
func TestRegisterCounter_VerbatimNameReservation(t *testing.T) {
	NewRegistry("").MustRegister(
		NewCounter("mk_events", "events"),
		NewGauge("mk_events_total", "a distinct family in Prometheus text format"),
		NewCounter("mk_reqs", "first"),
		NewCounter("mk_reqs_total", "a second, distinct counter family"),
		NewLabeledCounter("mk_hits", "hits", []string{"m"}),
		NewGauge("mk_hits_total", "also distinct"),
	)
}

// TestHandler_logsOnWriteError verifies a failed Prometheus exposition write is
// logged at debug level rather than silently swallowed.
func TestHandler_logsOnWriteError(t *testing.T) {
	buf := captureDebugLogs(t)
	reg := NewRegistry("")
	reg.MustRegister(NewCounter("writeerr_total", "h"))

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
	r.MustRegister(lg, c, h)

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
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
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
	r.MustRegister(httpReqs, httpDur, tasks)

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
