package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/assets"
)

// RegisterStatic mounts the embedded, content-hashed static assets, and
// /favicon.ico for clients that ask for it without reading the page's
// <link rel="icon">: a permanent redirect to the hashed icon.
func RegisterStatic(r chi.Router, reg *assets.Registry) {
	r.Handle(assets.Prefix+"*", reg.Handler())
	icon := reg.URL("img/favicon.svg")
	favicon := func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, icon, http.StatusMovedPermanently)
	}
	r.Get("/favicon.ico", favicon)
	r.Head("/favicon.ico", favicon)
}
