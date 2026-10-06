package web

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"rsc.io/qr"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
	"github.com/drilonrecica/sinjal/web/templates"
)

const settingsAuthPath = "/settings/authentication"

// SettingsAuth serves Settings → Authentication: the signed-in admin's own
// sign-in methods. Changing one deletes the user's other sessions and
// rotates the current one (docs/13 "Sessions").
type SettingsAuth struct {
	auth     *auth.Authenticator
	db       *db.DB
	passkeys *auth.Passkeys
	sessions *auth.Sessions
	log      *slog.Logger
	now      func() time.Time
}

// NewSettingsAuth returns the Settings → Authentication handler.
func NewSettingsAuth(a *auth.Authenticator, d *db.DB, passkeys *auth.Passkeys, sessions *auth.Sessions, logger *slog.Logger) *SettingsAuth {
	return &SettingsAuth{auth: a, db: d, passkeys: passkeys, sessions: sessions, log: logging.Sub(logger, "auth"), now: time.Now}
}

// RegisterSettingsAuth mounts the pages inside RequireAdmin. Everything that
// shows or changes a credential also sits behind recent (RequireRecentAuth).
func RegisterSettingsAuth(r chi.Router, h *SettingsAuth, recent func(http.Handler) http.Handler) {
	r.Get(settingsAuthPath, h.page)
	r.Head(settingsAuthPath, h.page)
	r.Group(func(r chi.Router) {
		r.Use(recent)
		r.Get(settingsAuthPath+"/totp", h.totpSetup)
		r.Head(settingsAuthPath+"/totp", h.totpSetup)
		r.Post(settingsAuthPath+"/totp", h.totpEnable)
		r.Post(settingsAuthPath+"/totp/disable", h.totpDisable)
		r.Post(settingsAuthPath+"/passkeys/{id}/delete", h.passkeyDelete)
		r.Post(settingsAuthPath+"/viewers", h.viewerCreate)
		r.Post(settingsAuthPath+"/viewers/{id}/disable", h.viewerSetDisabled(true))
		r.Post(settingsAuthPath+"/viewers/{id}/enable", h.viewerSetDisabled(false))
	})
}

func (h *SettingsAuth) page(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, http.StatusOK, templates.ViewerForm{})
}

// renderPage shows the page with form, the state of the create-viewer form
// (its errors and the login to keep).
func (h *SettingsAuth) renderPage(w http.ResponseWriter, r *http.Request, status int, form templates.ViewerForm) {
	cs, _ := SessionFromContext(r.Context())
	enabled, err := h.auth.TOTPEnabled(r.Context(), cs.User.ID)
	if err != nil {
		h.fail(w, "reading TOTP state", err)
		return
	}
	keys, err := h.passkeys.List(r.Context(), cs.User.ID)
	if err != nil {
		h.fail(w, "listing passkeys", err)
		return
	}
	viewers, err := auth.ListViewers(r.Context(), h.db.Reader)
	if err != nil {
		h.fail(w, "listing viewers", err)
		return
	}
	view := templates.SettingsAuthView{TOTPEnabled: enabled, PasskeysUnavailable: h.passkeys.Unavailable(), ViewerForm: form, MinPassword: auth.MinPasswordLen}
	for _, v := range viewers {
		view.Viewers = append(view.Viewers, templates.ViewerView{ID: v.ID, Login: v.Login, Disabled: v.Disabled, Added: v.CreatedAt.Format(time.DateOnly)})
	}
	for _, k := range keys {
		used := "Never used"
		if !k.LastUsedAt.IsZero() {
			used = "Last used " + k.LastUsedAt.Format(time.DateOnly)
		}
		view.Passkeys = append(view.Passkeys, templates.PasskeyView{ID: k.ID, Label: k.Label, Added: k.CreatedAt.Format(time.DateOnly), LastUsed: used})
	}
	render(w, r, h.log, status, templates.SettingsAuth(pageFor(r, "Authentication — Sinjal"), view))
}

// passkeyDelete revokes one of the signed-in admin's passkeys.
func (h *SettingsAuth) passkeyDelete(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	now := h.now()
	err := h.passkeys.Delete(r.Context(), cs.User.ID, chi.URLParam(r, "id"), cs.Session.ID, proxy.ClientIP(r).String(), now)
	switch {
	case errors.Is(err, auth.ErrPasskeyNotFound):
		http.NotFound(w, r)
	case err != nil:
		h.fail(w, "removing a passkey", err)
	default:
		h.log.Info("passkey removed", "user_id", cs.User.ID)
		h.credentialChanged(w, r, now)
	}
}

// totpSetup shows a new secret. Each load makes a new one; nothing is
// stored until totpEnable sees a valid code for it.
func (h *SettingsAuth) totpSetup(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	if enabled, err := h.auth.TOTPEnabled(r.Context(), cs.User.ID); err != nil {
		h.fail(w, "reading TOTP state", err)
		return
	} else if enabled {
		http.Redirect(w, r, settingsAuthPath, http.StatusSeeOther)
		return
	}
	h.renderNewSetup(w, r, http.StatusOK, "")
}

func (h *SettingsAuth) totpEnable(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, loginMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pending := r.PostForm.Get("pending")
	now := h.now()
	err := h.auth.EnableTOTP(r.Context(), cs.User.ID, pending, r.PostForm.Get("code"), cs.Session.ID, proxy.ClientIP(r).String(), now)
	switch {
	case errors.Is(err, auth.ErrInvalidCode):
		en, err := h.auth.TOTPEnrolmentFromPending(cs.User, pending, now)
		if err != nil {
			h.renderNewSetup(w, r, http.StatusUnprocessableEntity, "This setup key has expired. Scan the new QR code and try again.")
			return
		}
		h.renderSetup(w, r, http.StatusUnprocessableEntity, en, "That code is not correct. Check that your phone's clock is right and enter the current code.")
	case errors.Is(err, auth.ErrTOTPExpired):
		h.renderNewSetup(w, r, http.StatusUnprocessableEntity, "This setup key has expired. Scan the new QR code and try again.")
	case errors.Is(err, auth.ErrTOTPEnabled):
		http.Redirect(w, r, settingsAuthPath, http.StatusSeeOther)
	case err != nil:
		h.fail(w, "enabling TOTP", err)
	default:
		h.log.Info("TOTP enabled", "user_id", cs.User.ID)
		h.credentialChanged(w, r, now)
	}
}

func (h *SettingsAuth) totpDisable(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	now := h.now()
	if err := h.auth.DisableTOTP(r.Context(), cs.User.ID, cs.Session.ID, proxy.ClientIP(r).String(), now); err != nil {
		h.fail(w, "disabling TOTP", err)
		return
	}
	h.log.Info("TOTP disabled", "user_id", cs.User.ID)
	h.credentialChanged(w, r, now)
}

// credentialChanged finishes a change: the other sessions are already
// deleted, the current one is rotated here, then back to the page.
func (h *SettingsAuth) credentialChanged(w http.ResponseWriter, r *http.Request, now time.Time) {
	if _, err := rotateSession(w, r, h.sessions, now); err != nil {
		// The change itself is committed and this session stays valid.
		h.log.Error("rotating the session after a credential change failed", "error", err)
	}
	http.Redirect(w, r, settingsAuthPath, http.StatusSeeOther)
}

func (h *SettingsAuth) renderNewSetup(w http.ResponseWriter, r *http.Request, status int, msg string) {
	cs, _ := SessionFromContext(r.Context())
	en, err := h.auth.NewTOTPEnrolment(cs.User, h.now())
	if err != nil {
		h.fail(w, "creating a TOTP secret", err)
		return
	}
	h.renderSetup(w, r, status, en, msg)
}

func (h *SettingsAuth) renderSetup(w http.ResponseWriter, r *http.Request, status int, en auth.TOTPEnrolment, msg string) {
	code, err := qr.Encode(en.URI, qr.M)
	if err != nil {
		h.fail(w, "encoding the QR code", err)
		return
	}
	code.Scale = 4
	render(w, r, h.log, status, templates.TOTPSetup(pageFor(r, "Set up an authenticator app — Sinjal"), templates.TOTPSetupView{
		Secret:  groupSecret(en.Secret),
		URI:     en.URI,
		QR:      "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()),
		Pending: en.Pending,
		Error:   msg,
	}))
}

// fail logs what went wrong (never a secret) and answers 500.
func (h *SettingsAuth) fail(w http.ResponseWriter, what string, err error) {
	h.log.Error("settings: "+what+" failed", "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// groupSecret splits a base32 key into groups of four for reading aloud or
// typing; authenticator apps ignore the spaces.
func groupSecret(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i += 4 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s[i:min(i+4, len(s))])
	}
	return b.String()
}
