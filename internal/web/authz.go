package web

import (
	"log/slog"
	"net/http"
	"net/url"

	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/web/templates"
)

// isHTMX reports an htmx request; it gets HX-Redirect instead of a 303,
// which htmx would follow inside the swap target.
func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// RequireAuth lets only signed-in users (admin or viewer) through. It needs
// LoadSession before it. A page request is sent to /login, which returns
// to the page afterwards; anything else gets 401, since a POST cannot be
// replayed after signing in.
func RequireAuth(logger *slog.Logger) func(http.Handler) http.Handler {
	log := logging.Sub(logger, "auth")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := SessionFromContext(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			target := "/login"
			if rel := r.URL.RequestURI(); rel != "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				target += "?next=" + url.QueryEscape(rel)
			}
			switch {
			case isHTMX(r):
				w.Header().Set("HX-Redirect", target)
				w.WriteHeader(http.StatusUnauthorized)
			case r.Method == http.MethodGet || r.Method == http.MethodHead:
				http.Redirect(w, r, target, http.StatusSeeOther)
			default:
				render(w, r, log, http.StatusUnauthorized, templates.AuthMessage(
					templates.NewPage("Sign in required — Sinjal", "", ""),
					"Sign in required", "Your session has ended. Sign in and try again."))
			}
		})
	}
}

// RequireAdmin lets only admins through; viewers get 403. Every
// state-changing app route is mounted behind it (docs/13: viewers cannot
// mutate). Without a session it answers like RequireAuth.
func RequireAdmin(logger *slog.Logger) func(http.Handler) http.Handler {
	log := logging.Sub(logger, "auth")
	requireAuth := RequireAuth(logger)
	return func(next http.Handler) http.Handler {
		admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cs, _ := SessionFromContext(r.Context())
			if cs.User.Role != "admin" {
				log.Warn("authorization: admin required", "user_id", cs.User.ID, "method", r.Method)
				render(w, r, log, http.StatusForbidden, templates.AuthMessage(pageFor(r, "Not allowed — Sinjal"),
					"Not allowed", "Your account can view Sinjal but not change it."))
				return
			}
			next.ServeHTTP(w, r)
		})
		return requireAuth(admin)
	}
}
