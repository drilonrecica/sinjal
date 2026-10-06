package monitor

import (
	"strings"
	"testing"
)

func TestParseStatusAccepts(t *testing.T) {
	for _, c := range []struct {
		expr      string
		in, out   []int
		canonical string
	}{
		{"200", []int{200}, []int{199, 201, 0}, "200"},
		{"200-299", []int{200, 250, 299}, []int{199, 300}, "200-299"},
		{"200,204", []int{200, 204}, []int{201, 203, 205}, "200,204"},
		{"200-399", []int{200, 301, 399}, []int{400}, "200-399"},
		{"200-299,301,404", []int{204, 301, 404}, []int{300, 302, 403, 405}, "200-299,301,404"},
		{" 200 , 204 ", []int{200, 204}, []int{202}, "200,204"},
		{"200 - 299", []int{299}, []int{300}, "200-299"},
		{"204,200", []int{200, 204}, nil, "200,204"},
		{"200-250,240-260", []int{200, 255, 260}, []int{261}, "200-260"},
		{"200-202,203", []int{200, 203}, nil, "200-203"},
		{"200-200", []int{200}, []int{201}, "200"},
		{"100,599", []int{100, 599}, []int{99, 600}, "100,599"},
	} {
		s, err := ParseStatus(c.expr)
		if err != nil {
			t.Errorf("ParseStatus(%q): %v", c.expr, err)
			continue
		}
		for _, code := range c.in {
			if !s.Match(code) {
				t.Errorf("%q must match %d", c.expr, code)
			}
		}
		for _, code := range c.out {
			if s.Match(code) {
				t.Errorf("%q must not match %d", c.expr, code)
			}
		}
		if got := s.String(); got != c.canonical {
			t.Errorf("%q canonical = %q, want %q", c.expr, got, c.canonical)
		}
	}
}

func TestParseStatusRejects(t *testing.T) {
	for name, expr := range map[string]string{
		"empty":           "",
		"spaces":          "   ",
		"letters":         "abc",
		"below range":     "99",
		"above range":     "600",
		"zero":            "000",
		"short":           "20",
		"long":            "2000",
		"leading zero":    "0200",
		"sign":            "+200",
		"negative":        "-200",
		"backwards":       "299-200",
		"open range":      "200-",
		"open start":      "-299",
		"double dash":     "200--299",
		"three-part":      "200-250-299",
		"trailing comma":  "200,",
		"leading comma":   ",200",
		"double comma":    "200,,204",
		"range past max":  "200-600",
		"range below min": "99-200",
		"wildcard":        "2xx",
		"semicolon":       "200;204",
		"inner space":     "2 00",
		"tab":             "200,\t204",
		"newline":         "200\n",
		"unicode digits":  "٢٠٠",
		"fullwidth":       "２００",
		"too long":        strings.Repeat("200,", 20) + "200",
		"too many parts":  strings.TrimSuffix(strings.Repeat("200,", 17), ","),
		"hex":             "0x1",
	} {
		if s, err := ParseStatus(expr); err == nil {
			t.Errorf("%s: ParseStatus(%q) = %q, want an error", name, expr, s)
		}
	}
}

func TestParseStatusDefaultAndZeroValue(t *testing.T) {
	d, err := ParseStatus("200-399")
	if err != nil || !d.Match(200) || !d.Match(302) || d.Match(404) || d.Match(500) {
		t.Errorf("default expression misbehaves: %v", err)
	}
	var zero StatusExpr
	if zero.Match(200) || zero.String() != "" {
		t.Error("the zero value must accept nothing")
	}
}

func TestStatusMatchOutOfRange(t *testing.T) {
	s, _ := ParseStatus("100-599")
	for _, c := range []int{-1, 0, 99, 600, 1 << 30, -1 << 30} {
		if s.Match(c) {
			t.Errorf("Match(%d) must be false and must not panic", c)
		}
	}
}

// FuzzParseStatus checks that arbitrary input never panics and that anything
// accepted round-trips through its canonical form with an identical match set.
// Seeds run under plain `go test`; explore with
//
//	go test ./internal/monitor -run='^$' -fuzz=FuzzParseStatus -fuzztime=30s
func FuzzParseStatus(f *testing.F) {
	for _, s := range []string{"200", "200-299", "200,204", "200-399", "200-299,301,404", " 1 ", "", ",", "-", "99-600", "٢٠٠", "200-", "299-200", "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		s, err := ParseStatus(expr)
		if err != nil {
			return
		}
		canon := s.String()
		if canon == "" {
			t.Fatalf("%q was accepted but accepts no code", expr)
		}
		again, err := ParseStatus(canon)
		if err != nil {
			t.Fatalf("canonical form %q of %q does not parse: %v", canon, expr, err)
		}
		if again != s {
			t.Fatalf("%q and its canonical form %q differ", expr, canon)
		}
		for c := -1; c <= MaxStatus+1; c++ {
			if s.Match(c) != again.Match(c) {
				t.Fatalf("Match(%d) differs after round trip of %q", c, expr)
			}
		}
	})
}
