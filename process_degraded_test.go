package metrics

import (
	"sync/atomic"
	"testing"
)

func TestProcDegradedTransition(t *testing.T) {
	tests := []struct {
		name     string
		initial  bool
		degraded bool
		want     bool
		wantNext bool
	}{
		{"healthy->healthy (no edge)", false, false, false, false},
		{"healthy->degraded (edge)", false, true, true, true},
		{"degraded->degraded (no edge)", true, true, false, true},
		{"degraded->healthy (edge)", true, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s atomic.Bool
			s.Store(tt.initial)
			if got := procDegradedTransition(&s, tt.degraded); got != tt.want {
				t.Errorf("procDegradedTransition(%v, %v) = %v, want %v",
					tt.initial, tt.degraded, got, tt.want)
			}
			if got := s.Load(); got != tt.wantNext {
				t.Errorf("state after transition = %v, want %v", got, tt.wantNext)
			}
		})
	}
}
