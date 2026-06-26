package metrics

import (
	"regexp"
	"testing"
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
