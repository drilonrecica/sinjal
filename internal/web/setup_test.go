package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/auth"
	"github.com/drilonrecica/sinjal/internal/db"
)

func migratedDB(t testing.TB) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "backups"), "test", quiet); err != nil {
		t.Fatal(err)
	}
	return d
}

type setupEnv struct {
	h     http.Handler
	db    *db.DB
	token *auth.SetupToken
	logs  *lockedBuffer
}

func newSetupEnv(t *testing.T, withToken bool) setupEnv {
	t.Helper()
	logger, logs := quietLogger()
	d := migratedDB(t)
	var tok *auth.SetupToken
	if withToken {
		tok = auth.NewSetupToken()
	}
	r := NewRouter(logger, nil)
	RegisterSetup(r, NewSetup(d, tok, logger))
	return setupEnv{h: r, db: d, token: tok, logs: logs}
}

func (e setupEnv) get(ip, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/setup?token="+url.QueryEscape(token), nil)
	req.RemoteAddr = ip + ":1234"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e setupEnv) post(ip string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = ip + ":1234"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e setupEnv) validForm() url.Values {
	return url.Values{
		"token": {e.token.Value()}, "login": {"admin"},
		"password": {"correct horse battery"}, "confirm": {"correct horse battery"},
	}
}

func TestSetupNotFoundWithoutToken(t *testing.T) {
	e := newSetupEnv(t, false)
	if rec := e.get("192.0.2.1", "anything"); rec.Code != 404 {
		t.Errorf("GET = %d, want 404", rec.Code)
	}
	if rec := e.post("192.0.2.1", url.Values{"token": {"x"}}); rec.Code != 404 {
		t.Errorf("POST = %d, want 404", rec.Code)
	}
}

func TestSetupFormRequiresToken(t *testing.T) {
	e := newSetupEnv(t, true)
	for _, bad := range []string{"", "wrong", strings.ToUpper(e.token.Value())} {
		rec := e.get("192.0.2.1", bad)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "Setup link not valid") {
			t.Errorf("GET token=%q = %d", bad, rec.Code)
		}
		if strings.Contains(rec.Body.String(), e.token.Value()) {
			t.Error("the real token leaked into a denial page")
		}
	}

	rec := e.get("192.0.2.1", e.token.Value())
	if rec.Code != 200 {
		t.Fatalf("GET with token = %d", rec.Code)
	}
	b := rec.Body.String()
	for _, want := range []string{
		`<input type="hidden" name="token" value="` + e.token.Value() + `"`,
		`<label for="login">`, `<label for="password">`, `<label for="confirm">`,
		`autocomplete="new-password"`, `minlength="12"`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", rec.Header())
	}
	if strings.Contains(e.logs.String(), e.token.Value()) {
		t.Error("token reached the access log")
	}
}

func TestSetupTokenFailuresAreRateLimited(t *testing.T) {
	e := newSetupEnv(t, true)
	for i := range setupMaxFailures {
		if rec := e.get("192.0.2.1", "wrong"); rec.Code != 403 {
			t.Fatalf("attempt %d = %d", i, rec.Code)
		}
	}
	if rec := e.get("192.0.2.1", e.token.Value()); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("after %d failures: %d, want 429 even with the right token", setupMaxFailures, rec.Code)
	}
	if rec := e.post("192.0.2.1", e.validForm()); rec.Code != 429 {
		t.Errorf("POST from blocked IP = %d, want 429", rec.Code)
	}
	if rec := e.get("192.0.2.2", e.token.Value()); rec.Code != 200 {
		t.Errorf("other IP = %d, want 200", rec.Code)
	}
}

func TestSetupValidationRerendersForm(t *testing.T) {
	e := newSetupEnv(t, true)
	tests := []struct {
		name   string
		change url.Values
		want   string
	}{
		{"mismatch", url.Values{"confirm": {"something else entirely"}}, "The passwords do not match."},
		{"short", url.Values{"password": {"short"}, "confirm": {"short"}}, "Use at least 12 characters."},
		{"bad login", url.Values{"login": {"two words"}}, "Spaces and control characters are not allowed."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form := e.validForm()
			for k, v := range tt.change {
				form[k] = v
			}
			rec := e.post("192.0.2.1", form)
			b := rec.Body.String()
			if rec.Code != 422 || !strings.Contains(b, tt.want) || !strings.Contains(b, `aria-invalid="true"`) {
				t.Errorf("status %d, body lacks %q or aria-invalid", rec.Code, tt.want)
			}
			if strings.Contains(b, form.Get("password")) || strings.Contains(b, form.Get("confirm")) {
				t.Error("a password was echoed back")
			}
			if !strings.Contains(b, `value="`+form.Get("login")+`"`) {
				t.Error("login was not kept")
			}
		})
	}
	if ok, _ := auth.AdminExists(context.Background(), e.db.Reader); ok {
		t.Error("an admin was created from invalid input")
	}
}

func TestSetupCreatesAdminOnce(t *testing.T) {
	e := newSetupEnv(t, true)
	token := e.token.Value()
	form := e.validForm()

	rec := e.post("192.0.2.1", form)
	if rec.Code != 303 || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST = %d %q, want 303 /login; body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if ok, _ := auth.AdminExists(context.Background(), e.db.Reader); !ok {
		t.Fatal("no admin after setup")
	}
	if e.token.Value() != "" {
		t.Error("token not discarded")
	}
	if rec := e.get("192.0.2.1", token); rec.Code != 404 {
		t.Errorf("GET after setup = %d, want 404", rec.Code)
	}
	if rec := e.post("192.0.2.1", form); rec.Code != 404 {
		t.Errorf("second POST = %d, want 404", rec.Code)
	}
	if strings.Contains(e.logs.String(), "correct horse battery") {
		t.Error("password reached the logs")
	}
}

// An admin created elsewhere (another request, reset-admin) closes setup
// even though this process still holds a token.
func TestSetupClosedWhenAdminExists(t *testing.T) {
	e := newSetupEnv(t, true)
	if _, err := auth.CreateAdmin(context.Background(), e.db, "other", "long enough password", time.Now()); err != nil {
		t.Fatal(err)
	}
	if rec := e.get("192.0.2.1", e.token.Value()); rec.Code != 404 {
		t.Errorf("GET = %d, want 404", rec.Code)
	}
	if rec := e.post("192.0.2.1", e.validForm()); rec.Code != 404 {
		t.Errorf("POST = %d, want 404", rec.Code)
	}
}

func TestSetupRejectsOversizedBody(t *testing.T) {
	e := newSetupEnv(t, true)
	form := e.validForm()
	form.Set("login", strings.Repeat("a", setupMaxBody))
	if rec := e.post("192.0.2.1", form); rec.Code != 400 {
		t.Errorf("POST = %d, want 400", rec.Code)
	}
}
