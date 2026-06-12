package metrics

import "strings"

// labelEscaper escapes only the three characters mandated by the Prometheus text format
// for label values: backslash, double-quote, and newline.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)

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

func validateLabelNames(labels []string) {
	for _, l := range labels {
		if !isValidLabelName(l) {
			panic("metrics: invalid label name: " + l)
		}
	}
}
