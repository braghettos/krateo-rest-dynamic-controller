package pathparsing

import (
	"fmt"
	"strconv"
	"strings"
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

// parsePredicate reports whether segment is a [?key=value] predicate and, if so, returns its key and
// expected value. Predicates arrive from ParsePath with the leading '?' intact.
func parsePredicate(segment string) (key, want string, ok bool) {
	if !strings.HasPrefix(segment, "?") {
		return "", "", false
	}
	k, v, found := strings.Cut(segment[1:], "=")
	if !found || k == "" {
		return "", "", false
	}
	return k, v, true
}

// matchPredicate resolves a [?key=value] predicate against a slice, returning the index of the single
// matching element. Comparison is by string form, so a predicate matches whether the document holds
// "2", 2 or 2.0 — API bodies round-trip through JSON with inconsistent numeric typing and a
// type-sensitive match would fail for reasons the author cannot see in the YAML.
//
// Ambiguity is an ERROR, never "take the first": silently picking one of several matches is exactly the
// wrong-element bug that predicates exist to eliminate. found=false means no match, which callers treat
// as absent (read) or as a hard error (write).
func matchPredicate(items []interface{}, key, want string) (idx int, found bool, err error) {
	hit := -1
	for i, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue // a scalar element cannot satisfy a field predicate
		}
		v, present := m[key]
		if !present {
			continue
		}
		if fmt.Sprintf("%v", v) != want {
			continue
		}
		if hit >= 0 {
			return 0, false, fmt.Errorf("predicate [?%s=%s] matches more than one element (indices %d and %d); it must select exactly one", key, want, hit, i)
		}
		hit = i
	}
	if hit < 0 {
		return 0, false, nil
	}
	return hit, true, nil
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
			if key, want, isPred := parsePredicate(seg); isPred {
				pi, hit, perr := matchPredicate(c, key, want)
				if perr != nil {
					return nil, false, fmt.Errorf("path segment %q at position %d: %w", seg, i, perr)
				}
				if !hit {
					return nil, false, nil
				}
				cur = c[pi]
				continue
			}
			idx, ok := isArrayIndex(seg)
			if !ok {
				return nil, false, fmt.Errorf("path segment %q at position %d: array requires a non-negative integer index or a [?key=value] predicate", seg, i)
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
		var idx int
		if key, want, isPred := parsePredicate(seg); isPred {
			// A predicate selects an EXISTING element; unlike an index it cannot auto-vivify, because
			// {key: want} alone is not a usable element (the rest of its fields are unknown) and
			// inventing one would append a half-formed entry to the outgoing body. No match is
			// therefore a hard error rather than a silent create — the write was aimed at something
			// the document does not contain, and dropping it would send an incomplete request.
			pi, hit, perr := matchPredicate(c, key, want)
			if perr != nil {
				return nil, fmt.Errorf("path segment %q: %w", seg, perr)
			}
			if !hit {
				return nil, fmt.Errorf("path segment %q: no array element matches; a predicate can only address an element that already exists", seg)
			}
			idx = pi
		} else {
			var ok bool
			idx, ok = isArrayIndex(seg)
			if !ok {
				return nil, fmt.Errorf("path segment %q: expected a non-negative integer index or a [?key=value] predicate into an array", seg)
			}
			for idx >= len(c) {
				c = append(c, nil)
			}
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
	wantIndex := segmentWantsArray(nextSegment)
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
	if segmentWantsArray(nextSegment) {
		return []interface{}{}
	}
	return map[string]interface{}{}
}

// segmentWantsArray reports whether a segment can only address an array: a numeric index, or a
// [?key=value] predicate. Both imply the container must be a slice — getting this wrong for predicates
// would have newContainerFor build a map and then fail to traverse it.
func segmentWantsArray(segment string) bool {
	if _, ok := isArrayIndex(segment); ok {
		return true
	}
	_, _, isPred := parsePredicate(segment)
	return isPred
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
		var idx int
		if key, want, isPred := parsePredicate(seg); isPred {
			// Removal is best-effort by contract, so an ambiguous or unmatched predicate is a no-op
			// rather than an error — the caller is deleting something that, by this path, is not
			// uniquely there.
			pi, hit, perr := matchPredicate(c, key, want)
			if perr != nil || !hit {
				return c
			}
			idx = pi
		} else {
			var ok bool
			idx, ok = isArrayIndex(seg)
			if !ok || idx < 0 || idx >= len(c) {
				return c
			}
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
