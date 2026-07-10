package comparison

import "testing"

// TestCompareExisting_SlicePrimitiveTypeNormalization proves the P0 fix: primitive elements inside a
// slice are compared with the same type-normalizing CompareAny used for scalar fields, so an int64 CR
// value and the same number decoded as float64 from an API response no longer drift inside arrays.
// Order is still significant (the fix does not turn slices into sets).
func TestCompareExisting_SlicePrimitiveTypeNormalization(t *testing.T) {
	tests := []struct {
		name     string
		mg       map[string]interface{}
		rm       map[string]interface{}
		expected bool
	}{
		{
			name:     "int64 vs float64 same value in slice is equal (was perpetual drift)",
			mg:       map[string]interface{}{"nums": []interface{}{int64(10), int64(20)}},
			rm:       map[string]interface{}{"nums": []interface{}{float64(10), float64(20)}},
			expected: true,
		},
		{
			name:     "large int64 vs exponential float64 in slice is equal",
			mg:       map[string]interface{}{"sizes": []interface{}{int64(2147483648)}},
			rm:       map[string]interface{}{"sizes": []interface{}{float64(2147483648)}},
			expected: true,
		},
		{
			name:     "different numbers in slice still differ",
			mg:       map[string]interface{}{"nums": []interface{}{int64(10)}},
			rm:       map[string]interface{}{"nums": []interface{}{float64(11)}},
			expected: false,
		},
		{
			name:     "order still matters for scalar elements",
			mg:       map[string]interface{}{"nums": []interface{}{int64(1), int64(2)}},
			rm:       map[string]interface{}{"nums": []interface{}{int64(2), int64(1)}},
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := CompareExisting(tt.mg, tt.rm)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.IsEqual != tt.expected {
				t.Errorf("expected IsEqual=%v, got %v (%s)", tt.expected, res.IsEqual, res.String())
			}
		})
	}
}

// TestCompareExisting_NestedSliceNoPanic proves nested arrays are compared recursively instead of
// panicking on `!=` over []interface{} (the previous behaviour), and that element types are normalized.
func TestCompareExisting_NestedSliceNoPanic(t *testing.T) {
	tests := []struct {
		name     string
		mg       map[string]interface{}
		rm       map[string]interface{}
		expected bool
	}{
		{
			name:     "nested arrays equal with mixed numeric types",
			mg:       map[string]interface{}{"matrix": []interface{}{[]interface{}{int64(1), int64(2)}}},
			rm:       map[string]interface{}{"matrix": []interface{}{[]interface{}{float64(1), float64(2)}}},
			expected: true,
		},
		{
			name:     "nested arrays differ",
			mg:       map[string]interface{}{"matrix": []interface{}{[]interface{}{int64(1), int64(2)}}},
			rm:       map[string]interface{}{"matrix": []interface{}{[]interface{}{int64(1), int64(3)}}},
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A failure of the previous implementation would panic here rather than return.
			res, err := CompareExisting(tt.mg, tt.rm)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.IsEqual != tt.expected {
				t.Errorf("expected IsEqual=%v, got %v (%s)", tt.expected, res.IsEqual, res.String())
			}
		})
	}
}

// TestCompareExisting_SliceNilElements proves nil slice elements are handled instead of panicking on
// reflect.TypeOf(nil).Kind().
func TestCompareExisting_SliceNilElements(t *testing.T) {
	tests := []struct {
		name     string
		mg       map[string]interface{}
		rm       map[string]interface{}
		expected bool
	}{
		{
			name:     "both nil elements are equal",
			mg:       map[string]interface{}{"list": []interface{}{nil, "x"}},
			rm:       map[string]interface{}{"list": []interface{}{nil, "x"}},
			expected: true,
		},
		{
			name:     "nil vs non-nil element differs",
			mg:       map[string]interface{}{"list": []interface{}{nil}},
			rm:       map[string]interface{}{"list": []interface{}{"x"}},
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := CompareExisting(tt.mg, tt.rm)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.IsEqual != tt.expected {
				t.Errorf("expected IsEqual=%v, got %v (%s)", tt.expected, res.IsEqual, res.String())
			}
		})
	}
}
