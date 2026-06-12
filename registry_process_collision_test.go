package metrics

import (
	"testing"
)

// TestRegistry_ProcessFamilyNamesAreGuarded asserts the CURRENT guarded behavior:
// NewRegistry pre-reserves the built-in process_* family names, so registering a
// user metric whose family name collides with one fails fast with a panic instead
// of silently emitting a duplicate "# TYPE" line that breaks the scrape.
func TestRegistry_ProcessFamilyNamesAreGuarded(t *testing.T) {
	mustPanicContaining(t, "collides", func() {
		r := NewRegistry("")
		r.RegisterGauge(NewGauge("process_goroutines", "user gauge colliding with the built-in process metric"))
	})
}
