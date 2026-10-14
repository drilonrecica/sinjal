package web

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/web/templates"
)

// pageFor builds the Page for a request: the signed-in user's theme,
// density and Ambient light, and the CSRF token for forms.
func pageFor(r *http.Request, title string) templates.Page {
	var theme, density, login string
	ambient := true
	if cs, ok := SessionFromContext(r.Context()); ok {
		theme, density, ambient, login = cs.User.Theme, cs.User.Density, cs.User.AmbientLight, cs.User.Login
	}
	page := templates.NewPage(title, theme, density)
	page.Login = login
	page.AmbientOff = !ambient
	page.Admin = isAdmin(r)
	page.CSRFToken = CSRFToken(r)
	page.ColumnQuery, page.ColumnText, page.ColumnState = columnState(r)
	return page
}

// RegisterPages mounts one placeholder page per primary navigation section
// that has no page of its own yet. Real section pages replace these in their
// own milestones: Maintenance, Incidents, Notifications and Status Pages
// have their own handlers; Home and the monitors are not sections.
func RegisterPages(r chi.Router, logger *slog.Logger) {
	log := logging.Sub(logger, "http")
	for _, s := range templates.Sections {
		if s.Key == "maintenance" || s.Key == "incidents" || s.Key == "notifications" || s.Key == "status-pages" {
			continue
		}
		h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			page := pageFor(req, s.Label+" — Sinjal")
			render(w, req, log, http.StatusOK, templates.PlaceholderPage(page, s))
		})
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r.Method(method, s.Path, h)
		}
	}
}
