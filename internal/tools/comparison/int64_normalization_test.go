package comparison

import "testing"

// TestInt64FloatNormalization guards against spurious drift on int64 fields.
// A large integer stored in a CR as int64 is printed by fmt "%v" in exponential
// form and decoded from an API JSON response as float64; both must normalize to
// the same integer so value-based comparison holds. Without this, every int64
// field (e.g. disk size, instance memory) would report perpetual drift.
func TestInt64FloatNormalization(t *testing.T) {
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		{"int64 vs equal float64 (exponential)", int64(21474836480), float64(21474836480), true},
		{"int64 vs equal float64 (memory 4GiB)", int64(4294967296), float64(4294967296), true},
		{"int64 vs different float64", int64(21474836480), float64(21474836481), false},
		{"small int vs float", int64(2), float64(2), true},
		{"fractional float stays distinct from int", int64(2), float64(2.5), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompareAny(tc.a, tc.b); got != tc.want {
				t.Errorf("CompareAny(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// exercise the full spec-vs-response path too
			res, err := CompareExisting(map[string]interface{}{"v": tc.a}, map[string]interface{}{"v": tc.b})
			if err != nil {
				t.Fatalf("CompareExisting error: %v", err)
			}
			if res.IsEqual != tc.want {
				t.Errorf("CompareExisting IsEqual = %v, want %v (reason %+v)", res.IsEqual, tc.want, res.Reason)
			}
		})
	}
}
