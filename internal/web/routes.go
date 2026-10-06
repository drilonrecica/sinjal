package web

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/assets"
	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
)

// App is what the route table needs from startup.
type App struct {
	Logger   *slog.Logger
	DB       *db.DB
	Health   *Health
	Assets   *assets.Registry
	Sessions *auth.Sessions
	Setup    *Setup
	CSRFKey  []byte // vault.Key.Derive(CSRFKeyLabel)
}

// Routes mounts the whole route table (docs/31_HTTP_ROUTES.md) on r, which
// comes from NewRouter. Tests build the production table through this too.
//
//   - Health checks and static assets need no session and no CSRF check.
//     Future machine endpoints (heartbeat push) belong here as well.
//   - Every browser route sits in the session group: LoadSession, then CSRF
//     on every state-changing request.
func Routes(r chi.Router, app App) {
	RegisterHealth(r, app.Health)
	RegisterStatic(r, app.Assets)

	r.Group(func(r chi.Router) {
		r.Use(LoadSession(app.Sessions, app.Logger), NewCSRF(app.CSRFKey, app.Logger).Middleware)
		RegisterSetup(r, app.Setup)
		RegisterLogout(r, app.Sessions, app.Logger)
		RegisterPages(r, app.Logger)
	})
}
