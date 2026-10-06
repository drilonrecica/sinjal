package monitor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// Resolve walks a document decoded by encoding/json and returns the value at
// the path. A null value resolves; a missing key, an out-of-range index or a
// step through a scalar does not.
func (p JSONPath) Resolve(doc any) (any, bool) {
	cur := doc
	for _, seg := range p {
		if seg.IsIndex {
			arr, ok := cur.([]any)
			if !ok || seg.Index < 0 || seg.Index >= len(arr) {
				return nil, false
			}
			cur = arr[seg.Index]
			continue
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[seg.Key]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// DecodeJSON decodes body with numbers kept exact (json.Number). Duplicate
// keys resolve to the last one; trailing data is an error.
func DecodeJSON(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errTrailingJSON
	}
	return doc, nil
}

var errTrailingJSON = errors.New("unexpected data after the JSON value")

// EvalJSONAssertion checks one assertion against a decoded document (P0-07).
// actual describes what was found, as JSON text, or "" when the path does
// not resolve.
func EvalJSONAssertion(a JSONAssertion, doc any) (ok bool, actual string, err error) {
	path, err := ParseJSONPath(a.Path)
	if err != nil {
		return false, "", err
	}
	got, found := path.Resolve(doc)
	if found {
		b, _ := json.Marshal(got)
		actual = string(b)
	}
	switch a.Op {
	case OpExists:
		return found, actual, nil
	case OpNotExists:
		return !found, actual, nil
	case OpEquals, OpNotEquals:
		if !found {
			return false, actual, nil // not_equals needs the path to exist too
		}
		want, err := DecodeJSON(a.Value)
		if err != nil {
			return false, actual, err
		}
		eq := jsonEqual(got, want)
		return eq == (a.Op == OpEquals), actual, nil
	}
	return false, actual, fmt.Errorf("unknown operator %q", a.Op)
}

// jsonEqual is type-aware: numbers compare by value, other scalars by type
// and value. Objects and arrays never equal a scalar operand.
func jsonEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		rx, ok1 := new(big.Rat).SetString(x.String())
		ry, ok2 := new(big.Rat).SetString(y.String())
		return ok1 && ok2 && rx.Cmp(ry) == 0
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	}
	return false
}
