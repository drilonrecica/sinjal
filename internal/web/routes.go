package web

import (
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/assets"
	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/vault"
)

// App is what the route table needs from startup.
type App struct {
	Logger   *slog.Logger
	DB       *db.DB
	Health   *Health
	Assets   *assets.Registry
	Sessions *auth.Sessions
	Setup    *Setup
	CSRFKey  []byte     // vault.Key.Derive(CSRFKeyLabel)
	Vault    *vault.Key // encrypts secrets at rest (TOTP)
	Passkeys *auth.Passkeys
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
	authn := auth.NewAuthenticator(app.DB, app.Vault, logging.Sub(app.Logger, "auth"))
	login := NewLogin(authn, app.Passkeys, app.Sessions, app.Logger)
	reauth := NewReauth(authn, app.Passkeys, app.Sessions, login.limiter, app.Logger)
	passkeys := NewPasskeys(app.Passkeys, app.Sessions, login, app.Logger)
	settingsAuth := NewSettingsAuth(authn, app.Passkeys, app.Sessions, app.Logger)
	recentAuth := RequireRecentAuth(app.Logger, time.Now)

	RegisterHealth(r, app.Health)
	RegisterStatic(r, app.Assets)

	r.Group(func(r chi.Router) {
		r.Use(LoadSession(app.Sessions, app.Logger), NewCSRF(app.CSRFKey, app.Logger).Middleware)
		RegisterSetup(r, app.Setup)
		RegisterLogin(r, login)
		RegisterPasskeyLogin(r, passkeys)
		RegisterLogout(r, app.Sessions, app.Logger)

		// Signed-in users: admins and viewers.
		r.Group(func(r chi.Router) {
			r.Use(RequireAuth(app.Logger))
			RegisterPages(r, app.Logger)
			RegisterReauth(r, reauth)
			RegisterPasskeyReauth(r, passkeys)

			// Admins only. Every state-changing app route is mounted here;
			// TestRouteTableGuards fails for one mounted anywhere else.
			// Sensitive actions sit behind recentAuth.
			r.Group(func(r chi.Router) {
				r.Use(RequireAdmin(app.Logger))
				RegisterSettingsAuth(r, settingsAuth, recentAuth)
				RegisterPasskeyRegistration(r, passkeys, recentAuth)
			})
		})
	})
}
