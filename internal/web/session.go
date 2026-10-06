package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
)

// Session cookie names (docs/13_AUTH_SECURITY.md "Sessions"). The __Host-
// prefix makes browsers insist on Secure, Path=/ and no Domain, so a
// sibling subdomain cannot plant or override it.
const (
	secureSessionCookie = "__Host-sinjal_session"
	plainSessionCookie  = "sinjal_session"
)

// sessionCookieName picks the name for the request's scheme, resolved under
// the trusted-proxy rules.
func sessionCookieName(r *http.Request) string {
	if proxy.IsHTTPS(r) {
		return secureSessionCookie
	}
	return plainSessionCookie
}

// SetSessionCookie issues the session cookie. Secure is set when the client
// reached Sinjal over HTTPS, directly or through a trusted proxy.
func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(r),
		Value:    token,
		Path:     "/",
		Expires:  expires,
		Secure:   proxy.IsHTTPS(r),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie tells the browser to drop the session cookie.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(r),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   proxy.IsHTTPS(r),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// CurrentSession is the signed-in session of a request.
type CurrentSession struct {
	Session auth.Session
	User    auth.User
}

type sessionKey struct{}

// SessionFromContext returns the session stored by LoadSession.
func SessionFromContext(ctx context.Context) (CurrentSession, bool) {
	cs, ok := ctx.Value(sessionKey{}).(CurrentSession)
	return cs, ok
}

// LoadSession resolves the session cookie and stores the session in the
// request context. It does not enforce anything: requests without a valid
// session continue signed out (RequireAuth arrives in M1-11). A stale
// cookie is cleared; a database error fails the request.
func LoadSession(sessions *auth.Sessions, logger *slog.Logger) func(http.Handler) http.Handler {
	log := logging.Sub(logger, "auth")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(sessionCookieName(r))
			if err != nil || c.Value == "" {
				next.ServeHTTP(w, r)
				return
			}
			sess, user, err := sessions.Lookup(r.Context(), c.Value, time.Now())
			switch {
			case errors.Is(err, auth.ErrNoSession):
				ClearSessionCookie(w, r)
				next.ServeHTTP(w, r)
			case err != nil:
				log.Error("session lookup failed", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			default:
				ctx := context.WithValue(r.Context(), sessionKey{}, CurrentSession{Session: sess, User: user})
				next.ServeHTTP(w, r.WithContext(ctx))
			}
		})
	}
}

// RegisterLogout mounts POST /logout: it deletes the current session, clears
// the cookie and redirects to /login. CSRF protection is added in M1-08.
func RegisterLogout(r chi.Router, sessions *auth.Sessions, logger *slog.Logger) {
	log := logging.Sub(logger, "auth")
	r.With(LoadSession(sessions, logger)).Post("/logout", func(w http.ResponseWriter, req *http.Request) {
		if cs, ok := SessionFromContext(req.Context()); ok {
			if err := sessions.Delete(req.Context(), cs.Session.ID); err != nil {
				log.Error("logout: deleting the session failed", "session_id", cs.Session.ID, "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
		}
		ClearSessionCookie(w, req)
		http.Redirect(w, req, "/login", http.StatusSeeOther)
	})
}
