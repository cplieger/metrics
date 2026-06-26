package metrics

import (
	"strconv"
	"testing"
)

// FuzzParseProcStatCPU_CommRobustness checks that an arbitrary comm field (which
// may embed spaces, parens, and newlines) never corrupts utime/stime indexing:
// parseProcStatCPU keys off the LAST ')', so with utime=200, stime=100 at the
// fixed trailing positions the result stays 3.0s for any comm.
func FuzzParseProcStatCPU_CommRobustness(f *testing.F) {
	f.Add("cat")
	f.Add("weird (proc) name")
	f.Add("has)trailing")
	f.Add("")
	f.Add("new\nline")
	f.Fuzz(func(t *testing.T, comm string) {
		stat := []byte("1234 (" + comm + ") S 0 0 0 0 0 0 0 0 0 0 200 100")
		if got := parseProcStatCPU(stat); got != 3.0 {
			t.Errorf("parseProcStatCPU with comm %q = %v, want 3.0", comm, got)
		}
	})
}

// FuzzParseProcStatusRSS_ValueAmongNoise checks that a known VmRSS line placed
// first is parsed correctly regardless of arbitrary trailing status noise. kb is
// bounded to uint16 so kB*1024 cannot overflow int64.
func FuzzParseProcStatusRSS_ValueAmongNoise(f *testing.F) {
	f.Add(uint16(1024), "Threads:\t1\n")
	f.Add(uint16(0), "")
	f.Add(uint16(65535), "VmHWM:\t100 kB\nName:\tcat\n")
	f.Fuzz(func(t *testing.T, kb uint16, noise string) {
		data := []byte("VmRSS:\t" + strconv.FormatUint(uint64(kb), 10) + " kB\n" + noise)
		want := int64(kb) * 1024
		if got := parseProcStatusRSS(data); got != want {
			t.Errorf("parseProcStatusRSS kb=%d noise=%q = %d, want %d", kb, noise, got, want)
		}
	})
}
