package web

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
)

func TestCSRFTokenIsBoundToTheSession(t *testing.T) {
	logger, _ := quietLogger()
	c := NewCSRF(testCSRFKey, logger)
	if a, b := c.token("s1"), c.token("s1"); a != b {
		t.Error("token not deterministic")
	}
	if c.token("s1") == c.token("s2") {
		t.Error("two sessions share a token")
	}
	if NewCSRF([]byte("another key, another token......"), logger).token("s1") == c.token("s1") {
		t.Error("token does not depend on the key")
	}
	if len(c.token("s1")) != 43 {
		t.Errorf("token length %d, want 43 (base64url of 32 bytes)", len(c.token("s1")))
	}
}

// TestCSRFGuardsStateChanges posts to /logout (a real state change: it
// deletes the session) through the production route table.
func TestCSRFGuardsStateChanges(t *testing.T) {
	tests := []struct {
		name   string
		build  func(token string, other string) *http.Request
		wantOK bool
	}{
		{"no token", func(tok, _ string) *http.Request {
			return req("POST", "/logout", url.Values{})
		}, false},
		{"wrong token", func(tok, _ string) *http.Request {
			return req("POST", "/logout", url.Values{CSRFFormField: {"x" + tok[1:]}})
		}, false},
		{"other session's token", func(_, other string) *http.Request {
			return req("POST", "/logout", url.Values{CSRFFormField: {other}})
		}, false},
		{"form field", func(tok, _ string) *http.Request {
			return req("POST", "/logout", url.Values{CSRFFormField: {tok}})
		}, true},
		{"header", func(tok, _ string) *http.Request {
			r := req("POST", "/logout", nil)
			r.Header.Set(CSRFHeader, tok)
			return r
		}, true},
		{"token in query string is ignored", func(tok, _ string) *http.Request {
			return req("POST", "/logout?"+CSRFFormField+"="+tok, nil)
		}, false},
		{"same-origin fetch metadata", func(tok, _ string) *http.Request {
			r := req("POST", "/logout", url.Values{CSRFFormField: {tok}})
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			return r
		}, true},
		{"cross-site with a valid token", func(tok, _ string) *http.Request {
			r := req("POST", "/logout", url.Values{CSRFFormField: {tok}})
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			return r
		}, false},
		{"same-site subdomain with a valid token", func(tok, _ string) *http.Request {
			r := req("POST", "/logout", url.Values{CSRFFormField: {tok}})
			r.Header.Set("Sec-Fetch-Site", "same-site")
			return r
		}, false},
		{"matching Origin", func(tok, _ string) *http.Request {
			r := req("POST", "/logout", url.Values{CSRFFormField: {tok}})
			r.Header.Set("Origin", "http://example.com")
			return r
		}, true},
		{"foreign Origin with a valid token", func(tok, _ string) *http.Request {
			r := req("POST", "/logout", url.Values{CSRFFormField: {tok}})
			r.Header.Set("Origin", "http://evil.example")
			return r
		}, false},
		{"Origin with the wrong scheme", func(tok, _ string) *http.Request {
			r := req("POST", "/logout", url.Values{CSRFFormField: {tok}})
			r.Header.Set("Origin", "https://example.com")
			return r
		}, false},
		{"opaque Origin", func(tok, _ string) *http.Request {
			r := req("POST", "/logout", url.Values{CSRFFormField: {tok}})
			r.Header.Set("Origin", "null")
			return r
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAppEnv(t)
			e.addUser(t, "u1", "admin", "admin", "")
			cookie, sess := e.signIn(t, "u1")
			_, other := e.signIn(t, "u1")

			rec := e.serve(withCookie(tt.build(e.csrf.token(sess.ID), e.csrf.token(other.ID)), cookie))
			_, _, err := e.sessions.Lookup(context.Background(), cookie, time.Now())
			if tt.wantOK {
				if rec.Code != 303 || !errors.Is(err, auth.ErrNoSession) {
					t.Fatalf("status %d, session lookup %v: want 303 and the session gone", rec.Code, err)
				}
				return
			}
			if rec.Code != 403 || err != nil {
				t.Fatalf("status %d, session lookup %v: want 403 and the session kept", rec.Code, err)
			}
			if !strings.Contains(rec.Body.String(), "This form has expired") {
				t.Errorf("403 body = %q", rec.Body.String())
			}
			if logs := e.logs.String(); !strings.Contains(logs, "csrf: request rejected") || strings.Contains(logs, e.csrf.token(sess.ID)) {
				t.Errorf("rejection log missing or contains the token: %s", logs)
			}
		})
	}
}

func TestCSRFAnonymousRequestsGetTheOriginCheckOnly(t *testing.T) {
	e := newAppEnv(t)
	if rec := e.serve(req("POST", "/logout", url.Values{})); rec.Code != 303 {
		t.Errorf("anonymous POST without browser headers = %d, want 303", rec.Code)
	}
	r := req("POST", "/logout", url.Values{})
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if rec := e.serve(r); rec.Code != 403 {
		t.Errorf("anonymous cross-site POST = %d, want 403", rec.Code)
	}
	r = req("POST", "/logout", url.Values{})
	r.Header.Set("Origin", "http://evil.example")
	if rec := e.serve(r); rec.Code != 403 {
		t.Errorf("anonymous foreign-Origin POST = %d, want 403", rec.Code)
	}
}

func TestCSRFOriginBehindTrustedProxy(t *testing.T) {
	e := newAppEnv(t, netip.MustParsePrefix("10.0.0.0/8"))
	build := func(peer string) *http.Request {
		r := req("POST", "/logout", url.Values{})
		r.RemoteAddr = peer
		r.Host = "sinjal:8080" // the proxy's upstream address
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "status.example")
		r.Header.Set("Origin", "https://status.example")
		return r
	}
	if rec := e.serve(build("10.0.0.2:1")); rec.Code != 303 {
		t.Errorf("trusted proxy, public origin = %d, want 303", rec.Code)
	}
	if rec := e.serve(build("192.0.2.1:1")); rec.Code != 403 {
		t.Errorf("untrusted peer claiming the public origin = %d, want 403", rec.Code)
	}
}

func TestCSRFTokenIsRendered(t *testing.T) {
	e := newAppEnv(t)
	e.addUser(t, "u1", "admin", "admin", "")
	cookie, sess := e.signIn(t, "u1")
	tok := e.csrf.token(sess.ID)

	body := e.serve(withCookie(req("GET", "/monitors", nil), cookie)).Body.String()
	for _, want := range []string{
		`<body hx-headers="{&#34;X-CSRF-Token&#34;:&#34;` + tok + `&#34;}">`,
		`<input type="hidden" name="_csrf" value="` + tok + `">`,
		`<form class="signout" method="post" action="/logout">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("signed-in page lacks %s", want)
		}
	}

	body = e.serve(req("GET", "/monitors", nil)).Body.String()
	if strings.Contains(body, "hx-headers") || strings.Contains(body, `name="_csrf"`) {
		t.Error("anonymous page renders a CSRF token")
	}
}
