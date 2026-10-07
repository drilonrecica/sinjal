package web

import (
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/assets"
	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/engine"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/vault"
	"github.com/drilonrecica/sinjal/internal/web/sse"
)

// App is what the route table needs from startup.
type App struct {
	Logger   *slog.Logger
	DB       *db.DB
	Health   *Health
	Assets   *assets.Registry
	Sessions *auth.Sessions
	Events   *sse.Hub // live updates on GET /events
	Setup    *Setup
	CSRFKey  []byte     // vault.Key.Derive(CSRFKeyLabel)
	Vault    *vault.Key // encrypts secrets at rest (TOTP, monitor secrets)
	Passkeys *auth.Passkeys
	Engine   *engine.Engine // schedules monitors after they change; must be started
	Timezone *time.Location // the instance time zone; nil is UTC
	BaseURL  string         // SINJAL_BASE_URL; "" when unset
	Uploads  string         // directory of uploaded files (the data directory's uploads/)
}

// Routes mounts the whole route table (docs/31_HTTP_ROUTES.md) on r, which
// comes from NewRouter. Tests build the production table through this too.
//
//   - Health checks, static assets and machine endpoints (heartbeat push)
//     need no session and no CSRF check.
//   - Every browser route sits in the session group: LoadSession, then CSRF
//     on every state-changing request. Inside it, setup/login/logout are
//     public; everything else requires a session (RequireAuth), and every
//     state change requires an admin (RequireAdmin).
func Routes(r chi.Router, app App) {
	authn := auth.NewAuthenticator(app.DB, app.Vault, logging.Sub(app.Logger, "auth"))
	login := NewLogin(authn, app.Passkeys, app.Sessions, app.Logger)
	reauth := NewReauth(authn, app.Passkeys, app.Sessions, login.limiter, app.Logger)
	passkeys := NewPasskeys(app.Passkeys, app.Sessions, login, app.Logger)
	settingsAuth := NewSettingsAuth(authn, app.DB, app.Passkeys, app.Sessions, app.Logger)
	system := NewSettingsSystem(app.DB, app.Logger)
	monitors := NewMonitors(app.DB, app.Vault, app.Engine, app.Events, app.Timezone, app.Logger)
	account := NewAccount(app.DB, app.Sessions, app.Logger)
	maint := NewMaintenance(app.DB, app.Events, app.Timezone, app.Logger)
	notifications := NewNotifications(app.DB, app.Vault, app.Events, app.Timezone, app.Logger)
	overview := NewOverview(app.DB, app.Timezone, app.Logger)
	incidents := NewIncidents(app.DB, app.Events, app.Timezone, app.Logger)
	statusPages := NewStatusPages(app.DB, app.BaseURL, app.Uploads, app.Logger)
	recentAuth := RequireRecentAuth(app.Logger, time.Now)

	RegisterHealth(r, app.Health)
	RegisterStatic(r, app.Assets)
	RegisterUploads(r, app.Uploads)
	RegisterHeartbeat(r, NewHeartbeat(app.Engine, app.Logger))

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
			RegisterEvents(r, app.Events, app.Sessions, app.Logger)
			RegisterMonitors(r, monitors)
			RegisterMaintenance(r, maint)
			RegisterNotifications(r, notifications)
			RegisterIncidents(r, incidents)
			RegisterOverview(r, overview)
			RegisterReauth(r, reauth)
			RegisterPasskeyReauth(r, passkeys)
			RegisterAccount(r, account, recentAuth) // the caller's own password; viewers too

			// Admins only. Every state-changing app route is mounted here;
			// TestRouteTableGuards fails for one mounted anywhere else.
			// Sensitive actions sit behind recentAuth.
			r.Group(func(r chi.Router) {
				r.Use(RequireAdmin(app.Logger))
				RegisterSettingsAuth(r, settingsAuth, recentAuth)
				RegisterPasskeyRegistration(r, passkeys, recentAuth)
				RegisterSettingsSystem(r, system) // read-only, but shows client addresses
				RegisterMonitorChanges(r, monitors, recentAuth)
				RegisterMaintenanceChanges(r, maint)
				RegisterNotificationChanges(r, notifications)
				RegisterIncidentChanges(r, incidents)
				RegisterStatusPages(r, statusPages, recentAuth)
			})
		})
	})
}
