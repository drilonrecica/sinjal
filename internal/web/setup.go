package web

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/logging"
	"github.com/drilonrecica/sinjal/internal/ratelimit"
	"github.com/drilonrecica/sinjal/internal/web/proxy"
	"github.com/drilonrecica/sinjal/web/templates"
)

// Failed setup-token attempts per client IP (docs/13: rate-limited like login).
const (
	setupMaxFailures = 10
	setupWindow      = 15 * time.Minute
	setupTrackedIPs  = 1024
	setupMaxBody     = 16 << 10
)

// Setup serves /setup while no admin exists (decision P0-10).
type Setup struct {
	db      *db.DB
	token   *auth.SetupToken // nil when an admin existed at startup
	limiter *ratelimit.Limiter
	log     *slog.Logger
	now     func() time.Time
}

// NewSetup returns the setup handler. token is nil when an admin already
// existed at startup; /setup is then always 404.
func NewSetup(d *db.DB, token *auth.SetupToken, logger *slog.Logger) *Setup {
	return &Setup{
		db:      d,
		token:   token,
		limiter: ratelimit.New(setupMaxFailures, setupWindow, setupTrackedIPs),
		log:     logging.Sub(logger, "auth"),
		now:     time.Now,
	}
}

// RegisterSetup mounts GET/HEAD/POST /setup.
func RegisterSetup(r chi.Router, s *Setup) {
	r.Get("/setup", s.serveForm)
	r.Head("/setup", s.serveForm)
	r.Post("/setup", s.submit)
}

func setupPage() templates.Page { return templates.NewPage("Set up Sinjal", "", "") }

// open reports whether setup is still possible; otherwise it answers 404.
// The database is consulted on every request: auth state is never cached.
func (s *Setup) open(w http.ResponseWriter, r *http.Request) bool {
	// The token is in the URL; keep it out of any Referer header.
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.token == nil || s.token.Value() == "" {
		http.NotFound(w, r)
		return false
	}
	exists, err := auth.AdminExists(r.Context(), s.db.Reader)
	if err != nil {
		s.log.Error("setup: admin lookup failed", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return false
	}
	if exists {
		http.NotFound(w, r)
		return false
	}
	return true
}

// checkToken verifies the token under the per-IP failure limit. On failure
// it has already written a generic response.
func (s *Setup) checkToken(w http.ResponseWriter, r *http.Request, candidate string) bool {
	ip := proxy.ClientIP(r).String()
	now := s.now()
	if s.limiter.Blocked(ip, now) {
		w.Header().Set("Retry-After", "900")
		render(w, r, s.log, http.StatusTooManyRequests, templates.AuthMessage(setupPage(),
			"Too many attempts", "Wait a few minutes, then open the setup link from the server log again."))
		return false
	}
	if !s.token.Check(candidate) {
		s.limiter.Add(ip, now)
		s.log.Warn("setup: invalid setup token", "client_ip", ip)
		render(w, r, s.log, http.StatusForbidden, templates.AuthMessage(setupPage(),
			"Setup link not valid", "Open the setup link printed in the server log. A restart prints a new link."))
		return false
	}
	return true
}

func (s *Setup) serveForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !s.open(w, r) || !s.checkToken(w, r, token) {
		return
	}
	s.renderForm(w, r, http.StatusOK, templates.SetupForm{Token: token})
}

func (s *Setup) submit(w http.ResponseWriter, r *http.Request) {
	if !s.open(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, setupMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token := r.PostForm.Get("token")
	if !s.checkToken(w, r, token) {
		return
	}

	form := templates.SetupForm{Token: token, Login: r.PostForm.Get("login"), Errors: map[string]string{}}
	password, confirm := r.PostForm.Get("password"), r.PostForm.Get("confirm")
	if login, err := auth.NormalizeLogin(form.Login); err != nil {
		form.Errors["login"] = inputMessage(err)
	} else {
		form.Login = login
	}
	if err := auth.ValidatePassword(password); err != nil {
		form.Errors["password"] = inputMessage(err)
	} else if password != confirm {
		form.Errors["confirm"] = "The passwords do not match."
	}
	if len(form.Errors) > 0 {
		s.renderForm(w, r, http.StatusUnprocessableEntity, form)
		return
	}

	id, err := auth.CreateAdmin(r.Context(), s.db, form.Login, password, s.now())
	switch {
	case errors.Is(err, auth.ErrAdminExists):
		http.NotFound(w, r)
		return
	case err != nil:
		s.log.Error("setup: creating the admin failed", "error", err)
		form.Errors["form"] = "The account could not be created. Check the server log and try again."
		s.renderForm(w, r, http.StatusInternalServerError, form)
		return
	}
	s.token.Discard()
	s.log.Info("initial admin account created", "user_id", id)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Setup) renderForm(w http.ResponseWriter, r *http.Request, status int, f templates.SetupForm) {
	render(w, r, s.log, status, templates.Setup(setupPage(), f, auth.MinPasswordLen))
}

// inputMessage returns the user-facing text of a validation error.
func inputMessage(err error) string {
	var ie *auth.InputError
	if errors.As(err, &ie) {
		return ie.Message
	}
	return "Invalid value."
}
