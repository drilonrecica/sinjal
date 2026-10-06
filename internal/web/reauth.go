package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/ratelimit"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
	"github.com/drilonrecica/sinjal/web/templates"
)

// RequireRecentAuth guards sensitive actions (docs/13 "Re-authentication
// actions"): the session must have proved a credential within
// auth.ReauthWindow, otherwise the user is sent to /reauth. Mount it inside
// RequireAuth. now is time.Now outside tests.
//
// A page request returns to itself afterwards. A form POST cannot be
// replayed, so it returns to the page the form was on (a same-origin
// Referer), where the user submits again.
func RequireRecentAuth(logger *slog.Logger, now func() time.Time) func(http.Handler) http.Handler {
	log := logging.Sub(logger, "auth")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cs, ok := SessionFromContext(r.Context())
			if ok && cs.Session.RecentlyAuthenticated(now()) {
				next.ServeHTTP(w, r)
				return
			}
			if !ok { // mounted outside RequireAuth by mistake: fail closed
				log.Error("RequireRecentAuth without a session", "method", r.Method)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			back := r.URL.RequestURI()
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				back = sameOriginReferer(r)
			}
			target := "/reauth"
			if n := safeNextOrEmpty(back); n != "" {
				target += "?next=" + url.QueryEscape(n)
			}
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", target)
				w.WriteHeader(http.StatusForbidden)
				return
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
		})
	}
}

// sameOriginReferer returns the path and query of the Referer when it is
// Sinjal's own origin, otherwise "".
func sameOriginReferer(r *http.Request) string {
	u, err := url.Parse(r.Referer())
	if err != nil || !strings.EqualFold(u.Scheme+"://"+u.Host, requestOrigin(r)) {
		return ""
	}
	return u.RequestURI()
}

// Reauth serves /reauth. It shares the login limiter: failures (password,
// TOTP code or passkey) count per client IP and user.
type Reauth struct {
	auth     *auth.Authenticator
	passkeys *auth.Passkeys
	sessions *auth.Sessions
	limiter  *ratelimit.Limiter
	log      *slog.Logger
	now      func() time.Time
}

// NewReauth returns the re-authentication handler.
func NewReauth(a *auth.Authenticator, passkeys *auth.Passkeys, sessions *auth.Sessions, limiter *ratelimit.Limiter, logger *slog.Logger) *Reauth {
	return &Reauth{auth: a, passkeys: passkeys, sessions: sessions, limiter: limiter, log: logging.Sub(logger, "auth"), now: time.Now}
}

// RegisterReauth mounts GET/HEAD/POST /reauth inside RequireAuth.
func RegisterReauth(r chi.Router, h *Reauth) {
	r.Get("/reauth", h.serveForm)
	r.Head("/reauth", h.serveForm)
	r.Post("/reauth", h.submit)
}

func (h *Reauth) serveForm(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	form := templates.ReauthForm{Login: cs.User.Login, Next: safeNextOrEmpty(r.URL.Query().Get("next"))}
	if !h.loadFactors(w, r, &form) {
		return
	}
	h.render(w, r, http.StatusOK, form)
}

// loadFactors fills in which factors the form asks for. It answers 500 and
// reports false when that cannot be read.
func (h *Reauth) loadFactors(w http.ResponseWriter, r *http.Request, form *templates.ReauthForm) bool {
	cs, _ := SessionFromContext(r.Context())
	totp, err := h.auth.TOTPEnabled(r.Context(), cs.User.ID)
	if err != nil {
		h.log.Error("reauth: reading the account's factors failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return false
	}
	form.TOTP = totp
	form.Passkey = h.passkeys.OfferedTo(r.Context(), cs.User.ID)
	return true
}

func (h *Reauth) submit(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, loginMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	form := templates.ReauthForm{Login: cs.User.Login, Next: safeNextOrEmpty(r.PostForm.Get("next"))}
	if !h.loadFactors(w, r, &form) {
		return
	}
	ip := proxy.ClientIP(r).String()
	key := reauthLimitKey(ip, cs.User.ID)
	now := h.now()

	if h.limiter.Blocked(key, now) {
		h.log.Warn("reauth: rate limited", "user_id", cs.User.ID, "client_ip", ip)
		w.Header().Set("Retry-After", "900")
		form.Error = "Too many failed attempts. Wait a few minutes and try again."
		h.render(w, r, http.StatusTooManyRequests, form)
		return
	}
	err := h.auth.Reauthenticate(r.Context(), cs.User.ID, r.PostForm.Get("password"), r.PostForm.Get("code"), ip, now)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		h.limiter.Add(key, now)
		h.log.Info("reauth failed", "user_id", cs.User.ID, "client_ip", ip)
		form.Error = "Incorrect password."
		if form.TOTP {
			form.Error = "Incorrect password or code."
		}
		h.render(w, r, http.StatusUnauthorized, form)
		return
	case err != nil:
		h.log.Error("reauth: checking the password failed", "error", err)
		form.Error = "Confirming failed. Check the server log and try again."
		h.render(w, r, http.StatusInternalServerError, form)
		return
	}
	h.limiter.Reset(key)

	// A new token marks the proof; the old one (and its CSRF token) stops
	// working.
	sess, err := rotateSession(w, r, h.sessions, now)
	if err != nil {
		h.log.Error("reauth: rotating the session failed", "session_id", cs.Session.ID, "error", err)
		form.Error = "Confirming failed. Check the server log and try again."
		h.render(w, r, http.StatusInternalServerError, form)
		return
	}
	h.log.Info("reauthenticated", "user_id", cs.User.ID, "session_id", sess.ID)
	http.Redirect(w, r, safeNext(form.Next), http.StatusSeeOther)
}

// reauthLimitKey counts failed re-authentications (password, code or
// passkey) per client address and user. Logins cannot contain control
// characters, so it never collides with a login key (ip NUL login).
func reauthLimitKey(ip, userID string) string { return ip + "\x00\x00" + userID }

func (h *Reauth) render(w http.ResponseWriter, r *http.Request, status int, f templates.ReauthForm) {
	render(w, r, h.log, status, templates.Reauth(pageFor(r, "Confirm it's you — Sinjal"), f))
}
