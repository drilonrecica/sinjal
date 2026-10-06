package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
	"github.com/drilonrecica/sinjal/web/templates"
)

const accountPasswordPath = "/account/password"

// Account serves the signed-in user's own account pages. They are open to
// viewers: each acts only on the caller's own account.
type Account struct {
	db       *db.DB
	sessions *auth.Sessions
	log      *slog.Logger
	now      func() time.Time
}

// NewAccount returns the account handler.
func NewAccount(d *db.DB, sessions *auth.Sessions, logger *slog.Logger) *Account {
	return &Account{db: d, sessions: sessions, log: logging.Sub(logger, "auth"), now: time.Now}
}

// RegisterAccount mounts the password change inside RequireAuth. Both
// methods sit behind recent (RequireRecentAuth): the recent confirmation of
// the current password is what authorizes the change, and asking before the
// form means a typed password is not lost to a redirect.
func RegisterAccount(r chi.Router, h *Account, recent func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(recent)
		r.Get(accountPasswordPath, h.passwordForm)
		r.Head(accountPasswordPath, h.passwordForm)
		r.Post(accountPasswordPath, h.passwordChange)
		r.Post("/account/sessions/sign-out-others", h.signOutOthers)
	})
}

func (h *Account) passwordForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, templates.PasswordForm{Changed: r.URL.Query().Get("changed") == "1", SignedOut: r.URL.Query().Get("signedout") == "1"})
}

func (h *Account) render(w http.ResponseWriter, r *http.Request, status int, f templates.PasswordForm) {
	render(w, r, h.log, status, templates.AccountPassword(pageFor(r, "Your account — Sinjal"), f, auth.MinPasswordLen))
}

// passwordChange stores the new password, deletes the user's other
// sessions and rotates the current one.
func (h *Account) passwordChange(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, loginMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	form := templates.PasswordForm{Errors: map[string]string{}}
	password := r.PostForm.Get("password")
	if err := auth.ValidatePassword(password); err != nil {
		form.Errors["password"] = inputMessage(err)
	} else if password != r.PostForm.Get("confirm") {
		form.Errors["confirm"] = "The passwords do not match."
	}
	if len(form.Errors) > 0 {
		h.render(w, r, http.StatusUnprocessableEntity, form)
		return
	}

	now := h.now()
	if err := auth.ChangePassword(r.Context(), h.db, cs.User.ID, password, cs.Session.ID, proxy.ClientIP(r).String(), now); err != nil {
		h.log.Error("account: changing the password failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.log.Info("password changed", "user_id", cs.User.ID)
	if _, err := rotateSession(w, r, h.sessions, now); err != nil {
		// The change is committed and this session stays valid.
		h.log.Error("rotating the session after a password change failed", "error", err)
	}
	http.Redirect(w, r, accountPasswordPath+"?changed=1", http.StatusSeeOther)
}

// signOutOthers ends every other session of the signed-in user, for a
// forgotten browser or a lost device. The current session stays.
func (h *Account) signOutOthers(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	n, err := auth.SignOutOtherSessions(r.Context(), h.db, cs.User.ID, cs.Session.ID, proxy.ClientIP(r).String(), h.now())
	if err != nil {
		h.log.Error("account: signing out other sessions failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.log.Info("other sessions signed out", "user_id", cs.User.ID, "count", n)
	http.Redirect(w, r, accountPasswordPath+"?signedout=1", http.StatusSeeOther)
}
