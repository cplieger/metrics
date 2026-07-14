package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzOpenMetricsLabelExposition asserts the OpenMetrics handler keeps labeled
// counter lines brace- and quote-balanced for any label value.
func FuzzOpenMetricsLabelExposition(f *testing.F) {
	f.Add("simple")
	f.Add("quote\"val")
	f.Add("back\\slash")
	f.Add("new\nline")
	f.Add("\x00\xff")
	f.Fuzz(func(t *testing.T, val string) {
		// Invalid-UTF-8 label values are a documented fail-fast programmer error
		// (validateLabelValues), out of domain for this escaping-structure target.
		if !utf8.ValidString(val) {
			t.Skip()
		}
		reg := NewRegistry("")
		lc := NewLabeledCounter("om_fuzz_counter", "help", []string{"lbl"})
		reg.RegisterLabeledCounter(lc)
		lc.Inc(val)
		rec := httptest.NewRecorder()
		reg.OpenMetricsHandler()(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		assertExpositionLabelsBalanced(t, rec.Body.String())
	})
}

// FuzzOpenMetricsHelpExposition asserts the OpenMetrics HELP line for an
// arbitrary help string is well-formed: every backslash is a valid \\, \n or \"
// escape and no double-quote survives unescaped (carriage return is left raw).
func FuzzOpenMetricsHelpExposition(f *testing.F) {
	f.Add("simple help")
	f.Add("line\nbreak")
	f.Add(`back\slash`)
	f.Add(`quote"here`)
	f.Add("cr\rreturn")
	f.Add("\x00\xff\n\\\"\r")
	f.Fuzz(func(t *testing.T, help string) {
		c := &Counter{name: "om_help_counter", help: help}
		var b strings.Builder
		writeOMSimpleCounter(&b, c)
		for line := range strings.SplitSeq(b.String(), "\n") {
			content, ok := strings.CutPrefix(line, "# HELP om_help_counter ")
			if !ok {
				continue
			}
			for i := 0; i < len(content); i++ {
				switch content[i] {
				case '\\':
					if i+1 >= len(content) {
						t.Fatalf("trailing backslash in OM HELP: %q", content)
					}
					if n := content[i+1]; n != '\\' && n != 'n' && n != '"' {
						t.Fatalf("invalid escape \\%c in OM HELP: %q", n, content)
					}
					i++
				case '"':
					t.Fatalf("unescaped quote in OM HELP: %q", content)
				}
			}
		}
	})
}

// FuzzAcceptsOpenMetrics asserts acceptsOpenMetrics never returns true unless
// the openmetrics-text token is present with a positive q-value, cross-checking
// against mediaQuality.
func FuzzAcceptsOpenMetrics(f *testing.F) {
	f.Add("application/openmetrics-text")
	f.Add("text/plain;q=0.9,application/openmetrics-text;q=0.5")
	f.Add("application/openmetrics-text;q=0,text/plain;q=1")
	f.Add(",;q=;application/openmetrics-text;;q=banana,")
	f.Add("APPLICATION/OPENMETRICS-TEXT;Q=0.5")
	f.Add("text/plain;q=0.4,application/openmetrics-text;q=0.4")
	f.Add("*/*")
	f.Fuzz(func(t *testing.T, accept string) {
		got := acceptsOpenMetrics(accept)
		if got && !strings.Contains(strings.ToLower(accept), "openmetrics-text") {
			t.Fatalf("acceptsOpenMetrics(%q) = true but token absent from header", accept)
		}
		if got {
			q, present := mediaQuality(accept, "application/openmetrics-text")
			if !present || q <= 0 {
				t.Fatalf("acceptsOpenMetrics(%q) = true but mediaQuality reports present=%v q=%v", accept, present, q)
			}
		}
	})
}
