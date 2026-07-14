package metrics

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// labelEscaper escapes only the three characters mandated by the Prometheus text format
// for label values: backslash, double-quote, and newline.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// isValidMetricName checks if a metric name matches [a-zA-Z_:][a-zA-Z0-9_:]*.
func isValidMetricName(name string) bool {
	if name == "" {
		return false
	}
	for i, b := range name {
		if isLetter(b) || b == '_' || b == ':' {
			continue
		}
		if i > 0 && isDigit(b) {
			continue
		}
		return false
	}
	return true
}

// isValidLabelName checks if a label name matches [a-zA-Z_][a-zA-Z0-9_]*.
func isValidLabelName(name string) bool {
	if name == "" {
		return false
	}
	for i, b := range name {
		if isLetter(b) || b == '_' {
			continue
		}
		if i > 0 && isDigit(b) {
			continue
		}
		return false
	}
	return true
}

func isLetter(b rune) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isDigit(b rune) bool  { return b >= '0' && b <= '9' }

func validateMetricName(name string) {
	if !isValidMetricName(name) {
		panic("metrics: invalid metric name: " + name)
	}
}

// validateLabelNames returns an owned, fully validated copy of labels. It panics on
// any invalid label name and on any duplicate label name, so the returned slice is
// safe to retain: later mutation of the caller's original slice cannot alter it or
// bypass set-level invariants (uniqueness, the reserved-name and arity guards).
func validateLabelNames(labels []string) []string {
	owned := append([]string(nil), labels...)
	seen := make(map[string]struct{}, len(owned))
	for _, l := range owned {
		if !isValidLabelName(l) {
			panic("metrics: invalid label name: " + l)
		}
		if strings.HasPrefix(l, "__") {
			panic(`metrics: label name uses reserved "__" prefix: ` + l)
		}
		if _, ok := seen[l]; ok {
			panic("metrics: duplicate label name: " + l)
		}
		seen[l] = struct{}{}
	}
	return owned
}

// validateLabelValues panics if any label value in key is not valid UTF-8.
// Prometheus exposition (both text and OpenMetrics) requires label values to be
// valid UTF-8, and client_golang's WithLabelValues enforces the same via
// utf8.ValidString. Values are fixed by the caller at the point a label
// combination first becomes a series, so an invalid value is a programmer error
// and panics, consistent with the fail-fast label-name guards. It runs once, on
// the series-creation (loadOrStore miss) path, so the hot update path stays
// validation-free; an invalid value never becomes a stored series, so repeated
// calls with the same bad value re-take the miss path and panic again.
//
// Unlike the construction-time metric/label-name and arity guards, which trip
// deterministically on static, source-fixed inputs, this is a RECORD-TIME,
// data-dependent panic: it fires on label VALUE content supplied when a series
// is first recorded, which for HTTP instrumentation is runtime and
// caller-forwarded (and can be attacker-influenced). Label values are
// caller-owned: values derived from untrusted input (raw request paths, header
// contents) must be templated or validated to valid UTF-8 before use.
// Recording such a value inside an http handler is caught by net/http's
// per-request recover (the process survives), but recording it from a context
// without panic recovery (a background goroutine, ticker, or queue consumer,
// not an http handler) crashes the whole process.
func validateLabelValues(key labelKey) {
	for _, v := range key {
		if !utf8.ValidString(v) {
			panic("metrics: label value is not valid UTF-8: " + strconv.Quote(v))
		}
	}
}
