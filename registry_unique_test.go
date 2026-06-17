package metrics

import (
	"strings"
	"testing"
)

// mustPanicContaining runs fn and fails unless it panics with a string value
// containing want. Used to assert the registry's fail-fast collision guard.
func mustPanicContaining(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q, got none", want)
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, want) {
			t.Fatalf("panic = %v, want a string containing %q", r, want)
		}
	}()
	fn()
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
	// prefix. Before the fix, RegisterX mutated name in place via prefixed()
	// before reserving, so a second registration under a non-empty prefix
	// double-prefixed (app_app_foo) into a fresh family and silently
	// re-appended instead of panicking, defeating the uniqueness guard.
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
