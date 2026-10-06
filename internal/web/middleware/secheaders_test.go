package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContentSecurityPolicyIsStrict(t *testing.T) {
	directives := map[string]string{}
	for _, d := range strings.Split(ContentSecurityPolicy, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
		directives[name] = value
	}
	for name, want := range map[string]string{
		"default-src":     "'none'",
		"script-src":      "'self'",
		"style-src":       "'self'",
		"frame-ancestors": "'none'",
		"form-action":     "'self'",
		"base-uri":        "'none'",
	} {
		if directives[name] != want {
			t.Errorf("%s = %q, want %q", name, directives[name], want)
		}
	}
	for _, banned := range []string{"unsafe-inline", "unsafe-eval", "http:", "https:", "*"} {
		if strings.Contains(ContentSecurityPolicy, banned) {
			t.Errorf("CSP allows %q", banned)
		}
	}
	if strings.Contains(PermissionsPolicy, "publickey-credentials") {
		t.Error("Permissions-Policy must leave WebAuthn at its default (passkeys)")
	}
}

func TestSecurityHeadersCanBeOverridden(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want the handler's override", got)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("CSP missing")
	}
}
