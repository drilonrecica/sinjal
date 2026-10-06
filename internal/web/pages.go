package web

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/web/templates"
)

// pageFor builds the Page for a request: the signed-in user's theme and
// density, and the CSRF token for forms.
func pageFor(r *http.Request, title string) templates.Page {
	var theme, density string
	var admin bool
	if cs, ok := SessionFromContext(r.Context()); ok {
		theme, density, admin = cs.User.Theme, cs.User.Density, cs.User.Role == "admin"
	}
	page := templates.NewPage(title, theme, density)
	page.Admin = admin
	page.CSRFToken = CSRFToken(r)
	return page
}

// RegisterPages mounts one placeholder page per primary navigation section.
// Real section pages replace these in their own milestones.
func RegisterPages(r chi.Router, logger *slog.Logger) {
	log := logging.Sub(logger, "http")
	for _, s := range templates.Sections {
		h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			page := pageFor(req, s.Label+" — Sinjal")
			render(w, req, log, http.StatusOK, templates.PlaceholderPage(page, s))
		})
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r.Method(method, s.Path, h)
		}
	}
}
