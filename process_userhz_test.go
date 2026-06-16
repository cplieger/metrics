package metrics

import (
	"math"
	"testing"
)

// TestLinuxUserHZ_Value documents the assumed clock-tick rate. If this ever
// needs to change, the change must be deliberate (see linuxUserHZ's doc on why
// it cannot be read at runtime without breaking the zero-dependency contract).
func TestLinuxUserHZ_Value(t *testing.T) {
	if linuxUserHZ != 100.0 {
		t.Errorf("linuxUserHZ = %v, want 100 (the near-universal Linux _SC_CLK_TCK)", linuxUserHZ)
	}
}

// TestParseProcStatCPU_DivisorIsLinuxUserHZ verifies parseProcStatCPU scales
// utime+stime by the linuxUserHZ constant rather than an inlined literal: the
// computed CPU seconds must equal (utime+stime)/linuxUserHZ exactly.
func TestParseProcStatCPU_DivisorIsLinuxUserHZ(t *testing.T) {
	const utime, stime = 200, 100
	// /proc/self/stat layout: "pid (comm) state ...". parseProcStatCPU keys off
	// the last ')', then reads utime at field index 11 and stime at index 12 of
	// the whitespace-split remainder. Eleven filler fields precede them.
	stat := []byte("1 (test proc) S 0 0 0 0 0 0 0 0 0 0 200 100")

	got := parseProcStatCPU(stat)
	want := float64(utime+stime) / linuxUserHZ
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("parseProcStatCPU = %v, want (utime+stime)/linuxUserHZ = %v", got, want)
	}
	// Guard the relationship explicitly: with USER_HZ=100, 300 ticks = 3.0s.
	if math.Abs(got-3.0) > 1e-12 {
		t.Errorf("parseProcStatCPU = %v, want 3.0 (300 ticks / 100)", got)
	}
}
