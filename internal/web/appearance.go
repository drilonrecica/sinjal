package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/web/templates"
)

const accountAppearancePath = "/account/appearance"

// RegisterAppearance mounts the signed-in user's own appearance page. It is
// open to viewers and changes only the caller's row, so no recent
// re-authentication is asked.
func RegisterAppearance(r chi.Router, h *Account) {
	r.Get(accountAppearancePath, h.appearanceForm)
	r.Head(accountAppearancePath, h.appearanceForm)
	r.Post(accountAppearancePath, h.appearanceSave)
}

func (h *Account) appearanceForm(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	p := templates.NewPage("", cs.User.Theme, cs.User.Density) // normalized values
	h.renderAppearance(w, r, http.StatusOK, templates.AppearanceForm{
		Theme: p.Theme, Density: p.Density, Ambient: cs.User.AmbientLight, Saved: r.URL.Query().Get("saved") == "1",
	})
}

func (h *Account) renderAppearance(w http.ResponseWriter, r *http.Request, status int, f templates.AppearanceForm) {
	render(w, r, h.log, status, templates.AccountAppearance(pageFor(r, "Appearance — Sinjal"), f))
}

// appearanceSave stores the user's theme, density and Ambient light. Values
// outside the allowed sets are refused, never stored.
func (h *Account) appearanceSave(w http.ResponseWriter, r *http.Request) {
	cs, _ := SessionFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, loginMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	theme, density := r.PostForm.Get("theme"), r.PostForm.Get("density")
	ambient := r.PostForm.Get("ambient") == "on"
	if !templates.ValidTheme(theme) || !templates.ValidDensity(density) {
		h.renderAppearance(w, r, http.StatusUnprocessableEntity, templates.AppearanceForm{
			Theme: cs.User.Theme, Density: cs.User.Density, Ambient: ambient, Error: "Choose one of the listed themes and densities.",
		})
		return
	}
	if err := auth.SetPreferences(r.Context(), h.db, cs.User.ID, theme, density, ambient, h.now()); err != nil {
		h.log.Error("account: saving the appearance failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, accountAppearancePath+"?saved=1", http.StatusSeeOther)
}
