package web

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
)

type hostPageKey struct{}

// HostRouter serves the status page mapped to the request's hostname
// (docs/12 "Custom hostnames"). The hostname is the one resolved under the
// trusted-proxy rules (proxy.Host: X-Forwarded-Host only from a trusted
// peer), lowercased, without its port and trailing dot. A mapped hostname
// is answered entirely by mapped, which knows only the page's own routes;
// every other request, and the instance's own base-URL host, goes on to
// next. It must be installed before any route.
func HostRouter(d *db.DB, baseHost string, mapped http.Handler, logger *slog.Logger) func(http.Handler) http.Handler {
	log := logging.Sub(logger, "http")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := requestHost(r)
			if host == "" || host == baseHost {
				next.ServeHTTP(w, r)
				return
			}
			id, err := store.StatusPageIDByHost(r.Context(), d.Reader, host)
			switch {
			case errors.Is(err, store.ErrNotFound):
				next.ServeHTTP(w, r)
			case err != nil:
				// Falling through would show the admin UI on a mapped name.
				log.Error("looking up a mapped hostname failed", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			default:
				mapped.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), hostPageKey{}, id)))
			}
		})
	}
}

// requestHost is the trusted Host in the form hostnames are stored
// (statuspage.NormalizeHost): lowercase, no port, no brackets, no trailing
// dot.
func requestHost(r *http.Request) string {
	h := proxy.Host(r)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(strings.Trim(h, "[]"), ".")
}

// mappedRoutes is everything a mapped hostname serves: the page at /,
// its api.json and feed.xml, the health check, the static assets and the uploads (logos). Anything
// else, the admin UI, /login, /events, /api/v1 and other pages included,
// is 404. The page's api.json and feed.xml join it with M7-07. There is
// no session here: session cookies belong to the instance's own host.
func mappedRoutes(app App, csrf *CSRF, public *Public) http.Handler {
	r := chi.NewRouter()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/healthz", http.HandlerFunc(app.Health.healthz))
	}
	RegisterStatic(r, app.Assets)
	RegisterUploads(r, app.Uploads)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/api.json", public.byHostAs(kindJSON))
		r.Method(method, "/feed.xml", public.byHostAs(kindFeed))
	}
	r.Group(func(r chi.Router) {
		r.Use(csrf.Middleware) // the password form: an anonymous post gets the origin check
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			r.Method(method, "/", http.HandlerFunc(public.byHost))
		}
	})
	return r
}

// byHost serves / on a mapped hostname.
func (h *Public) byHost(w http.ResponseWriter, r *http.Request) {
	id, _ := r.Context().Value(hostPageKey{}).(string)
	p, err := store.GetStatusPage(r.Context(), h.db.Reader, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, "loading a status page", err)
		return
	}
	h.serve(w, r, p, pageAccess{base: "/", mapped: true})
}
