package monitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Header is one non-secret request header, stored in headers_json as
// [{"name": "...", "value": "..."}].
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// JSON assertion operators (docs/06).
const (
	OpEquals    = "equals"
	OpNotEquals = "not_equals"
	OpExists    = "exists"
	OpNotExists = "not_exists"
)

// JSONAssertion is one entry of json_assertions_json. Value is a JSON
// scalar for equals/not_equals and absent for exists/not_exists.
type JSONAssertion struct {
	Path  string          `json:"path"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Names of HTTP monitor secrets in monitor_secrets. Values never appear in
// headers_json or any other plain column.
const (
	SecretBasicAuth    = "auth.basic"  // "user:password"
	SecretBearerToken  = "auth.bearer" // token without the "Bearer " prefix
	SecretHeaderPrefix = "header."     // "header.X-Api-Key": a custom secret header
)

// ParseHeaders decodes headers_json. Empty text is no headers.
func ParseHeaders(text string) ([]Header, error) {
	if text == "" {
		return nil, nil
	}
	var hs []Header
	if err := decodeStrict(text, &hs); err != nil {
		return nil, fmt.Errorf("headers are not a JSON list of name/value pairs")
	}
	return hs, nil
}

// ParseJSONAssertions decodes json_assertions_json. Empty text is no
// assertions. It checks the shape only; ValidateJSONAssertion checks each.
func ParseJSONAssertions(text string) ([]JSONAssertion, error) {
	if text == "" {
		return nil, nil
	}
	var as []JSONAssertion
	if err := decodeStrict(text, &as); err != nil {
		return nil, fmt.Errorf("JSON assertions are not a JSON list of path/op/value entries")
	}
	return as, nil
}

// ValidateJSONAssertion checks one assertion: a valid path, a known
// operator, and a scalar value exactly when the operator compares
// (docs/38).
func ValidateJSONAssertion(a JSONAssertion) error {
	if _, err := ParseJSONPath(a.Path); err != nil {
		return err
	}
	hasValue := len(a.Value) > 0
	switch a.Op {
	case OpExists, OpNotExists:
		if hasValue {
			return fmt.Errorf("%s takes no expected value", a.Op)
		}
	case OpEquals, OpNotEquals:
		if !hasValue {
			return fmt.Errorf("%s needs an expected value", a.Op)
		}
		if v := bytes.TrimSpace(a.Value); len(v) == 0 || v[0] == '{' || v[0] == '[' {
			return fmt.Errorf("the expected value must be a string, number, boolean or null")
		}
	default:
		return fmt.Errorf("unknown operator %q", a.Op)
	}
	return nil
}

// IsSecretName reports whether name follows the secret naming convention
// above. Header names are checked with ValidHeaderName.
func IsSecretName(name string) bool {
	if name == SecretBasicAuth || name == SecretBearerToken {
		return true
	}
	h, ok := strings.CutPrefix(name, SecretHeaderPrefix)
	return ok && ValidHeaderName(h)
}

// ValidHeaderName reports whether s is an RFC 9110 field name (a token).
func ValidHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return false
		}
	}
	return true
}

// ValidHeaderValue reports whether s can be sent as a field value: no
// control characters other than horizontal tab.
func ValidHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

func decodeStrict(text string, v any) error {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data")
	}
	return nil
}
