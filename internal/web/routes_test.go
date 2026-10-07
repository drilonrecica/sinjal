package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/assets"
	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/engine"
	"github.com/drilonrecica/sinjal/internal/vault"
	"github.com/drilonrecica/sinjal/internal/web/sse"
)

// appEnv is the production route table (Routes) on a migrated database.
type appEnv struct {
	h        http.Handler
	db       *db.DB
	sessions *auth.Sessions
	csrf     *CSRF
	events   *sse.Hub
	engine   *engine.Engine
	key      *vault.Key
	logs     *lockedBuffer
}

var testCSRFKey = []byte("0123456789abcdef0123456789abcdef")

// testBaseURL is SINJAL_BASE_URL in tests. httptest requests arrive for
// example.com; passkey tests set the Host to this one.
const testBaseURL = "http://localhost"

func newAppEnv(t testing.TB, trusted ...netip.Prefix) *appEnv {
	t.Helper()
	return newAppEnvAt(t, testBaseURL, trusted...)
}

// newAppEnvAt is newAppEnv with SINJAL_BASE_URL set to baseURL.
func newAppEnvAt(t testing.TB, baseURL string, trusted ...netip.Prefix) *appEnv {
	t.Helper()
	logger, logs := quietLogger()
	d := migratedDB(t)
	key, err := vault.LoadOrCreate(context.Background(), t.TempDir(), d.Reader, logger)
	if err != nil {
		t.Fatal(err)
	}
	e := &appEnv{
		db:       d,
		sessions: auth.NewSessions(d, logger),
		csrf:     NewCSRF(testCSRFKey, logger),
		events:   sse.NewHub(logger),
		key:      key,
		logs:     logs,
	}
	// A running engine with nothing to check: the monitor pages schedule
	// what they create. Monitors added straight to the store are not
	// checked, so tests make no outside requests.
	e.engine = engine.New(d, key, 2, "Sinjal/test", time.UTC, nil, nil, nil, logger)
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); e.engine.Wait() })
	r := NewRouter(logger, trusted)
	Routes(r, App{
		Logger:   logger,
		DB:       d,
		Health:   NewHealth(d.Reader, logger),
		Assets:   assets.Default,
		Sessions: e.sessions,
		Events:   e.events,
		Setup:    NewSetup(d, nil, logger),
		CSRFKey:  testCSRFKey,
		Vault:    key,
		Passkeys: auth.NewPasskeys(d, baseURL, logger),
		Engine:   e.engine,
		BaseURL:  baseURL,
	})
	e.h = r
	return e
}

// addUser inserts a user; an empty password leaves password_hash NULL.
func (e *appEnv) addUser(t testing.TB, id, login, role, password string) {
	t.Helper()
	var hash any
	if password != "" {
		h, err := auth.HashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		hash = h
	}
	if _, err := e.db.Writer.Exec(`INSERT INTO users (id, login, role, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'now', 'now')`, id, login, role, hash); err != nil {
		t.Fatal(err)
	}
}

// signIn creates a session for userID and returns its cookie token.
func (e *appEnv) signIn(t testing.TB, userID string) (string, auth.Session) {
	t.Helper()
	token, sess, err := e.sessions.Create(context.Background(), userID, "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return token, sess
}

// req builds a request; a non-nil form becomes a url-encoded body.
func req(method, path string, form url.Values) *http.Request {
	if form == nil {
		return httptest.NewRequest(method, path, nil)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func withCookie(r *http.Request, token string) *http.Request {
	r.AddCookie(&http.Cookie{Name: plainSessionCookie, Value: token})
	return r
}

func (e *appEnv) serve(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}
