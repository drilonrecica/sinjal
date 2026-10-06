package web

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/web/templates"
)

// Viewer management is part of Settings → Authentication and admin-only.
// There are no invitations: the admin picks the login and a password and
// hands them over. Viewers never see secrets; they cannot reach this page.

func (h *SettingsAuth) viewerCreate(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, loginMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	form := templates.ViewerForm{Login: r.PostForm.Get("login"), Errors: map[string]string{}}
	password, confirm := r.PostForm.Get("password"), r.PostForm.Get("confirm")
	if login, err := auth.NormalizeLogin(form.Login); err != nil {
		form.Errors["login"] = inputMessage(err)
	} else {
		form.Login = login
	}
	if err := auth.ValidatePassword(password); err != nil {
		form.Errors["password"] = inputMessage(err)
	} else if password != confirm {
		form.Errors["confirm"] = "The passwords do not match."
	}
	if len(form.Errors) > 0 {
		h.renderPage(w, r, http.StatusUnprocessableEntity, form)
		return
	}

	id, err := auth.CreateViewer(r.Context(), h.db, cs.User.ID, form.Login, password, h.now())
	var ie *auth.InputError
	switch {
	case errors.As(err, &ie):
		form.Errors[ie.Field] = ie.Message
		h.renderPage(w, r, http.StatusUnprocessableEntity, form)
	case err != nil:
		h.fail(w, "creating a viewer", err)
	default:
		h.log.Info("viewer created", "user_id", cs.User.ID, "viewer_id", id)
		http.Redirect(w, r, settingsAuthPath, http.StatusSeeOther)
	}
}

func (h *SettingsAuth) viewerSetDisabled(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cs, _ := SessionFromContext(r.Context())
		id := chi.URLParam(r, "id")
		err := auth.SetViewerDisabled(r.Context(), h.db, cs.User.ID, id, disabled, h.now())
		switch {
		case errors.Is(err, auth.ErrUserNotFound):
			http.NotFound(w, r)
		case err != nil:
			h.fail(w, "changing a viewer", err)
		default:
			h.log.Info("viewer changed", "user_id", cs.User.ID, "viewer_id", id, "disabled", disabled)
			http.Redirect(w, r, settingsAuthPath, http.StatusSeeOther)
		}
	}
}
