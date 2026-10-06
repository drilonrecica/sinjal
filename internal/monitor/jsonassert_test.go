package monitor

import (
	"encoding/json"
	"testing"
)

func TestEvalJSONAssertion(t *testing.T) {
	const body = `{"n":1,"s":"42","t":true,"z":null,"a":[{"k":"v"},2.50],"o":{"x":1},"d":1,"d":2,"big":12345678901234567890}`
	doc, err := DecodeJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path, op, val string
		want          bool
	}{
		{"$.n", OpEquals, `1`, true},
		{"$.n", OpEquals, `1.0`, true},
		{"$.n", OpEquals, `"1"`, false},
		{"$.s", OpEquals, `"42"`, true},
		{"$.s", OpEquals, `42`, false},
		{"$.t", OpEquals, `true`, true},
		{"$.t", OpEquals, `"true"`, false},
		{"$.z", OpEquals, `null`, true},
		{"$.z", OpEquals, `0`, false},
		{"$.z", OpExists, ``, true},
		{"$.a[1]", OpEquals, `2.5`, true},
		{`$.a[0]["k"]`, OpEquals, `"v"`, true},
		{"$.o", OpEquals, `1`, false},
		{"$.a", OpEquals, `"x"`, false},
		{"$.d", OpEquals, `2`, true}, // last duplicate wins
		{"$.big", OpEquals, `12345678901234567890`, true},
		{"$.big", OpEquals, `12345678901234567891`, false},
		{"$.n", OpNotEquals, `2`, true},
		{"$.n", OpNotEquals, `1`, false},
		{"$.s", OpNotEquals, `42`, true},
		{"$.missing", OpNotEquals, `1`, false}, // missing path fails not_equals
		{"$.missing", OpEquals, `1`, false},
		{"$.missing", OpExists, ``, false},
		{"$.missing", OpNotExists, ``, true},
		{"$.n", OpNotExists, ``, false},
		{"$.a[9]", OpExists, ``, false},
		{"$.n.x", OpExists, ``, false},
		{"$.o[0]", OpExists, ``, false},
	}
	for _, c := range cases {
		a := JSONAssertion{Path: c.path, Op: c.op}
		if c.val != "" {
			a.Value = json.RawMessage(c.val)
		}
		got, _, err := EvalJSONAssertion(a, doc)
		if err != nil || got != c.want {
			t.Errorf("%s %s %s = %v, %v; want %v", c.path, c.op, c.val, got, err, c.want)
		}
	}
}

func TestEvalJSONAssertionActual(t *testing.T) {
	doc, _ := DecodeJSON([]byte(`{"a":"x"}`))
	_, actual, _ := EvalJSONAssertion(JSONAssertion{Path: "$.a", Op: OpExists}, doc)
	if actual != `"x"` {
		t.Errorf("actual = %q", actual)
	}
	_, actual, _ = EvalJSONAssertion(JSONAssertion{Path: "$.b", Op: OpExists}, doc)
	if actual != "" {
		t.Errorf("missing actual = %q", actual)
	}
}

func TestDecodeJSONErrors(t *testing.T) {
	for _, b := range []string{``, `{`, `{"a":1} x`, `{"a":1}{"b":2}`, `nope`} {
		if _, err := DecodeJSON([]byte(b)); err == nil {
			t.Errorf("DecodeJSON(%q) succeeded", b)
		}
	}
	if _, err := DecodeJSON([]byte(` {"a":1} `)); err != nil {
		t.Error(err)
	}
}
