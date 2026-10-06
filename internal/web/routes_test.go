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
	"github.com/drilonrecica/sinjal/internal/vault"
)

// appEnv is the production route table (Routes) on a migrated database.
type appEnv struct {
	h        http.Handler
	db       *db.DB
	sessions *auth.Sessions
	csrf     *CSRF
	logs     *lockedBuffer
}

var testCSRFKey = []byte("0123456789abcdef0123456789abcdef")

func newAppEnv(t *testing.T, trusted ...netip.Prefix) *appEnv {
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
		logs:     logs,
	}
	r := NewRouter(logger, trusted)
	Routes(r, App{
		Logger:   logger,
		DB:       d,
		Health:   NewHealth(d.Reader, logger),
		Assets:   assets.Default,
		Sessions: e.sessions,
		Setup:    NewSetup(d, nil, logger),
		CSRFKey:  testCSRFKey,
		Vault:    key,
	})
	e.h = r
	return e
}

// addUser inserts a user; an empty password leaves password_hash NULL.
func (e *appEnv) addUser(t *testing.T, id, login, role, password string) {
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
func (e *appEnv) signIn(t *testing.T, userID string) (string, auth.Session) {
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
