package web

import (
	"net/url"
	"strings"
)

// safeNext returns raw when it is a same-origin path (for "return to after
// login/re-authentication"), otherwise "/". It rejects absolute URLs,
// scheme-relative "//host" and the "/\host" form that browsers treat like
// it, and anything with control characters.
func safeNext(raw string) string {
	if raw == "" || len(raw) > 2048 || !strings.HasPrefix(raw, "/") ||
		strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, `/\`) {
		return "/"
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return "/"
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return raw
}
