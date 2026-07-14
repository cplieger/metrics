package metrics

import (
	"regexp"
	"testing"
	"unicode/utf8"
)

var (
	metricNameRe = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNameRe  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// FuzzMetricNameValidation cross-checks isValidMetricName against an
// independent regexp oracle for the Prometheus metric-name grammar.
func FuzzMetricNameValidation(f *testing.F) {
	f.Add("")
	f.Add("valid_name")
	f.Add("0startsdigit")
	f.Add("has-dash")
	f.Add("colon:ok")
	f.Add("\x00null")
	f.Fuzz(func(t *testing.T, s string) {
		got := isValidMetricName(s)
		want := metricNameRe.MatchString(s)
		if got != want {
			t.Errorf("isValidMetricName(%q) = %v, want %v", s, got, want)
		}
	})
}

// FuzzLabelNameValidation cross-checks isValidLabelName against an independent
// regexp oracle for the Prometheus label-name grammar (no colon allowed).
func FuzzLabelNameValidation(f *testing.F) {
	f.Add("")
	f.Add("valid_label")
	f.Add("0digit")
	f.Add("has:colon")
	f.Add("__reserved")
	f.Fuzz(func(t *testing.T, s string) {
		got := isValidLabelName(s)
		want := labelNameRe.MatchString(s)
		if got != want {
			t.Errorf("isValidLabelName(%q) = %v, want %v", s, got, want)
		}
	})
}

// FuzzLabelValueValidation asserts the series-creation label-value guard fires
// exactly on invalid-UTF-8 values: creating a labeled series must panic iff the
// value is not valid UTF-8, and never for a well-formed value.
func FuzzLabelValueValidation(f *testing.F) {
	f.Add("plain")
	f.Add("héllo")
	f.Add("")
	f.Add("\xff\xfe")
	f.Add("\xc3\x28")
	f.Fuzz(func(t *testing.T, s string) {
		wantPanic := !utf8.ValidString(s)
		func() {
			defer func() {
				r := recover()
				if wantPanic && r == nil {
					t.Errorf("expected panic for invalid-UTF-8 value %q, got none", s)
				}
				if !wantPanic && r != nil {
					t.Errorf("unexpected panic for valid value %q: %v", s, r)
				}
			}()
			NewLabeledCounter("fuzz_val_total", "test", []string{"m"}).Inc(s)
		}()
	})
}
