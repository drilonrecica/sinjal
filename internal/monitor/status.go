// Package monitor holds monitor semantics (docs/06_MONITORING_ENGINE.md):
// what a check result means. This file is the HTTP status expression.
package monitor

import (
	"fmt"
	"strconv"
	"strings"
)

// Status expression limits. Codes are the three-digit HTTP range.
const (
	MinStatus        = 100
	MaxStatus        = 599
	maxStatusExprLen = 64
	maxStatusParts   = 16
)

// StatusExpr is a parsed set of accepted HTTP status codes.
type StatusExpr struct {
	accept [MaxStatus + 1]bool
}

// ParseStatus parses "200", "200-299", "200,204" or a mixed list such as
// "200-299,301,404". Parts are separated by commas; spaces around a part are
// ignored. It is strict: codes are exactly three digits in 100-599, a range
// runs low to high, and empty parts, signs and stray characters are errors
// (docs/38 "status expression parsable").
func ParseStatus(expr string) (StatusExpr, error) {
	var s StatusExpr
	if len(expr) > maxStatusExprLen {
		return s, fmt.Errorf("status expression is longer than %d characters", maxStatusExprLen)
	}
	if strings.TrimSpace(expr) == "" {
		return s, fmt.Errorf("status expression is empty")
	}
	parts := strings.Split(expr, ",")
	if len(parts) > maxStatusParts {
		return s, fmt.Errorf("status expression has more than %d parts", maxStatusParts)
	}
	for _, part := range parts {
		part = strings.Trim(part, " ")
		if part == "" {
			return s, fmt.Errorf("status expression has an empty part")
		}
		lo, hi, isRange := strings.Cut(part, "-")
		first, err := parseCode(lo)
		if err != nil {
			return StatusExpr{}, err
		}
		last := first
		if isRange {
			if last, err = parseCode(hi); err != nil {
				return StatusExpr{}, err
			}
			if last < first {
				return StatusExpr{}, fmt.Errorf("range %q runs backwards", part)
			}
		}
		for c := first; c <= last; c++ {
			s.accept[c] = true
		}
	}
	return s, nil
}

// parseCode accepts exactly three ASCII digits within 100-599. Spaces around
// the dash of a range are tolerated.
func parseCode(s string) (int, error) {
	s = strings.Trim(s, " ")
	if len(s) != 3 {
		return 0, fmt.Errorf("%q is not a three-digit status code", s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("%q is not a three-digit status code", s)
		}
	}
	n, _ := strconv.Atoi(s)
	if n < MinStatus || n > MaxStatus {
		return 0, fmt.Errorf("status code %d is outside %d-%d", n, MinStatus, MaxStatus)
	}
	return n, nil
}

// Match reports whether code is accepted. Codes outside 100-599 never match.
func (s StatusExpr) Match(code int) bool {
	return code >= MinStatus && code <= MaxStatus && s.accept[code]
}

// String is the canonical form: sorted, merged runs, e.g. "200-299,404".
// Parsing it yields an equal expression. The zero value prints "".
func (s StatusExpr) String() string {
	var b strings.Builder
	for c := MinStatus; c <= MaxStatus; c++ {
		if !s.accept[c] {
			continue
		}
		end := c
		for end+1 <= MaxStatus && s.accept[end+1] {
			end++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(c))
		if end > c {
			b.WriteByte('-')
			b.WriteString(strconv.Itoa(end))
		}
		c = end
	}
	return b.String()
}
