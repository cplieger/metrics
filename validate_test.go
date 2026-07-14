package metrics

import (
	"strings"
	"testing"
)

func TestValidateMetricName_Panics(t *testing.T) {
	invalid := []string{
		"",
		"123abc",
		"metric-name",
		"metric.name",
		"metric name",
		"metric\x00name",
		"metric\nname",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for name %q", name)
				}
			}()
			NewCounter(name, "test")
		})
	}
}

func TestValidateMetricName_Valid(t *testing.T) {
	valid := []string{
		"a",
		"_private",
		":colon",
		"abc_123",
		"A_B_C",
		"metric:submetric",
		"__internal",
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			// Should not panic
			NewCounter(name, "test")
		})
	}
}

func TestValidateLabelName_Panics(t *testing.T) {
	invalid := []string{
		"",
		"123abc",
		"label-name",
		"label:name", // colons not allowed in labels
		"label.name",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for label %q", name)
				}
			}()
			NewLabeledCounter("valid_metric", "test", []string{name})
		})
	}
}

// TestValidate_CharacterRangeBoundaries pins the inclusive ends of isLetter's
// and isDigit's ranges: 'Z' is a letter, '0' and '9' are digits in a
// non-initial position. Dropping a boundary character would reject these
// otherwise-valid names.
func TestValidate_CharacterRangeBoundaries(t *testing.T) {
	if !isValidMetricName("Z") {
		t.Error(`isValidMetricName("Z") = false, want true (isLetter 'Z' boundary)`)
	}
	if !isValidMetricName("a0") {
		t.Error(`isValidMetricName("a0") = false, want true (isDigit '0' boundary)`)
	}
	if !isValidMetricName("a9") {
		t.Error(`isValidMetricName("a9") = false, want true (isDigit '9' boundary)`)
	}
}

func TestIsValidLabelName_digitAfterFirstChar(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"letter then digit", "label1", true},
		{"underscore then digit", "_0", true},
		{"letters and digits mixed", "http2xx", true},
		{"leading digit rejected", "1label", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidLabelName(tc.in); got != tc.want {
				t.Errorf("isValidLabelName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestValidateLabelNames_DuplicatePanics pins the set-level uniqueness invariant:
// constructing any labeled metric with a repeated label name is a fail-fast panic,
// since duplicate names would otherwise emit invalid series like {a="x",a="y"}.
func TestValidateLabelNames_DuplicatePanics(t *testing.T) {
	dup := []string{"method", "method"}
	cases := []struct {
		name string
		fn   func()
	}{
		{"LabeledCounter", func() { NewLabeledCounter("dup_counter_total", "test", dup) }},
		{"LabeledGauge", func() { NewLabeledGauge("dup_gauge", "test", dup) }},
		{"LabeledHistogram", func() { NewLabeledHistogram("dup_hist_seconds", "test", dup) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustPanicContaining(t, "duplicate label name: method", tc.fn)
		})
	}
}

// TestNewLabeledCounter_MutatingCallerSliceIsolated verifies the constructor owns
// its label-name slice: mutating the caller's original slice after construction
// does not alter the emitted label names.
func TestNewLabeledCounter_MutatingCallerSliceIsolated(t *testing.T) {
	labels := []string{"region"}
	lc := NewLabeledCounter("iso_counter_total", "test", labels)
	labels[0] = "zone" // mutate caller slice after construction
	lc.Inc("us-east")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()
	if !strings.Contains(out, `region="us-east"`) {
		t.Errorf("expected owned label name region, got: %s", out)
	}
	if strings.Contains(out, `zone=`) {
		t.Errorf("caller-slice mutation leaked into emitted labels: %s", out)
	}
}

// TestNewLabeledHistogram_MutationCannotBypassLeGuard verifies that owning the
// label-name slice prevents a post-construction mutation of the caller's slice
// from injecting the reserved "le" name into the emitted series.
func TestNewLabeledHistogram_MutationCannotBypassLeGuard(t *testing.T) {
	labels := []string{"path"}
	lh := NewLabeledHistogram("iso_hist_seconds", "test", labels)
	labels[0] = "le" // would collide with the reserved bucket-bound label if not owned
	lh.Observe(0.5, "/api")

	var b strings.Builder
	WriteLabeledHistogram(&b, lh)
	out := b.String()
	if !strings.Contains(out, `path="/api"`) {
		t.Errorf("expected owned label name path, got: %s", out)
	}
	// The only le="..." occurrences must be the bucket-bound label, never a user
	// label carrying the observed value "/api".
	if strings.Contains(out, `le="/api"`) {
		t.Errorf("caller-slice mutation bypassed the reserved le guard: %s", out)
	}
}

// TestValidateLabelNames_ReservedPrefixPanics pins the Prometheus data-model
// guard: a label name beginning with the reserved "__" prefix (used for
// internal names like __name__) is a fail-fast panic on construction of any
// labeled metric, matching client_golang's checkLabelName.
func TestValidateLabelNames_ReservedPrefixPanics(t *testing.T) {
	reserved := []string{"__foo", "__name__", "__"}
	for _, name := range reserved {
		t.Run(name, func(t *testing.T) {
			cases := []struct {
				kind string
				fn   func()
			}{
				{"LabeledCounter", func() { NewLabeledCounter("rsv_counter_total", "test", []string{name}) }},
				{"LabeledGauge", func() { NewLabeledGauge("rsv_gauge", "test", []string{name}) }},
				{"LabeledHistogram", func() { NewLabeledHistogram("rsv_hist_seconds", "test", []string{name}) }},
			}
			for _, tc := range cases {
				t.Run(tc.kind, func(t *testing.T) {
					mustPanicContaining(t, `reserved "__" prefix`, tc.fn)
				})
			}
		})
	}
}

// TestValidateLabelNames_SingleUnderscoreValid verifies a single leading
// underscore stays a valid label name; only the double-underscore "__" prefix
// is reserved.
func TestValidateLabelNames_SingleUnderscoreValid(t *testing.T) {
	cases := []struct {
		kind string
		fn   func()
	}{
		{"LabeledCounter", func() { NewLabeledCounter("us_counter_total", "test", []string{"_foo"}) }},
		{"LabeledGauge", func() { NewLabeledGauge("us_gauge", "test", []string{"_foo"}) }},
		{"LabeledHistogram", func() { NewLabeledHistogram("us_hist_seconds", "test", []string{"_foo"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			tc.fn() // must not panic
		})
	}
}

// TestValidateLabelValues_InvalidUTF8Panics pins the label-value UTF-8 guard:
// creating a series with an invalid-UTF-8 label value is a fail-fast panic
// (Prometheus exposition requires valid UTF-8), matching client_golang's
// WithLabelValues. The check runs on series creation, so it fires on the first
// update for each labeled metric type.
func TestValidateLabelValues_InvalidUTF8Panics(t *testing.T) {
	bad := "\xff\xfe"
	cases := []struct {
		kind string
		fn   func()
	}{
		{"LabeledCounter", func() { NewLabeledCounter("badval_counter_total", "test", []string{"m"}).Inc(bad) }},
		{"LabeledGauge", func() { NewLabeledGauge("badval_gauge", "test", []string{"m"}).Set(1, bad) }},
		{"LabeledHistogram", func() { NewLabeledHistogram("badval_hist_seconds", "test", []string{"m"}).Observe(0.5, bad) }},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			mustPanicContaining(t, "not valid UTF-8", tc.fn)
		})
	}
}

// TestValidateLabelValues_ValidSucceeds verifies that well-formed UTF-8 label
// values (including multi-byte runes) create series without panicking and are
// emitted in the exposition.
func TestValidateLabelValues_ValidSucceeds(t *testing.T) {
	lc := NewLabeledCounter("okval_counter_total", "test", []string{"lang"})
	lc.Inc("héllo") // multi-byte UTF-8
	lc.Inc("plain")

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	out := b.String()
	if !strings.Contains(out, `lang="héllo"`) {
		t.Errorf("expected multi-byte UTF-8 label value in output, got: %s", out)
	}
	if !strings.Contains(out, `lang="plain"`) {
		t.Errorf("expected plain label value in output, got: %s", out)
	}
}
