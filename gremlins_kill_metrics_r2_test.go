package metrics

// Round-2 mutant-kill tests for unit metrics-r2 (package ".").
// Internal test package so it can reach unexported symbols. New identifiers are
// prefixed gk_metrics_r2_ to avoid collisions with sibling units.
//
// Survivor dispositions for this round (see /workspace/.gremlins-run/round2/metrics.md):
//
//   - exposition.go:146:40 ARITHMETIC_BASE — the `*` in
//     make([]sample, 0, len(keys)*(len(lh.bounds)+3)) mutated to `/`. Both
//     operands are positive (len(keys) >= 1 after the empty-key guard,
//     len(lh.bounds)+3 >= 3), so `/` yields a non-negative slice CAPACITY HINT.
//     A make capacity only affects pre-allocation, never the appended output,
//     and can never go negative here, so no panic and no observable difference.
//     EQUIVALENT — left as-is.
//
//   - openmetrics.go:87:22 CONDITIONALS_BOUNDARY — `cur > q` mutated to
//     `cur >= q` in mediaQuality's max-selection. When cur == q, reassigning
//     q = cur is a no-op and present is already true, so (q, present) end
//     identical for every Accept header. EQUIVALENT — left as-is.
//
//   - process.go:186:22 CONDITIONALS_BOUNDARY — `idx+2 >= len(s)` mutated to
//     `idx+2 > len(s)` in parseProcStatCPU. The only flipped input is
//     idx+2 == len(s), where the mutant proceeds to s[len(s):] == "" ->
//     strings.Fields("") == [] -> len < 13 -> returns -1, identical to the
//     guard's -1. EQUIVALENT — left as-is.
//
//   - process.go:264:19 CONDITIONALS_BOUNDARY — `len(fields) >= 1` mutated to
//     `> 1` in the /proc/self/limits parse. A one-field line parses vs returns
//     0 -> a real observable difference. KILLED below, after extracting the
//     parse into parseProcLimitsMaxFDs (the pure-parse counterpart to
//     readProcFDs, matching parseProcStatCPU / parseProcStatusRSS).

import "testing"

// Kills process.go:264:19 CONDITIONALS_BOUNDARY (`len(fields) >= 1` -> `> 1`).
// The "single field" case is the boundary: with exactly one field after the
// "Max open files" label, the real `>= 1` guard parses it, while the `> 1`
// mutant demands two fields and silently returns 0.
func TestGkMetricsR2_ParseProcLimitsMaxFDs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		// Real kernel shape: soft, hard, then the "files" unit. fields[0] is the
		// soft limit that this parser reports.
		{"soft hard unit", "Max open files            1024                 4096                 files\n", 1024},
		// Exactly one field after the label -- the len(fields) >= 1 boundary.
		// The `> 1` mutant returns 0 here instead of 4096.
		{"single field", "Max open files 4096\n", 4096},
		// Label present but no value: nothing to index, so 0.
		{"label only", "Max open files\n", 0},
		// No matching line at all: 0.
		{"absent", "Max locked memory         0                    0                    bytes\n", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProcLimitsMaxFDs([]byte(tc.in)); got != tc.want {
				t.Errorf("parseProcLimitsMaxFDs(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
