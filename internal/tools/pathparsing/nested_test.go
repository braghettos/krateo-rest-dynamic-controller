package pathparsing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetNestedField_ArrayElement is a regression test for issue #33: a fieldMapping/secretRef path must
// be able to read a value nested inside an array, e.g. Keycloak's spec.credentials[0].valueSecretRef.name.
func TestGetNestedField_ArrayElement(t *testing.T) {
	obj := map[string]interface{}{
		"spec": map[string]interface{}{
			"credentials": []interface{}{
				map[string]interface{}{
					"valueSecretRef": map[string]interface{}{
						"name": "alice-secret",
						"key":  "password",
					},
				},
				map[string]interface{}{
					"valueSecretRef": map[string]interface{}{
						"name": "alice-otp-secret",
						"key":  "otp",
					},
				},
			},
		},
	}

	segments, err := ParsePath("spec.credentials[0].valueSecretRef.name")
	require.NoError(t, err)
	assert.Equal(t, []string{"spec", "credentials", "0", "valueSecretRef", "name"}, segments)

	val, found, err := GetNestedField(obj, segments)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "alice-secret", val)

	segments2, err := ParsePath("spec.credentials[1].valueSecretRef.key")
	require.NoError(t, err)
	val2, found2, err := GetNestedField(obj, segments2)
	require.NoError(t, err)
	require.True(t, found2)
	assert.Equal(t, "otp", val2)
}

func TestGetNestedField(t *testing.T) {
	tests := []struct {
		name       string
		obj        interface{}
		segments   []string
		wantVal    interface{}
		wantFound  bool
		wantErr    bool
		errContain string
	}{
		{
			name:      "plain map path",
			obj:       map[string]interface{}{"a": map[string]interface{}{"b": "c"}},
			segments:  []string{"a", "b"},
			wantVal:   "c",
			wantFound: true,
		},
		{
			name:      "array index path",
			obj:       map[string]interface{}{"items": []interface{}{"x", "y", "z"}},
			segments:  []string{"items", "1"},
			wantVal:   "y",
			wantFound: true,
		},
		{
			name:      "missing map key",
			obj:       map[string]interface{}{"a": map[string]interface{}{}},
			segments:  []string{"a", "b"},
			wantFound: false,
		},
		{
			name:      "array index out of range",
			obj:       map[string]interface{}{"items": []interface{}{"x"}},
			segments:  []string{"items", "5"},
			wantFound: false,
		},
		{
			name:       "non-numeric segment into array",
			obj:        map[string]interface{}{"items": []interface{}{"x"}},
			segments:   []string{"items", "foo"},
			wantErr:    true,
			errContain: "array requires a non-negative integer index",
		},
		{
			name:       "descend into a scalar",
			obj:        map[string]interface{}{"a": "scalar"},
			segments:   []string{"a", "b"},
			wantErr:    true,
			errContain: "is not a map or array",
		},
		{
			name:      "empty segments returns the object itself",
			obj:       map[string]interface{}{"a": "b"},
			segments:  []string{},
			wantVal:   map[string]interface{}{"a": "b"},
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, found, err := GetNestedField(tt.obj, tt.segments)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContain != "" {
					assert.Contains(t, err.Error(), tt.errContain)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFound, found)
			if tt.wantFound {
				assert.Equal(t, tt.wantVal, val)
			}
		})
	}
}

func TestSetNestedField(t *testing.T) {
	t.Run("writes into an existing array element", func(t *testing.T) {
		obj := map[string]interface{}{
			"credentials": []interface{}{
				map[string]interface{}{"type": "password"},
			},
		}
		err := SetNestedField(obj, "s3cr3t", []string{"credentials", "0", "value"})
		require.NoError(t, err)
		val, found, err := GetNestedField(obj, []string{"credentials", "0", "value"})
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, "s3cr3t", val)
		// The rest of the element is untouched.
		typeVal, _, _ := GetNestedField(obj, []string{"credentials", "0", "type"})
		assert.Equal(t, "password", typeVal)
	})

	t.Run("auto-vivifies an array from an empty object", func(t *testing.T) {
		obj := map[string]interface{}{}
		err := SetNestedField(obj, "hunter2", []string{"credentials", "0", "value"})
		require.NoError(t, err)

		creds, ok := obj["credentials"].([]interface{})
		require.True(t, ok, "credentials should be created as an array")
		require.Len(t, creds, 1)

		val, found, err := GetNestedField(obj, []string{"credentials", "0", "value"})
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, "hunter2", val)
	})

	t.Run("grows an array to fit a higher index", func(t *testing.T) {
		obj := map[string]interface{}{"items": []interface{}{"a"}}
		err := SetNestedField(obj, "d", []string{"items", "3"})
		require.NoError(t, err)

		items, ok := obj["items"].([]interface{})
		require.True(t, ok)
		require.Len(t, items, 4)
		assert.Equal(t, "a", items[0])
		assert.Nil(t, items[1])
		assert.Nil(t, items[2])
		assert.Equal(t, "d", items[3])
	})

	t.Run("plain map path still works (backward compatible)", func(t *testing.T) {
		obj := map[string]interface{}{}
		err := SetNestedField(obj, "v", []string{"a", "b", "c"})
		require.NoError(t, err)
		val, found, _ := GetNestedField(obj, []string{"a", "b", "c"})
		require.True(t, found)
		assert.Equal(t, "v", val)
	})

	t.Run("no segments is an error", func(t *testing.T) {
		err := SetNestedField(map[string]interface{}{}, "v", nil)
		require.Error(t, err)
	})
}

func TestRemoveNestedField(t *testing.T) {
	t.Run("removes a key nested inside an array element", func(t *testing.T) {
		obj := map[string]interface{}{
			"credentials": []interface{}{
				map[string]interface{}{"type": "password", "legacyValue": "plain"},
			},
		}
		RemoveNestedField(obj, []string{"credentials", "0", "legacyValue"})
		_, found, _ := GetNestedField(obj, []string{"credentials", "0", "legacyValue"})
		assert.False(t, found)
		typeVal, found, _ := GetNestedField(obj, []string{"credentials", "0", "type"})
		require.True(t, found)
		assert.Equal(t, "password", typeVal)
	})

	t.Run("removing an array element shifts later elements down", func(t *testing.T) {
		obj := map[string]interface{}{"items": []interface{}{"a", "b", "c"}}
		RemoveNestedField(obj, []string{"items", "0"})
		items, ok := obj["items"].([]interface{})
		require.True(t, ok)
		assert.Equal(t, []interface{}{"b", "c"}, items)
	})

	t.Run("missing path is a no-op", func(t *testing.T) {
		obj := map[string]interface{}{"a": "b"}
		RemoveNestedField(obj, []string{"x", "y"})
		assert.Equal(t, map[string]interface{}{"a": "b"}, obj)
	})
}

// TestPredicate_ShapeIndependentAddressing covers [?key=value]: addressing an array element by CONTENT
// instead of position. The motivating case is Keycloak credentials — credentials[0] silently targets the
// wrong element if the order changes, whereas [?type=password] does not.
func TestPredicate_ShapeIndependentAddressing(t *testing.T) {
	doc := func() map[string]interface{} {
		return map[string]interface{}{
			"credentials": []interface{}{
				map[string]interface{}{"type": "otp", "secretDataRef": map[string]interface{}{"name": "s", "key": "otpseed"}},
				map[string]interface{}{"type": "password", "valueRef": map[string]interface{}{"name": "s", "key": "pw"}},
			},
		}
	}

	t.Run("parses to a predicate segment", func(t *testing.T) {
		segs, err := ParsePath("credentials[?type=password].valueRef.key")
		require.NoError(t, err)
		assert.Equal(t, []string{"credentials", "?type=password", "valueRef", "key"}, segs)
	})

	t.Run("reads the matching element regardless of position", func(t *testing.T) {
		// password is at index 1 here; the predicate must not care.
		segs, _ := ParsePath("credentials[?type=password].valueRef.key")
		v, found, err := GetNestedField(doc(), segs)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, "pw", v)

		segs, _ = ParsePath("credentials[?type=otp].secretDataRef.key")
		v, found, err = GetNestedField(doc(), segs)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, "otpseed", v)
	})

	t.Run("writes into the matching element", func(t *testing.T) {
		d := doc()
		segs, _ := ParsePath("credentials[?type=password].value")
		require.NoError(t, SetNestedField(d, "hunter2", segs))
		creds := d["credentials"].([]interface{})
		assert.Equal(t, "hunter2", creds[1].(map[string]interface{})["value"], "must write element 1, the password one")
		_, wrote0 := creds[0].(map[string]interface{})["value"]
		assert.False(t, wrote0, "must not touch the otp element")
	})

	t.Run("no match on write is a hard error, never a silent append", func(t *testing.T) {
		d := doc()
		segs, _ := ParsePath("credentials[?type=webauthn].value")
		err := SetNestedField(d, "x", segs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no array element matches")
		assert.Len(t, d["credentials"].([]interface{}), 2, "must not have appended a half-formed element")
	})

	t.Run("no match on read is simply absent", func(t *testing.T) {
		segs, _ := ParsePath("credentials[?type=webauthn].value")
		_, found, err := GetNestedField(doc(), segs)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("ambiguity is an error, not first-wins", func(t *testing.T) {
		d := map[string]interface{}{"c": []interface{}{
			map[string]interface{}{"type": "password", "v": "a"},
			map[string]interface{}{"type": "password", "v": "b"},
		}}
		segs, _ := ParsePath("c[?type=password].v")
		_, _, err := GetNestedField(d, segs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "more than one element")
	})

	t.Run("matches across JSON numeric typing", func(t *testing.T) {
		d := map[string]interface{}{"c": []interface{}{
			map[string]interface{}{"prio": float64(2), "v": "hit"},
		}}
		segs, _ := ParsePath("c[?prio=2].v")
		v, found, err := GetNestedField(d, segs)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, "hit", v, "float64(2) from JSON must satisfy [?prio=2]")
	})

	t.Run("removes the matching element", func(t *testing.T) {
		d := doc()
		segs, _ := ParsePath("credentials[?type=otp]")
		RemoveNestedField(d, segs)
		creds := d["credentials"].([]interface{})
		require.Len(t, creds, 1)
		assert.Equal(t, "password", creds[0].(map[string]interface{})["type"])
	})

	t.Run("malformed predicate is rejected at parse time", func(t *testing.T) {
		for _, bad := range []string{"c[?nokey]", "c[?=value]"} {
			_, err := ParsePath(bad)
			require.Errorf(t, err, "expected %q to be rejected", bad)
		}
	})
}
