package metrics

import "testing"

func TestParseProcStatCPU(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want float64
	}{
		{
			name: "comm with spaces and parens",
			// pid (comm with (nested) spaces) state ppid pgrp ... fields[11]=utime fields[12]=stime
			in:   "1234 (weird (proc) name) S 1 1 1 0 0 0 0 0 0 0 200 100 0 0",
			want: 3.0, // (200+100)/100
		},
		{
			name: "simple comm",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 50 50 0 0",
			want: 1.0, // (50+50)/100
		},
		{
			name: "too few fields",
			in:   "1234 (cat) S 1 1 1 0 0",
			want: -1,
		},
		{
			name: "no closing paren",
			in:   "1234 cat S 1 1",
			want: -1,
		},
		{
			name: "non-numeric utime",
			in:   "1234 (cat) S 1 1 1 0 0 0 0 0 0 0 abc 50 0 0",
			want: -1,
		},
		{
			name: "empty input",
			in:   "",
			want: -1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProcStatCPU([]byte(tc.in))
			if got != tc.want {
				t.Errorf("parseProcStatCPU(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseProcStatusRSS(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{
			name: "VmRSS with kB suffix",
			in:   "Name:\tcat\nVmRSS:\t  1024 kB\nThreads:\t1\n",
			want: 1024 * 1024,
		},
		{
			name: "VmRSS without suffix",
			in:   "VmRSS:\t2048\n",
			want: 2048 * 1024,
		},
		{
			name: "no VmRSS line",
			in:   "Name:\tcat\nThreads:\t1\n",
			want: 0,
		},
		{
			name: "malformed value",
			in:   "VmRSS:\tnotanumber kB\n",
			want: 0,
		},
		{
			name: "empty input",
			in:   "",
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProcStatusRSS([]byte(tc.in))
			if got != tc.want {
				t.Errorf("parseProcStatusRSS(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestProcMetricsDegraded(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		cpuSeconds float64
		rss        int64
		openFDs    int
		want       bool
	}{
		{"linux all healthy", "linux", 1.5, 4096, 12, false},
		{"linux cpu read failed", "linux", -1, 4096, 12, true},
		{"linux rss zero", "linux", 1.5, 0, 12, true},
		{"linux rss negative", "linux", 1.5, -1, 12, true},
		{"linux fds read failed", "linux", 1.5, 4096, -1, true},
		{"linux all failed", "linux", -1, 0, -1, true},
		{"non-linux all failed stays quiet", "darwin", -1, 0, -1, false},
		{"non-linux healthy", "windows", 1.5, 4096, 12, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := procMetricsDegraded(tc.goos, tc.cpuSeconds, tc.rss, tc.openFDs)
			if got != tc.want {
				t.Errorf("procMetricsDegraded(%q, %v, %d, %d) = %v, want %v",
					tc.goos, tc.cpuSeconds, tc.rss, tc.openFDs, got, tc.want)
			}
		})
	}
}

func TestOpenFDCount(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  int
	}{
		{"empty listing", nil, 0},
		{"only the dir handle", []string{"3"}, 0},
		{"three real fds plus handle", []string{"0", "1", "2", "3"}, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := openFDCount(tc.names); got != tc.want {
				t.Errorf("openFDCount(%v) = %d, want %d", tc.names, got, tc.want)
			}
		})
	}
}

func FuzzParseProcStatCPU_CommRobustness(f *testing.F) {
	f.Add("cat")
	f.Add("weird (proc) name")
	f.Add("has)trailing")
	f.Add("")
	f.Add("new\nline")
	f.Fuzz(func(t *testing.T, comm string) {
		// /proc/self/stat is "pid (comm) state ...". parseProcStatCPU keys off the
		// LAST ')', so an arbitrary comm (embedded spaces, parens, newlines) must
		// not corrupt utime/stime field indexing. The trailing fields contain no
		// ')', so the appended ')' is always the last one; with utime=200,stime=100
		// at field indices 11,12 the result stays 3.0s for any comm.
		stat := []byte("1234 (" + comm + ") S 0 0 0 0 0 0 0 0 0 0 200 100")
		if got := parseProcStatCPU(stat); got != 3.0 {
			t.Errorf("parseProcStatCPU with comm %q = %v, want 3.0", comm, got)
		}
	})
}
