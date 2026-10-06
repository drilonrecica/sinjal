package web

import "testing"

func TestSafeNext(t *testing.T) {
	for raw, want := range map[string]string{
		"":                          "/",
		"/":                         "/",
		"/monitors":                 "/monitors",
		"/monitors/1?tab=history#x": "/monitors/1?tab=history#x",
		"//evil.example":            "/",
		"//evil.example/path":       "/",
		`/\evil.example`:            "/",
		`/\/evil.example`:           "/",
		`/a\b`:                      "/",
		"https://evil.example":      "/",
		"http:/evil.example":        "/",
		"javascript:alert(1)":       "/",
		"monitors":                  "/",
		"/\t/evil.example":          "/",
		"/\n/evil.example":          "/",
		"/%2F%2Fevil.example":       "/%2F%2Fevil.example", // stays a local path
		"/ok\x7f":                   "/",
	} {
		if got := safeNext(raw); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", raw, got, want)
		}
	}
}
