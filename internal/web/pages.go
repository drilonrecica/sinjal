package web

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/web/templates"
)

// RegisterPages mounts one placeholder page per primary navigation section.
// Real section pages replace these in their own milestones.
func RegisterPages(r chi.Router, logger *slog.Logger) {
	log := logging.Sub(logger, "http")
	for _, s := range templates.Sections {
		h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			page := templates.NewPage(s.Label+" — Sinjal", "", "")
			render(w, req, log, http.StatusOK, templates.PlaceholderPage(page, s))
		})
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r.Method(method, s.Path, h)
		}
	}
}
