package metrics

import (
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"
)

// labelEscaper escapes only the three characters mandated by the Prometheus text format
// for label values: backslash, double-quote, and newline.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// maxLogValueLen caps how much of a caller-forwarded (potentially
// attacker-influenced) value is embedded in a sanitization warning's log
// attributes: the prefix identifies the offending value while bounding the
// size of the log record a hostile multi-megabyte value would otherwise
// produce.
const maxLogValueLen = 256

// truncateForLog bounds an attacker-influenceable value for embedding in a log
// attribute, appending a "...(truncated)" marker when the value was cut.
func truncateForLog(s string) string {
	if len(s) <= maxLogValueLen {
		return s
	}
	// Back off to the previous rune boundary so the truncated prefix of a
	// just-sanitized (valid UTF-8) value cannot itself carry a split rune.
	cut := maxLogValueLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...(truncated)"
}

// sanitizeUTF8 is the shared UTF-8 sanitization engine for record-time label
// values and construction-time help text. It returns s unchanged when s is
// valid UTF-8; otherwise it returns s with every run of invalid bytes replaced
// by the Unicode replacement character U+FFFD and reports the change. Both
// exposition formats require valid UTF-8, but the library never panics on
// invalid UTF-8: degraded input is repaired and logged instead (unlike the
// fail-fast name, arity, and bucket guards, which stay panics).
func sanitizeUTF8(s string) (string, bool) {
	if utf8.ValidString(s) {
		return s, false
	}
	return strings.ToValidUTF8(s, "\uFFFD"), true
}

// sanitizeHelp returns help sanitized to valid UTF-8 via sanitizeUTF8. Invalid
// help would make a strict parser reject the entire scrape, so it is repaired
// with U+FFFD and a warning names the metric. Constructors hold no locks, so
// warning directly here is safe.
func sanitizeHelp(name, help string) string {
	san, changed := sanitizeUTF8(help)
	if changed {
		slog.Warn("metrics: help text contained invalid UTF-8; sanitized with U+FFFD",
			"metric", name, "help", truncateForLog(san))
	}
	return san
}

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
	owned := slices.Clone(labels)
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
