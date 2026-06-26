package metrics

import "testing"

func TestValidateMetricName_Panics(t *testing.T) {
	invalid := []string{
		"",
		"123abc",
		"metric-name",
		"metric.name",
		"metric name",
		"metric\x00name",
		"metric\nname",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for name %q", name)
				}
			}()
			NewCounter(name, "test")
		})
	}
}

func TestValidateMetricName_Valid(t *testing.T) {
	valid := []string{
		"a",
		"_private",
		":colon",
		"abc_123",
		"A_B_C",
		"metric:submetric",
		"__internal",
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			// Should not panic
			NewCounter(name, "test")
		})
	}
}

func TestValidateLabelName_Panics(t *testing.T) {
	invalid := []string{
		"",
		"123abc",
		"label-name",
		"label:name", // colons not allowed in labels
		"label.name",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for label %q", name)
				}
			}()
			NewLabeledCounter("valid_metric", "test", []string{name})
		})
	}
}

// TestValidate_CharacterRangeBoundaries pins the inclusive ends of isLetter's
// and isDigit's ranges: 'Z' is a letter, '0' and '9' are digits in a
// non-initial position. Dropping a boundary character would reject these
// otherwise-valid names.
func TestValidate_CharacterRangeBoundaries(t *testing.T) {
	if !isValidMetricName("Z") {
		t.Error(`isValidMetricName("Z") = false, want true (isLetter 'Z' boundary)`)
	}
	if !isValidMetricName("a0") {
		t.Error(`isValidMetricName("a0") = false, want true (isDigit '0' boundary)`)
	}
	if !isValidMetricName("a9") {
		t.Error(`isValidMetricName("a9") = false, want true (isDigit '9' boundary)`)
	}
}

func TestIsValidLabelName_digitAfterFirstChar(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"letter then digit", "label1", true},
		{"underscore then digit", "_0", true},
		{"letters and digits mixed", "http2xx", true},
		{"leading digit rejected", "1label", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidLabelName(tc.in); got != tc.want {
				t.Errorf("isValidLabelName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
