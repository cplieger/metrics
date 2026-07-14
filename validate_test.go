package metrics

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
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

// TestSanitizeLabelValues_InvalidUTF8NoPanic pins the record-time UTF-8
// policy: recording an invalid-UTF-8 label value never panics — the value is
// sanitized with the Unicode replacement character U+FFFD, the sanitized
// series appears in the exposition, a warning is logged exactly once, and a
// second record of the same bad value increments the SAME sanitized series
// without re-warning. Serial: it captures slog.Default.
func TestSanitizeLabelValues_InvalidUTF8NoPanic(t *testing.T) {
	buf := captureDebugLogs(t)
	bad := "\xff\xfe"
	lc := NewLabeledCounter("sanval_counter_total", "test", []string{"m"})
	lc.Inc(bad) // must not panic

	var b strings.Builder
	WriteLabeledCounter(&b, lc)
	if out := b.String(); !strings.Contains(out, "\uFFFD") {
		t.Errorf("exposition missing U+FFFD replacement: %q", out)
	}
	if got := strings.Count(buf.String(), "label value contained invalid UTF-8"); got != 1 {
		t.Fatalf("sanitize warnings after first record = %d, want 1", got)
	}

	lc.Inc(bad) // same bad value: same sanitized series, no second warn
	if got := len(lc.vals); got != 1 {
		t.Fatalf("series count after repeated bad value = %d, want 1", got)
	}
	for _, v := range lc.vals {
		if got := v.Load(); got != 2 {
			t.Errorf("sanitized series value = %d, want 2", got)
		}
	}
	if got := strings.Count(buf.String(), "label value contained invalid UTF-8"); got != 1 {
		t.Errorf("sanitize warnings after second record = %d, want 1 (no re-warn)", got)
	}
}

// TestSanitizeLabelValues_NoPanicAllTypes verifies the no-panic UTF-8 policy
// holds across every labeled metric type. Serial: it captures slog.Default.
func TestSanitizeLabelValues_NoPanicAllTypes(t *testing.T) {
	bad := "\xff\xfe"
	cases := []struct {
		kind string
		fn   func()
	}{
		{"LabeledCounter", func() { NewLabeledCounter("sanval2_counter_total", "test", []string{"m"}).Inc(bad) }},
		{"LabeledGauge", func() { NewLabeledGauge("sanval_gauge", "test", []string{"m"}).Set(1, bad) }},
		{"LabeledHistogram", func() { NewLabeledHistogram("sanval_hist_seconds", "test", []string{"m"}).Observe(0.5, bad) }},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			buf := captureDebugLogs(t)
			tc.fn() // must not panic
			if !strings.Contains(buf.String(), "label value contained invalid UTF-8") {
				t.Errorf("logs = %q, want sanitize warning", buf.String())
			}
		})
	}
}

// TestSanitizeLabelValues_LongInvalidUTF8LogTruncated pins the maxLogValueLen
// truncation: a hostile multi-hundred-byte invalid label value sanitizes
// without panicking, and the warning's value attribute retains a bounded
// prefix with the "...(truncated)" marker while dropping the
// attacker-controlled tail. Serial: it captures slog.Default.
func TestSanitizeLabelValues_LongInvalidUTF8LogTruncated(t *testing.T) {
	buf := captureDebugLogs(t)
	longInvalid := strings.Repeat("a", maxLogValueLen) + "\xff" + strings.Repeat("b", 64)

	NewLabeledCounter("long_sanval_total", "test", []string{"m"}).Inc(longInvalid) // must not panic

	logs := buf.String()
	if !strings.Contains(logs, "label value contained invalid UTF-8") {
		t.Fatalf("logs = %q, want sanitize warning", logs)
	}
	if !strings.Contains(logs, "...(truncated)") {
		t.Errorf("logs = %q, want truncation marker", logs)
	}
	if !strings.Contains(logs, strings.Repeat("a", 32)) {
		t.Errorf("logs = %q, want retained prefix", logs)
	}
	if strings.Contains(logs, strings.Repeat("b", 16)) {
		t.Errorf("logs = %q, want attacker-controlled tail truncated", logs)
	}

	// Multi-byte rune straddling byte 256: after sanitization the value is
	// 255 'a's, then U+00E9 occupying bytes 255-256, so a naive byte-256 cut
	// would split the rune. The logged value attribute must stay valid UTF-8
	// (truncateForLog backs the cut off to the previous rune boundary).
	buf.Reset()
	straddle := strings.Repeat("a", maxLogValueLen-1) + "\u00e9\xff" + strings.Repeat("b", 64)
	NewLabeledCounter("long_sanval_straddle_total", "test", []string{"m"}).Inc(straddle) // must not panic

	var line string
	for l := range strings.SplitSeq(buf.String(), "\n") {
		if strings.Contains(l, "long_sanval_straddle_total") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("logs = %q, want sanitize warning for straddle metric", buf.String())
	}
	_, attr, ok := strings.Cut(line, " value=")
	if !ok {
		t.Fatalf("log line %q, want value attribute", line)
	}
	if strings.HasPrefix(attr, `"`) {
		// TextHandler quotes (and \x-escapes) values that are not plain
		// printable strings; unquote to recover the raw logged attribute.
		unquoted, err := strconv.Unquote(attr)
		if err != nil {
			t.Fatalf("unquote value attribute %q: %v", attr, err)
		}
		attr = unquoted
	}
	if !utf8.ValidString(attr) {
		t.Errorf("logged value attribute %q is not valid UTF-8; truncation split a rune", attr)
	}
}

// TestSanitizeHelp_InvalidUTF8NoPanic pins the construction-time help-text
// UTF-8 policy: constructing any metric with invalid-UTF-8 help never panics —
// the help is sanitized with U+FFFD and a warning is logged naming the metric.
// Serial: it captures slog.Default.
func TestSanitizeHelp_InvalidUTF8NoPanic(t *testing.T) {
	badHelp := "\xff\xfe"
	cases := []struct {
		kind   string
		metric string
		fn     func()
	}{
		{"Counter", "sanhelp_counter_total", func() { NewCounter("sanhelp_counter_total", badHelp) }},
		{"LabeledCounter", "sanhelp_lcounter_total", func() { NewLabeledCounter("sanhelp_lcounter_total", badHelp, []string{"m"}) }},
		{"Gauge", "sanhelp_gauge", func() { NewGauge("sanhelp_gauge", badHelp) }},
		{"LabeledGauge", "sanhelp_lgauge", func() { NewLabeledGauge("sanhelp_lgauge", badHelp, []string{"m"}) }},
		{"Histogram", "sanhelp_hist_seconds", func() { NewHistogram("sanhelp_hist_seconds", badHelp) }},
		{"LabeledHistogram", "sanhelp_lhist_seconds", func() { NewLabeledHistogram("sanhelp_lhist_seconds", badHelp, []string{"m"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			buf := captureDebugLogs(t)
			tc.fn() // must not panic
			logs := buf.String()
			if !strings.Contains(logs, "help text contained invalid UTF-8") {
				t.Fatalf("logs = %q, want help sanitize warning", logs)
			}
			if !strings.Contains(logs, "metric="+tc.metric) {
				t.Errorf("logs = %q, want warning naming metric %s", logs, tc.metric)
			}
		})
	}
}

// TestSanitizeHelp_ExpositionCarriesReplacement verifies the sanitized help
// text reaches the exposition HELP line carrying the U+FFFD replacement, for
// both an unlabeled counter (counter.go wiring) and a labeled histogram
// (histogram.go wiring). Serial: it captures slog.Default.
func TestSanitizeHelp_ExpositionCarriesReplacement(t *testing.T) {
	_ = captureDebugLogs(t) // absorb the expected sanitize warnings

	c := NewCounter("sanhelp_expo_counter_total", "bad\xff\xfehelp")
	c.Inc()
	var cb strings.Builder
	WriteCounter(&cb, c)
	if out := cb.String(); !strings.Contains(out, "# HELP sanhelp_expo_counter_total bad\uFFFDhelp") {
		t.Errorf("counter HELP = %q, want U+FFFD replacement", out)
	}

	lh := NewLabeledHistogram("sanhelp_expo_hist_seconds", "bad\xff\xfehelp", []string{"m"})
	lh.Observe(0.5, "x")
	var hb strings.Builder
	WriteLabeledHistogram(&hb, lh)
	if out := hb.String(); !strings.Contains(out, "# HELP sanhelp_expo_hist_seconds bad\uFFFDhelp") {
		t.Errorf("labeled histogram HELP = %q, want U+FFFD replacement", out)
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

// TestSanitizeHelp_LongInvalidUTF8LogTruncated pins the maxLogValueLen
// truncation on the help-text warning path: constructing a metric with
// hostile multi-hundred-byte invalid help sanitizes without panicking, and
// the warning's help attribute retains a bounded prefix with the
// "...(truncated)" marker while dropping the tail -- the same bound the
// label-value warning already enforces. Serial: it captures slog.Default.
func TestSanitizeHelp_LongInvalidUTF8LogTruncated(t *testing.T) {
	buf := captureDebugLogs(t)
	longInvalid := strings.Repeat("a", maxLogValueLen) + "\xff" + strings.Repeat("b", 64)

	NewCounter("long_sanhelp_total", longInvalid) // must not panic

	logs := buf.String()
	if !strings.Contains(logs, "help text contained invalid UTF-8") {
		t.Fatalf("logs = %q, want help sanitize warning", logs)
	}
	if !strings.Contains(logs, "...(truncated)") {
		t.Errorf("logs = %q, want truncation marker on help attribute", logs)
	}
	if strings.Contains(logs, strings.Repeat("b", 16)) {
		t.Errorf("logs = %q, want hostile help tail truncated", logs)
	}
}
