package monitor

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func key(k string) PathSegment { return PathSegment{Key: k} }
func idx(i int) PathSegment    { return PathSegment{Index: i, IsIndex: true} }

func TestParseJSONPathAccepts(t *testing.T) {
	for _, c := range []struct {
		path string
		want JSONPath
	}{
		{"$", nil},
		{"$.status", JSONPath{key("status")}},
		{"$.data.items[0].state", JSONPath{key("data"), key("items"), idx(0), key("state")}},
		{`$["x-version"]`, JSONPath{key("x-version")}},
		{"$[2]", JSONPath{idx(2)}},
		{"$[10][0]", JSONPath{idx(10), idx(0)}},
		{"$._a1.B_2", JSONPath{key("_a1"), key("B_2")}},
		{`$["a.b"]["]"]["\"q\""]`, JSONPath{key("a.b"), key("]"), key(`"q"`)}},
		{`$["é"]["sp ace"][""]`, JSONPath{key("é"), key("sp ace"), key("")}},
		{"$" + strings.Repeat(".a", 32), JSONPath(slicesRepeat(key("a"), 32))},
	} {
		got, err := ParseJSONPath(c.path)
		if err != nil {
			t.Errorf("ParseJSONPath(%q): %v", c.path, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseJSONPath(%q) = %+v, want %+v", c.path, got, c.want)
		}
	}
}

func slicesRepeat(s PathSegment, n int) []PathSegment {
	out := make([]PathSegment, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func TestParseJSONPathRejects(t *testing.T) {
	for _, p := range []string{
		"", "status", ".status", "$.", "$..a", "$.*", "$[*]", "$.1a", "$.a-b",
		"$[-1]", "$[01]", "$[]", "$[1:2]", "$[?(@.a)]", "$.a()", "$ .a", "$.a ",
		`$["a"`, `$["a]`, `$['a']`, `$["a\x"]`, `$["\"]`, "$[a]", "$[1", "$[ 1]",
		"$[99999999999999999999]", "$.é", "$.a\xff",
		"$" + strings.Repeat(".a", 33),
		"$." + strings.Repeat("a", 256),
	} {
		if got, err := ParseJSONPath(p); err == nil {
			t.Errorf("ParseJSONPath(%q) = %+v, want an error", p, got)
		}
	}
}

func TestValidateJSONAssertion(t *testing.T) {
	ok := []JSONAssertion{
		{Path: "$.a", Op: OpExists},
		{Path: "$.a", Op: OpNotExists},
		{Path: "$.a", Op: OpEquals, Value: json.RawMessage(`"up"`)},
		{Path: "$.a", Op: OpEquals, Value: json.RawMessage(`1.5`)},
		{Path: "$.a", Op: OpNotEquals, Value: json.RawMessage(`true`)},
		{Path: "$.a", Op: OpEquals, Value: json.RawMessage(`null`)},
	}
	for _, a := range ok {
		if err := ValidateJSONAssertion(a); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	bad := []JSONAssertion{
		{Path: "a", Op: OpExists},
		{Path: "$.a", Op: "contains", Value: json.RawMessage(`"x"`)},
		{Path: "$.a", Op: OpExists, Value: json.RawMessage(`1`)},
		{Path: "$.a", Op: OpEquals},
		{Path: "$.a", Op: OpEquals, Value: json.RawMessage(`{"a":1}`)},
		{Path: "$.a", Op: OpNotEquals, Value: json.RawMessage(`[1]`)},
	}
	for _, a := range bad {
		if err := ValidateJSONAssertion(a); err == nil {
			t.Errorf("%+v must be rejected", a)
		}
	}
}

func TestParseJSONAssertionsShape(t *testing.T) {
	as, err := ParseJSONAssertions(`[{"path":"$.a","op":"equals","value":null}]`)
	if err != nil || len(as) != 1 || string(as[0].Value) != "null" {
		t.Fatalf("got %+v, %v (a null expected value must be kept)", as, err)
	}
	for _, s := range []string{`{}`, `[{"path":"$","op":"exists","extra":1}]`, `[] []`, `[`} {
		if _, err := ParseJSONAssertions(s); err == nil {
			t.Errorf("%q must be rejected", s)
		}
	}
}

func TestHeaderAndSecretNames(t *testing.T) {
	for _, n := range []string{"X-Api-Key", "accept", "a!#$%&'*+-.^_`|~1"} {
		if !ValidHeaderName(n) {
			t.Errorf("%q is a valid header name", n)
		}
	}
	for _, n := range []string{"", "X Api", "X:Y", "é", "a\n"} {
		if ValidHeaderName(n) {
			t.Errorf("%q is not a valid header name", n)
		}
	}
	if !ValidHeaderValue("a\tb é") || ValidHeaderValue("a\r\nb") || ValidHeaderValue("a\x00") || ValidHeaderValue("\x7f") {
		t.Error("header value check is wrong")
	}
	for n, want := range map[string]bool{
		"auth.basic": true, "auth.bearer": true, "header.X-Api-Key": true,
		"auth": false, "header.": false, "header.X Y": false, "auth.digest": false,
	} {
		if IsSecretName(n) != want {
			t.Errorf("IsSecretName(%q) = %v", n, !want)
		}
	}
}

// FuzzParseJSONPath: no panics, accepted paths respect the limits, and the
// parsed segments re-encoded in bracket form parse to the same path.
func FuzzParseJSONPath(f *testing.F) {
	for _, s := range []string{"$", "$.a.b[0]", `$["x-y"]`, "$[2]", "$..a", `$["\u0000"]`, "$[01]", `$["a\"]`, "$.a[", "$\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParseJSONPath(s)
		if err != nil {
			return
		}
		if len(s) > maxJSONPathLen || len(p) > maxJSONPathSegments {
			t.Fatalf("%q exceeds the limits but was accepted", s)
		}
		var b strings.Builder
		b.WriteByte('$')
		for _, seg := range p {
			if seg.IsIndex {
				b.WriteString("[" + strconv.Itoa(seg.Index) + "]")
				continue
			}
			q, _ := json.Marshal(seg.Key)
			b.WriteString("[" + string(q) + "]")
		}
		if b.Len() > maxJSONPathLen {
			return
		}
		again, err := ParseJSONPath(b.String())
		if err != nil {
			t.Fatalf("bracket form %q of %q does not parse: %v", b.String(), s, err)
		}
		if !reflect.DeepEqual(again, p) {
			t.Fatalf("%q and its bracket form %q differ: %+v vs %+v", s, b.String(), p, again)
		}
	})
}
