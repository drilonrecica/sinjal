package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
	"github.com/drilonrecica/sinjal/web/templates"
)

// CSRF protection (docs/13_AUTH_SECURITY.md "CSRF").
const (
	// CSRFKeyLabel derives the HMAC key from the master key (vault.Key.Derive).
	CSRFKeyLabel = "sinjal csrf v1"
	// CSRFHeader carries the token on htmx requests (hx-headers on <body>).
	CSRFHeader = "X-CSRF-Token"
	// CSRFFormField carries the token in plain HTML forms.
	CSRFFormField = "_csrf"
	// csrfMaxForm caps a url-encoded body the middleware parses for the token.
	csrfMaxForm = 64 << 10
)

// CSRF checks every state-changing request in its route group:
//
//  1. Origin: a browser request must come from Sinjal's own origin
//     (Sec-Fetch-Site, else Origin, compared with the scheme and host
//     resolved under the trusted-proxy rules). A request with neither
//     header is not from a browser and passes this step.
//  2. Synchronizer token, when the request carries a session: the
//     X-CSRF-Token header or the _csrf form field must equal
//     HMAC-SHA256(key, session id).
//
// Anonymous requests (login, setup) only get the origin check. Machine
// endpoints are mounted outside the group and are not checked. The token is
// derived, not stored, and changes whenever the session is rotated.
type CSRF struct {
	key []byte
	log *slog.Logger
}

// NewCSRF returns the CSRF guard. key comes from Derive(CSRFKeyLabel).
func NewCSRF(key []byte, logger *slog.Logger) *CSRF {
	return &CSRF{key: key, log: logging.Sub(logger, "auth")}
}

// token returns the synchronizer token for a session.
func (c *CSRF) token(sessionID string) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

type csrfKey struct{}

// CSRFToken returns the request's CSRF token for rendering forms, or ""
// for an anonymous request. It requires the CSRF middleware.
func CSRFToken(r *http.Request) string {
	t, _ := r.Context().Value(csrfKey{}).(string)
	return t
}

// Middleware must run after LoadSession.
func (c *CSRF) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs, signedIn := SessionFromContext(r.Context())
		var want string
		if signedIn {
			want = c.token(cs.Session.ID)
			r = r.WithContext(context.WithValue(r.Context(), csrfKey{}, want))
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if reason := checkOrigin(r); reason != "" {
			c.reject(w, r, reason)
			return
		}
		if signedIn {
			got := r.Header.Get(CSRFHeader)
			if got == "" && isURLEncodedForm(r) {
				r.Body = http.MaxBytesReader(w, r.Body, csrfMaxForm)
				if err := r.ParseForm(); err != nil {
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				got = r.PostForm.Get(CSRFFormField)
			}
			if got == "" {
				c.reject(w, r, "missing token")
				return
			}
			if !hmac.Equal([]byte(got), []byte(want)) {
				c.reject(w, r, "invalid token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// checkOrigin returns why a request is cross-origin, or "" when it is not.
// Same model as net/http.CrossOriginProtection, but the expected origin is
// resolved under the trusted-proxy rules instead of taken from r.Host.
func checkOrigin(r *http.Request) string {
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "same-origin", "none":
		return ""
	case "":
	default:
		return "Sec-Fetch-Site " + site
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return "" // not a browser: no ambient credentials to abuse
	}
	scheme := "http"
	if proxy.IsHTTPS(r) {
		scheme = "https"
	}
	if !strings.EqualFold(origin, scheme+"://"+proxy.Host(r)) {
		return "foreign Origin"
	}
	return ""
}

func isURLEncodedForm(r *http.Request) bool {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return ct == "application/x-www-form-urlencoded"
}

func (c *CSRF) reject(w http.ResponseWriter, r *http.Request, reason string) {
	c.log.Warn("csrf: request rejected", "reason", reason, "method", r.Method,
		"client_ip", proxy.ClientIP(r).String())
	render(w, r, c.log, http.StatusForbidden, templates.AuthMessage(
		templates.NewPage("Request rejected — Sinjal", "", ""),
		"This form has expired", "Reload the page and try again."))
}
