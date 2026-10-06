package web

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/assets"
	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
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
//     on every state-changing request. Inside it, setup/login/logout are
//     public; everything else requires a session (RequireAuth), and every
//     state change requires an admin (RequireAdmin).
func Routes(r chi.Router, app App) {
	authn := auth.NewAuthenticator(app.DB, logging.Sub(app.Logger, "auth"))
	login := NewLogin(authn, app.Sessions, app.Logger)
	reauth := NewReauth(authn, app.Sessions, login.limiter, app.Logger)

	RegisterHealth(r, app.Health)
	RegisterStatic(r, app.Assets)

	r.Group(func(r chi.Router) {
		r.Use(LoadSession(app.Sessions, app.Logger), NewCSRF(app.CSRFKey, app.Logger).Middleware)
		RegisterSetup(r, app.Setup)
		RegisterLogin(r, login)
		RegisterLogout(r, app.Sessions, app.Logger)

		// Signed-in users: admins and viewers.
		r.Group(func(r chi.Router) {
			r.Use(RequireAuth(app.Logger))
			RegisterPages(r, app.Logger)
			RegisterReauth(r, reauth)

			// Admins only. Every state-changing app route is mounted here;
			// TestRouteTableGuards fails for one mounted anywhere else.
			// Sensitive actions add RequireRecentAuth(app.Logger, time.Now).
			r.Group(func(r chi.Router) {
				r.Use(RequireAdmin(app.Logger))
			})
		})
	})
}
