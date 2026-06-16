package metrics

import (
	"strings"
	"testing"
)

// This file targets specific mutants that the broader suite left alive
// (gremlins "solid gap" findings). Each test pins the EXACT boundary value or
// the BOTH-sides behavior a boundary/negation mutation would flip, so the
// mutated build fails while the real build passes. Tests are named after the
// behavior, not the mutant id.

// --- counter.go: Counter.Add negative guard is `n < 0` (boundary). ---
// `n <= 0` would reject the legal zero increment, so Add(0) must be a no-op.
func TestCounterAdd_ZeroIsAllowedNoPanic(t *testing.T) {
	c := NewCounter("mk_counter_add0", "test")
	c.Add(0) // must not panic: the guard is n < 0, not n <= 0
	if got := c.val.Load(); got != 0 {
		t.Errorf("Counter.Add(0) value = %d, want 0", got)
	}
	c.Add(7)
	if got := c.val.Load(); got != 7 {
		t.Errorf("Counter.Add(7) after Add(0) = %d, want 7", got)
	}
}

// --- counter/gauge/histogram: `len(labels) > 4` arity guard (boundary). ---
// `>= 4` would reject exactly four labels, which is the legal maximum.
func TestNewLabeledCounter_ExactlyFourLabelsAllowed(t *testing.T) {
	lc := NewLabeledCounter("mk_lc4_total", "test", []string{"a", "b", "c", "d"})
	lc.Inc("1", "2", "3", "4") // must not panic with four labels
	if got := lc.vals[labelKey{"1", "2", "3", "4"}].Load(); got != 1 {
		t.Errorf("LabeledCounter[4 labels] = %d, want 1", got)
	}
}

func TestNewLabeledGauge_ExactlyFourLabelsAllowed(t *testing.T) {
	lg := NewLabeledGauge("mk_lg4", "test", []string{"a", "b", "c", "d"})
	lg.Set(9, "1", "2", "3", "4") // must not panic with four labels

	var b strings.Builder
	WriteLabeledGauge(&b, lg)
	if out := b.String(); !strings.Contains(out, `a="1",b="2",c="3",d="4"`) {
		t.Errorf("four-label gauge not exposed correctly:\n%s", out)
	}
}

func TestNewLabeledHistogram_ExactlyFourLabelsAllowed(t *testing.T) {
	lh := NewLabeledHistogram("mk_lh4", "test", []string{"a", "b", "c", "d"})
	lh.Observe(0.5, "1", "2", "3", "4") // must not panic with four labels

	var b strings.Builder
	WriteLabeledHistogram(&b, lh)
	if out := b.String(); !strings.Contains(out, `a="1",b="2",c="3",d="4"`) {
		t.Errorf("four-label histogram not exposed correctly:\n%s", out)
	}
}

// --- metrics.go formatValue: lower integer-range guard `v >= -1e15` (boundary). ---
// `v > -1e15` would push the exact lower bound into the float 'g' path,
// rendering "-1e+15" instead of the bare integer. The existing TestFormatValue
// pins the +1e15 upper bound but not the -1e15 lower bound.
func TestFormatValue_NegativeIntegerLowerBoundary(t *testing.T) {
	if got := formatValue(-1e15); got != "-1000000000000000" {
		t.Errorf("formatValue(-1e15) = %q, want %q (>= -1e15 boundary renders as bare integer)",
			got, "-1000000000000000")
	}
}

// --- validate.go isLetter/isDigit character-range boundaries. ---
// isLetter's `b <= 'Z'` and isDigit's `b >= '0'` / `b <= '9'` are the exact
// inclusive ends of their ranges; a `<`/`>` mutation drops the boundary char,
// which would reject these otherwise-valid names.
func TestValidate_CharacterRangeBoundaries(t *testing.T) {
	// isLetter upper bound: 'Z' must count as a letter (first-position OK).
	if !isValidMetricName("Z") {
		t.Error(`isValidMetricName("Z") = false, want true (isLetter 'Z' boundary)`)
	}
	// isDigit lower bound: '0' must count as a digit in a non-initial position.
	if !isValidMetricName("a0") {
		t.Error(`isValidMetricName("a0") = false, want true (isDigit '0' boundary)`)
	}
	// isDigit upper bound: '9' must count as a digit in a non-initial position.
	if !isValidMetricName("a9") {
		t.Error(`isValidMetricName("a9") = false, want true (isDigit '9' boundary)`)
	}
}

// --- openmetrics.go acceptsOpenMetrics: `omQ <= 0` refusal guard (boundary). ---
// A bare q=0 with no text/plain present must be refused; `omQ < 0` would let
// the explicit q=0 refusal through (the existing q=0 case also lists text/plain,
// so the text/plain branch masks this boundary there).
func TestAcceptsOpenMetrics_BareQZeroRefused(t *testing.T) {
	if acceptsOpenMetrics("application/openmetrics-text;q=0") {
		t.Error(`acceptsOpenMetrics("application/openmetrics-text;q=0") = true, want false (q<=0 refusal)`)
	}
}

// --- openmetrics.go mediaQuality: `cur > q` max-selection (negation). ---
// With two entries for the same media type, the larger q wins. Negating the
// comparison would keep the first (smaller) q instead.
func TestMediaQuality_DuplicateTypeKeepsLargestQ(t *testing.T) {
	q, present := mediaQuality(
		"application/openmetrics-text;q=0.3,application/openmetrics-text;q=0.8",
		"application/openmetrics-text")
	if !present || q != 0.8 {
		t.Errorf("mediaQuality(duplicate type) = (%v, %v), want (0.8, true)", q, present)
	}
}

// --- metrics.go RegisterCounter/RegisterLabeledCounter: `sample != base`
// reservation guard (negation). ---
// A counter NOT named with _total reserves BOTH its base name and the derived
// _total sample-series name. Negating the guard would skip reserving the
// _total series, so a later metric colliding with that series would NOT panic.
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
