package pathparsing

import (
	"fmt"
	"strings"

	"github.com/go-andiamo/splitter"
)

var dotSplitter = splitter.MustCreateSplitter('.', splitter.SquareBrackets, splitter.DoubleQuotes, splitter.SingleQuotes)

// ParsePath parses a path string into segments.
// Supports: dot notation (a.b.c) and bracket notation with both single and double quotes (a['b.c'], a["b.c"]).
func ParsePath(path string) ([]string, error) {
	if path == "" {
		return []string{""}, nil
	}

	if strings.Contains(path, " ") {
		return nil, fmt.Errorf("malformed path: contains spaces")
	}

	// Check for consecutive dots outside brackets (error).
	// E.g., a..b is invalid but ['a..b'] is valid.
	inBracket := false
	leadingDotsEnded := false
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '[':
			inBracket = true
			leadingDotsEnded = true
		case ']':
			inBracket = false
		case '.':
			if !inBracket && leadingDotsEnded && i+1 < len(path) && path[i+1] == '.' {
				return nil, fmt.Errorf("malformed path: consecutive dots")
			}
		default:
			leadingDotsEnded = true
		}
	}

	// Check for trailing dot (end of path)
	if len(path) > 0 && path[len(path)-1] == '.' {
		// Make sure it's not inside a bracket
		inBracket := false
		for i := 0; i < len(path)-1; i++ {
			switch path[i] {
			case '[':
				inBracket = true
			case ']':
				inBracket = false
			}
		}
		if !inBracket {
			return nil, fmt.Errorf("malformed path: trailing dot")
		}
	}

	// Split by dots outside brackets
	// Example: "a.b['c.d'].e" -> ["a", "b", "['c.d']", "e"]
	parts, err := dotSplitter.Split(path)
	if err != nil {
		return nil, fmt.Errorf("malformed path: %w", err)
	}

	// Handle leading dots - attach to first segment
	merged := make([]string, 0, len(parts))
	leadingDots := 0

	for _, p := range parts {
		if p == "" {
			leadingDots++
		} else {
			prefix := strings.Repeat(".", leadingDots)
			merged = append(merged, prefix+p)
			leadingDots = 0
		}
	}

	segments := make([]string, 0, len(merged))
	for _, p := range merged {
		segs, err := parseSegmentGroup(p)
		if err != nil {
			return nil, err
		}
		segments = append(segments, segs...)
	}

	return segments, nil
}

// isDigits reports whether s is non-empty and consists entirely of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseSegmentGroup parses one dot-separated token into one or more path segments. A token is usually a
// single segment (a plain name, or one bracket group), but a name immediately followed by an array-index
// bracket with no separating dot — "credentials[0]" — is also accepted and split into two segments
// ("credentials", "0"), so an array index need not be preceded by a dot.
func parseSegmentGroup(s string) ([]string, error) {
	if s == "" {
		return nil, fmt.Errorf("malformed path: empty segment")
	}

	// Strip leading dots for validation, but keep them in the result.
	leadingDots := 0
	for leadingDots < len(s) && s[leadingDots] == '.' {
		leadingDots++
	}

	if leadingDots == len(s) {
		// All dots, no content
		return nil, fmt.Errorf("malformed path: segment has only dots")
	}

	rest := s[leadingDots:]

	bracketStart := strings.IndexByte(rest, '[')

	// Plain segment (no brackets at all).
	if bracketStart < 0 {
		if strings.ContainsAny(rest, "[]'\"") { // If there is no opening bracket, these chars are invalid
			return nil, fmt.Errorf("malformed path: invalid characters in segment")
		}
		return []string{s}, nil // return with leading dots
	}

	// Bracketed segment with leading dots is invalid, e.g., just .['field'] is not allowed
	if leadingDots > 0 && bracketStart == 0 {
		return nil, fmt.Errorf("malformed path: dot before bracket")
	}

	if bracketStart == 0 {
		seg, err := parseBracketContent(rest)
		if err != nil {
			return nil, err
		}
		return []string{seg}, nil
	}

	// "name[...]" form: a plain-name prefix immediately followed by one trailing bracket group, e.g.
	// "credentials[0]". The leading dots (if any) attach to the name prefix, as for a plain segment.
	prefix := rest[:bracketStart]
	if strings.ContainsAny(prefix, "]'\"") {
		return nil, fmt.Errorf("malformed path: invalid characters in segment")
	}
	seg, err := parseBracketContent(rest[bracketStart:])
	if err != nil {
		return nil, err
	}
	return []string{s[:leadingDots] + prefix, seg}, nil
}

// parseBracketContent parses a single bracket group "[...]" (rest must start with '[' and contain exactly
// one closed group — a second bracket group directly appended, e.g. "[0][1]", is rejected as "adjacent
// brackets must be separated by dot", same as adjacent quoted brackets). Accepts a quoted string
// (['a'], ["a"]) or an unquoted non-negative integer index ([0], [12]).
func parseBracketContent(rest string) (string, error) {
	if len(rest) < 3 {
		return "", fmt.Errorf("malformed path: bracket must contain quoted string")
	}
	if !strings.HasSuffix(rest, "]") {
		return "", fmt.Errorf("malformed path: unclosed bracket")
	}

	// Check for adjacent brackets like ['a']['b'] (without dot in between, invalid)
	closeIdx := strings.Index(rest, "]")
	if closeIdx != len(rest)-1 {
		return "", fmt.Errorf("malformed path: adjacent brackets must be separated by dot")
	}

	inner := rest[1 : len(rest)-1] // remove [ and ] at the ends
	if inner == "" {
		return "", fmt.Errorf("malformed path: empty bracket content")
	}

	// An unquoted non-negative integer is a valid bracket segment — an array index, e.g. [0], [12] — kept
	// as the bare digit string, the same representation dotted numeric segments (e.g. "credentials.0.x")
	// already produce. Whether it is ultimately used as an array index or a map key is decided later, by
	// GetNestedField/SetNestedField, from the actual (or, when creating, the intended) container shape.
	if isDigits(inner) && (len(inner) == 1 || inner[0] != '0') {
		return inner, nil
	}

	// A predicate selects an array element by CONTENT rather than position: [?key=value] matches the
	// element whose `key` field equals `value`. This is what makes a path shape-independent — e.g.
	// credentials[?type=password].value addresses the password credential wherever it sits, instead of
	// hard-coding credentials[0] and silently targeting the wrong element if the order changes. Kept
	// verbatim (leading '?') as the segment; GetNestedField/SetNestedField interpret it.
	//
	// Caveat, deliberate: a map key that literally begins with '?' is therefore not addressable, not even
	// via the quoted form, since the quoted form yields its inner content and would be indistinguishable.
	// No such key occurs in a JSON/YAML API body in practice, and the alternative (an out-of-band marker)
	// would leak into every []string segment consumer for no real gain.
	if strings.HasPrefix(inner, "?") {
		k, v, found := strings.Cut(inner[1:], "=")
		if !found || k == "" {
			return "", fmt.Errorf("malformed path: predicate must be [?key=value]")
		}
		_ = v // an empty value is legal: [?foo=] matches elements whose foo is ""
		return inner, nil
	}

	if len(inner) < 2 {
		return "", fmt.Errorf("malformed path: bracket must contain quoted string")
	}

	// At this point, inner should be a quoted string like 'a.b' or "a.b"

	quote := inner[0]
	if quote != '\'' && quote != '"' {
		return "", fmt.Errorf("malformed path: bracket must contain quoted string")
	}
	if inner[len(inner)-1] != quote {
		return "", fmt.Errorf("malformed path: mismatched quotes")
	}

	content := inner[1 : len(inner)-1]
	if content == "" {
		return "", fmt.Errorf("malformed path: empty bracket content")
	}

	return content, nil
}
