package web

import (
	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/assets"
)

// RegisterStatic mounts the embedded, content-hashed static assets.
func RegisterStatic(r chi.Router, reg *assets.Registry) {
	r.Handle(assets.Prefix+"*", reg.Handler())
}
