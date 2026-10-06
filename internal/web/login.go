package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/ratelimit"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
	"github.com/drilonrecica/sinjal/web/templates"
)

// Failed password checks per client IP and account (docs/13 "Login
// protection"). The limiter holds a fixed number of keys, so memory stays
// bounded under a spray of logins or addresses.
const (
	loginMaxFailures = 10
	loginWindow      = 15 * time.Minute
	loginTrackedKeys = 4096
	loginMaxBody     = 16 << 10
	loginFailedMsg   = "Incorrect username or password."
)

// Login serves the sign-in form. Its limiter is shared with
// re-authentication, which counts failures per IP and user.
type Login struct {
	auth     *auth.Authenticator
	sessions *auth.Sessions
	limiter  *ratelimit.Limiter
	log      *slog.Logger
	now      func() time.Time
}

// NewLogin returns the login handler.
func NewLogin(a *auth.Authenticator, sessions *auth.Sessions, logger *slog.Logger) *Login {
	return &Login{
		auth:     a,
		sessions: sessions,
		limiter:  ratelimit.New(loginMaxFailures, loginWindow, loginTrackedKeys),
		log:      logging.Sub(logger, "auth"),
		now:      time.Now,
	}
}

// RegisterLogin mounts GET/HEAD/POST /login and the TOTP step POST
// /login/totp in the session group.
func RegisterLogin(r chi.Router, l *Login) {
	r.Get("/login", l.serveForm)
	r.Head("/login", l.serveForm)
	r.Post("/login", l.submit)
	r.Post("/login/totp", l.submitTOTP)
}

func (l *Login) serveForm(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if _, signedIn := SessionFromContext(r.Context()); signedIn {
		http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
		return
	}
	l.render(w, r, http.StatusOK, templates.LoginForm{Next: safeNextOrEmpty(next)})
}

func (l *Login) submit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, loginMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	login, password := r.PostForm.Get("login"), r.PostForm.Get("password")
	form := templates.LoginForm{Login: login, Next: safeNextOrEmpty(r.PostForm.Get("next"))}
	ip := proxy.ClientIP(r).String()
	key := ip + "\x00" + strings.ToLower(strings.TrimSpace(login))
	now := l.now()

	// Checked before hashing, so a blocked client cannot occupy the two
	// Argon2 slots.
	if l.limiter.Blocked(key, now) {
		l.log.Warn("login: rate limited", "client_ip", ip)
		w.Header().Set("Retry-After", "900")
		form.Error = "Too many failed attempts. Wait a few minutes and try again."
		l.render(w, r, http.StatusTooManyRequests, form)
		return
	}

	user, err := l.auth.Login(r.Context(), login, password, ip, now)
	switch {
	case errors.Is(err, auth.ErrTOTPRequired):
		// No session yet: the challenge only carries "password checked" to
		// the code form.
		l.renderTOTP(w, r, http.StatusOK, templates.LoginTOTPForm{Challenge: l.auth.TOTPChallenge(user.ID, now), Next: form.Next})
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		l.limiter.Add(key, now)
		l.log.Info("login failed", "client_ip", ip)
		form.Error = loginFailedMsg
		l.render(w, r, http.StatusUnauthorized, form)
		return
	case err != nil:
		l.log.Error("login: checking credentials failed", "error", err)
		form.Error = "Signing in failed. Check the server log and try again."
		l.render(w, r, http.StatusInternalServerError, form)
		return
	}
	l.limiter.Reset(key)
	if err := l.startSession(w, r, user, now); err != nil {
		form.Error = "Signing in failed. Check the server log and try again."
		l.render(w, r, http.StatusInternalServerError, form)
		return
	}
	http.Redirect(w, r, safeNext(form.Next), http.StatusSeeOther)
}

// totpLimitKey counts wrong login codes per account, from any address:
// guessing codes needs the password first, so this cannot lock out a user
// whose password is not already known, and spreading guesses over many
// addresses does not help. NUL cannot occur in a login key's address part.
func totpLimitKey(userID string) string { return "\x00totp\x00" + userID }

// submitTOTP is the second sign-in step for accounts with TOTP.
func (l *Login) submitTOTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, loginMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	form := templates.LoginTOTPForm{Challenge: r.PostForm.Get("challenge"), Next: safeNextOrEmpty(r.PostForm.Get("next"))}
	ip := proxy.ClientIP(r).String()
	now := l.now()

	userID, err := l.auth.OpenTOTPChallenge(form.Challenge, now)
	if err != nil {
		l.render(w, r, http.StatusUnauthorized, templates.LoginForm{Next: form.Next, Error: "That took too long. Sign in again."})
		return
	}
	key := totpLimitKey(userID)
	if l.limiter.Blocked(key, now) {
		l.log.Warn("login: code attempts rate limited", "user_id", userID, "client_ip", ip)
		w.Header().Set("Retry-After", "900")
		form.Error = "Too many failed attempts. Wait a few minutes and try again."
		l.renderTOTP(w, r, http.StatusTooManyRequests, form)
		return
	}

	user, err := l.auth.LoginTOTP(r.Context(), userID, r.PostForm.Get("code"), ip, now)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		l.limiter.Add(key, now)
		l.log.Info("login failed at the code step", "user_id", userID, "client_ip", ip)
		form.Error = "Incorrect code."
		l.renderTOTP(w, r, http.StatusUnauthorized, form)
		return
	case err != nil:
		l.log.Error("login: checking the code failed", "error", err)
		form.Error = "Signing in failed. Check the server log and try again."
		l.renderTOTP(w, r, http.StatusInternalServerError, form)
		return
	}
	l.limiter.Reset(key)
	l.limiter.Reset(ip + "\x00" + strings.ToLower(user.Login)) // the password step's counter
	if err := l.startSession(w, r, user, now); err != nil {
		form.Error = "Signing in failed. Check the server log and try again."
		l.renderTOTP(w, r, http.StatusInternalServerError, form)
		return
	}
	http.Redirect(w, r, safeNext(form.Next), http.StatusSeeOther)
}

// startSession signs user in: it creates the session and sets its cookie.
// A session from before (for example someone else's on a shared browser)
// is ended rather than kept beside the new one.
func (l *Login) startSession(w http.ResponseWriter, r *http.Request, user auth.User, now time.Time) error {
	ip := proxy.ClientIP(r).String()
	if cs, ok := SessionFromContext(r.Context()); ok {
		if err := l.sessions.Delete(r.Context(), cs.Session.ID); err != nil {
			l.log.Warn("login: deleting the previous session failed", "session_id", cs.Session.ID, "error", err)
		}
	}
	token, sess, err := l.sessions.Create(r.Context(), user.ID, r.UserAgent(), ip, now)
	if err != nil {
		l.log.Error("login: creating the session failed", "user_id", user.ID, "error", err)
		return err
	}
	SetSessionCookie(w, r, token, sess.ExpiresAt)
	l.log.Info("signed in", "user_id", user.ID, "session_id", sess.ID, "client_ip", ip)
	return nil
}

func (l *Login) render(w http.ResponseWriter, r *http.Request, status int, f templates.LoginForm) {
	render(w, r, l.log, status, templates.Login(pageFor(r, "Sign in — Sinjal"), f))
}

func (l *Login) renderTOTP(w http.ResponseWriter, r *http.Request, status int, f templates.LoginTOTPForm) {
	render(w, r, l.log, status, templates.LoginTOTP(pageFor(r, "Enter your code — Sinjal"), f))
}

// safeNextOrEmpty keeps a valid return path for the form's hidden field and
// drops anything else ("/" is the default anyway).
func safeNextOrEmpty(raw string) string {
	if n := safeNext(raw); n != "/" {
		return n
	}
	return ""
}
