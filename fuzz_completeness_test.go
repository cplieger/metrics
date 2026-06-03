package metrics

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var (
	metricNameRe = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNameRe  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

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

func FuzzHelpTextExposition(f *testing.F) {
	f.Add("simple help")
	f.Add("line\nbreak")
	f.Add(`back\slash`)
	f.Add("quote\"here")
	f.Add("\x00\xff\n\\\n")
	f.Fuzz(func(t *testing.T, help string) {
		c := &Counter{name: "test_counter", help: help}
		var b strings.Builder
		WriteCounter(&b, c)
		out := b.String()
		for line := range strings.SplitSeq(out, "\n") {
			if !strings.HasPrefix(line, "# HELP ") {
				continue
			}
			helpContent := strings.TrimPrefix(line, "# HELP test_counter ")
			// No raw newlines within the HELP line (the line itself is split by \n)
			if strings.ContainsRune(helpContent, '\n') {
				t.Fatal("raw newline in HELP line")
			}
			// No unescaped backslashes: every \ must be followed by \ or n
			for i := 0; i < len(helpContent); i++ {
				if helpContent[i] == '\\' {
					if i+1 >= len(helpContent) {
						t.Fatal("trailing backslash in HELP line")
					}
					next := helpContent[i+1]
					if next != '\\' && next != 'n' {
						t.Fatalf("invalid escape \\%c in HELP line", next)
					}
					i++ // skip next
				}
			}
		}
	})
}

func FuzzOpenMetricsLabelExposition(f *testing.F) {
	f.Add("simple")
	f.Add("quote\"val")
	f.Add("back\\slash")
	f.Add("new\nline")
	f.Add("\x00\xff")
	f.Fuzz(func(t *testing.T, val string) {
		reg := NewRegistry("")
		lc := NewLabeledCounter("om_fuzz_counter", "help", []string{"lbl"})
		reg.RegisterLabeledCounter(lc)
		lc.Inc(val)
		rec := httptest.NewRecorder()
		reg.OpenMetricsHandler()(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		out := rec.Body.String()
		// Check all label values are properly quoted
		for line := range strings.SplitSeq(out, "\n") {
			if !strings.Contains(line, "{") {
				continue
			}
			braceStart := strings.IndexByte(line, '{')
			braceEnd := strings.LastIndexByte(line, '}')
			if braceEnd <= braceStart {
				t.Fatalf("unbalanced braces: %q", line)
			}
			inner := line[braceStart+1 : braceEnd]
			// Verify no unescaped quotes inside label values
			checkLabelQuoting(t, inner)
		}
	})
}

func checkLabelQuoting(t *testing.T, inner string) {
	t.Helper()
	inQuote := false
	for i := 0; i < len(inner); i++ {
		switch {
		case inner[i] == '\\' && inQuote:
			i++ // skip escaped char
		case inner[i] == '"':
			inQuote = !inQuote
		}
	}
	if inQuote {
		t.Fatalf("unbalanced quotes in label section: %q", inner)
	}
}

func FuzzRegistryFullExposition(f *testing.F) {
	f.Add("val1", "val2")
	f.Add("quote\"x", "nl\ny")
	f.Add("back\\s", "\x00")
	f.Fuzz(func(t *testing.T, lv1, lv2 string) {
		reg := NewRegistry("")
		c := NewCounter("fuzz_counter", "counter help")
		c.Inc()
		reg.RegisterCounter(c)
		g := NewGauge("fuzz_gauge", "gauge help")
		g.Set(42)
		reg.RegisterGauge(g)
		h := NewHistogram("fuzz_histogram", "hist help")
		h.Observe(0.1)
		reg.RegisterHistogram(h)
		lc := NewLabeledCounter("fuzz_labeled", "labeled help", []string{"a", "b"})
		lc.Inc(lv1, lv2)
		reg.RegisterLabeledCounter(lc)

		rec := httptest.NewRecorder()
		reg.Handler()(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		out := rec.Body.String()
		for line := range strings.SplitSeq(out, "\n") {
			if line == "" {
				continue
			}
			// Every non-empty line must be a comment or a sample
			if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "fuzz_") && !strings.HasPrefix(line, "process_") {
				t.Fatalf("unexpected line: %q", line)
			}
			// TYPE lines must have valid type
			if strings.HasPrefix(line, "# TYPE ") {
				parts := strings.Fields(line)
				if len(parts) < 4 {
					t.Fatalf("malformed TYPE line: %q", line)
				}
				typ := parts[len(parts)-1]
				switch typ {
				case "counter", "gauge", "histogram":
				default:
					t.Fatalf("unknown type %q in line: %q", typ, line)
				}
			}
		}
	})
}
