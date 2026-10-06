package web

import (
	"bytes"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
)

// render writes a templ component as an HTML response. The page is rendered
// to a buffer first, so a template error produces a clean 500 instead of a
// half-written document. Admin HTML must not be cached.
func render(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		log.Error("render failed", "route", r.URL.Path, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(buf.Bytes())
	}
}
