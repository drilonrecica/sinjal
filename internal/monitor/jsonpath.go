package monitor

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSON path limits (decision P0-07).
const (
	maxJSONPathLen      = 256
	maxJSONPathSegments = 32
)

// PathSegment is one step of a JSON path: an object key, or an array index
// when IsIndex is set.
type PathSegment struct {
	Key     string
	Index   int
	IsIndex bool
}

// JSONPath is a parsed path such as $.data.items[0].state.
type JSONPath []PathSegment

// ParseJSONPath parses the strict subset of docs/06 (decision P0-07):
//
//	path    = "$" segment*
//	segment = "." name            ; name = [A-Za-z_][A-Za-z0-9_]*
//	        | "[" json-string "]" ; any key, JSON string escaping
//	        | "[" index "]"       ; index = 0 | [1-9][0-9]*
//
// Wildcards, filters, slices, negative indices, recursive descent and
// functions are errors. A path is at most 256 characters and 32 segments.
func ParseJSONPath(s string) (JSONPath, error) {
	if len(s) > maxJSONPathLen {
		return nil, fmt.Errorf("path is longer than %d characters", maxJSONPathLen)
	}
	if !utf8.ValidString(s) {
		return nil, fmt.Errorf("path is not valid UTF-8")
	}
	if !strings.HasPrefix(s, "$") {
		return nil, fmt.Errorf("path must start with $")
	}
	var p JSONPath
	for i := 1; i < len(s); {
		if len(p) == maxJSONPathSegments {
			return nil, fmt.Errorf("path has more than %d segments", maxJSONPathSegments)
		}
		switch s[i] {
		case '.':
			j := i + 1
			for j < len(s) && isNameByte(s[j], j == i+1) {
				j++
			}
			if j == i+1 {
				return nil, fmt.Errorf("expected a name after . at position %d", i+1)
			}
			p = append(p, PathSegment{Key: s[i+1 : j]})
			i = j
		case '[':
			seg, n, err := parseBracket(s[i:])
			if err != nil {
				return nil, fmt.Errorf("%v at position %d", err, i+1)
			}
			p = append(p, seg)
			i += n
		default:
			return nil, fmt.Errorf("unexpected %q at position %d", s[i], i+1)
		}
	}
	return p, nil
}

func isNameByte(c byte, first bool) bool {
	switch {
	case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return !first
	}
	return false
}

// parseBracket parses "[...]" at the start of s and returns the segment and
// the number of bytes consumed.
func parseBracket(s string) (PathSegment, int, error) {
	if len(s) > 1 && s[1] == '"' {
		// Find the closing quote, skipping escaped characters.
		j := 2
		for j < len(s) && s[j] != '"' {
			if s[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(s) || j+1 >= len(s) || s[j+1] != ']' {
			return PathSegment{}, 0, fmt.Errorf("unterminated quoted key")
		}
		var key string
		if err := json.Unmarshal([]byte(s[1:j+1]), &key); err != nil {
			return PathSegment{}, 0, fmt.Errorf("invalid quoted key")
		}
		return PathSegment{Key: key}, j + 2, nil
	}
	end := strings.IndexByte(s, ']')
	if end < 0 {
		return PathSegment{}, 0, fmt.Errorf("missing ]")
	}
	digits := s[1:end]
	if digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return PathSegment{}, 0, fmt.Errorf("invalid index %q", digits)
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return PathSegment{}, 0, fmt.Errorf("invalid index %q", digits)
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return PathSegment{}, 0, fmt.Errorf("index %q is too large", digits)
	}
	return PathSegment{Index: n, IsIndex: true}, end + 1, nil
}
