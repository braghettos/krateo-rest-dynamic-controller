package pathparsing

import (
	"fmt"
	"strconv"
)

// isArrayIndex reports whether segment is a valid non-negative array index (e.g. "0", "12"). A redundant
// leading zero ("00") is rejected so it is never mistaken for an index — it is left available as an
// ordinary map key.
func isArrayIndex(segment string) (int, bool) {
	if segment == "" {
		return 0, false
	}
	for _, r := range segment {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	if len(segment) > 1 && segment[0] == '0' {
		return 0, false
	}
	idx, err := strconv.Atoi(segment)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// GetNestedField reads the value at the given path segments from obj, which may nest through both
// map[string]interface{} and []interface{} — a segment indexes into whichever the current container
// actually is: an array index (parsed via isArrayIndex) into a slice, a map key everywhere else. Mirrors
// unstructured.NestedFieldNoCopy's (value, found, err) contract, generalized to arrays.
func GetNestedField(obj interface{}, segments []string) (interface{}, bool, error) {
	cur := obj
	for i, seg := range segments {
		switch c := cur.(type) {
		case map[string]interface{}:
			val, ok := c[seg]
			if !ok {
				return nil, false, nil
			}
			cur = val
		case []interface{}:
			idx, ok := isArrayIndex(seg)
			if !ok {
				return nil, false, fmt.Errorf("path segment %q at position %d: array requires a non-negative integer index", seg, i)
			}
			if idx < 0 || idx >= len(c) {
				return nil, false, nil
			}
			cur = c[idx]
		default:
			return nil, false, fmt.Errorf("path segment %q at position %d: %T is not a map or array", seg, i, cur)
		}
	}
	return cur, true, nil
}

// SetNestedField writes value at the path formed by segments into obj, creating intermediate maps or
// arrays as needed (auto-vivification, generalizing unstructured.SetNestedField's map-only version). When a
// container along the path must be created — because it is absent or its existing type doesn't match what
// the next segment needs — the choice between map and array is made by looking at that next segment: a
// valid array index (see isArrayIndex) creates a []interface{}, anything else creates a
// map[string]interface{}. An existing array is grown with nil elements up to the written index.
func SetNestedField(obj map[string]interface{}, value interface{}, segments []string) error {
	if len(segments) == 0 {
		return fmt.Errorf("no path segments provided")
	}
	_, err := setNested(obj, value, segments)
	return err
}

func setNested(container interface{}, value interface{}, segments []string) (interface{}, error) {
	seg := segments[0]
	rest := segments[1:]

	switch c := container.(type) {
	case map[string]interface{}:
		if len(rest) == 0 {
			c[seg] = value
			return c, nil
		}
		child := c[seg]
		if !containerMatches(child, rest[0]) {
			child = newContainerFor(rest[0])
		}
		newChild, err := setNested(child, value, rest)
		if err != nil {
			return nil, err
		}
		c[seg] = newChild
		return c, nil

	case []interface{}:
		idx, ok := isArrayIndex(seg)
		if !ok {
			return nil, fmt.Errorf("path segment %q: expected a non-negative integer index into an array", seg)
		}
		for idx >= len(c) {
			c = append(c, nil)
		}
		if len(rest) == 0 {
			c[idx] = value
			return c, nil
		}
		child := c[idx]
		if !containerMatches(child, rest[0]) {
			child = newContainerFor(rest[0])
		}
		newChild, err := setNested(child, value, rest)
		if err != nil {
			return nil, err
		}
		c[idx] = newChild
		return c, nil

	default:
		return nil, fmt.Errorf("path segment %q: cannot traverse into %T", seg, container)
	}
}

// containerMatches reports whether v is already the right kind of container (map or array) to descend into
// for nextSegment, so an existing, correctly-shaped container along the path is reused rather than
// discarded.
func containerMatches(v interface{}, nextSegment string) bool {
	_, wantIndex := isArrayIndex(nextSegment)
	switch v.(type) {
	case map[string]interface{}:
		return !wantIndex
	case []interface{}:
		return wantIndex
	default:
		return false
	}
}

func newContainerFor(nextSegment string) interface{} {
	if _, ok := isArrayIndex(nextSegment); ok {
		return []interface{}{}
	}
	return map[string]interface{}{}
}

// RemoveNestedField deletes the value at the path formed by segments from obj, if present — a no-op if any
// segment along the path is absent. Mirrors unstructured.RemoveNestedField, generalized to arrays: removing
// the last segment when it indexes into an array deletes that element and shifts later elements down
// (rather than leaving a hole), matching how RemoveNestedField deletes a map key outright.
func RemoveNestedField(obj map[string]interface{}, segments []string) {
	if len(segments) == 0 {
		return
	}
	removeNested(obj, segments)
}

// removeNested removes the value at segments from container, returning the (possibly resized) container so
// the caller can write it back into its own parent when a slice shrinks.
func removeNested(container interface{}, segments []string) interface{} {
	seg := segments[0]
	rest := segments[1:]

	switch c := container.(type) {
	case map[string]interface{}:
		if len(rest) == 0 {
			delete(c, seg)
			return c
		}
		child, ok := c[seg]
		if !ok {
			return c
		}
		c[seg] = removeNested(child, rest)
		return c

	case []interface{}:
		idx, ok := isArrayIndex(seg)
		if !ok || idx < 0 || idx >= len(c) {
			return c
		}
		if len(rest) == 0 {
			return append(c[:idx], c[idx+1:]...)
		}
		c[idx] = removeNested(c[idx], rest)
		return c

	default:
		return container
	}
}
