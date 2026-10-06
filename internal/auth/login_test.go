package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/vault"
)

const testPassword = "correct horse battery"

type loginEnv struct {
	t     *testing.T
	a     *Authenticator
	calls *[]string // hashes passed to verify
}

func newLoginEnv(t *testing.T) loginEnv {
	t.Helper()
	d := testDB(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	key, err := vault.LoadOrCreate(context.Background(), t.TempDir(), d.Reader, quiet)
	if err != nil {
		t.Fatal(err)
	}
	e := loginEnv{t: t, a: NewAuthenticator(d, key, quiet), calls: new([]string)}
	t.Cleanup(func() { verify = VerifyPassword })
	verify = func(pw, hash string) (bool, bool, error) {
		*e.calls = append(*e.calls, hash)
		return VerifyPassword(pw, hash)
	}
	return e
}

// user inserts an account; hash "" leaves password_hash NULL.
func (e loginEnv) user(id, login, role, hash string, disabled bool) {
	e.t.Helper()
	var h any
	if hash != "" {
		h = hash
	}
	if _, err := e.a.db.Writer.Exec(`INSERT INTO users (id, login, role, password_hash, disabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'now', 'now')`, id, login, role, h, disabled); err != nil {
		e.t.Fatal(err)
	}
}

func (e loginEnv) hash(pw string) string {
	e.t.Helper()
	h, err := HashPassword(pw)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

type auditRow struct {
	userID, event, meta string
}

func (e loginEnv) audits() []auditRow {
	e.t.Helper()
	rows, err := e.a.db.Reader.Query(`SELECT COALESCE(user_id, ''), event_type, COALESCE(metadata_json, '') FROM audit_events ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.userID, &r.event, &r.meta); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestLoginSucceeds(t *testing.T) {
	e := newLoginEnv(t)
	e.user("u1", "Admin", "admin", e.hash(testPassword), false)

	u, err := e.a.Login(context.Background(), "  Admin ", testPassword, "203.0.113.9", now)
	if err != nil || u.ID != "u1" || u.Role != "admin" || u.Login != "Admin" {
		t.Fatalf("Login = %+v, %v", u, err)
	}
	got := e.audits()
	if len(got) != 1 || got[0] != (auditRow{"u1", "auth.login_succeeded", `{"client_ip":"203.0.113.9"}`}) {
		t.Errorf("audit = %+v", got)
	}
}

// TestLoginFailuresLookAlike: every failure is ErrInvalidCredentials and
// costs exactly one hash verification, so neither the error nor the timing
// tells whether the account exists.
func TestLoginFailuresLookAlike(t *testing.T) {
	tests := []struct {
		name, login, password string
		wantUserInAudit       string
	}{
		{"wrong password", "admin", "wrong password!", "u1"},
		{"unknown user", "nobody", testPassword, ""},
		{"login differs in case", "ADMIN", testPassword, ""},
		{"disabled user, right password", "off", testPassword, "u2"},
		{"no password set", "keyonly", testPassword, "u3"},
		{"invalid login", "has space", testPassword, ""},
		{"empty password", "admin", "", "u1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newLoginEnv(t)
			h := e.hash(testPassword)
			e.user("u1", "admin", "admin", h, false)
			e.user("u2", "off", "viewer", h, true)
			e.user("u3", "keyonly", "admin", "", false)
			*e.calls = nil

			_, err := e.a.Login(context.Background(), tt.login, tt.password, "203.0.113.9", now)
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("err = %v, want ErrInvalidCredentials", err)
			}
			if len(*e.calls) != 1 {
				t.Errorf("verify called %d times, want 1", len(*e.calls))
			}
			got := e.audits()
			if len(got) != 1 || got[0].event != "auth.login_failed" || got[0].userID != tt.wantUserInAudit {
				t.Errorf("audit = %+v, want login_failed for %q", got, tt.wantUserInAudit)
			}
			if strings.Contains(got[0].meta, tt.login) && tt.login != "" {
				t.Errorf("attempted login stored in audit metadata: %s", got[0].meta)
			}
		})
	}
}

func TestLoginOverlongPasswordSkipsTheHash(t *testing.T) {
	e := newLoginEnv(t)
	e.user("u1", "admin", "admin", e.hash(testPassword), false)
	*e.calls = nil
	_, err := e.a.Login(context.Background(), "admin", strings.Repeat("x", MaxPasswordLen+1), "", now)
	if !errors.Is(err, ErrInvalidCredentials) || len(*e.calls) != 0 {
		t.Errorf("err %v, %d verifications; want ErrInvalidCredentials without hashing", err, len(*e.calls))
	}
}

func TestLoginMalformedStoredHashIsAFailure(t *testing.T) {
	e := newLoginEnv(t)
	e.user("u1", "admin", "admin", "$argon2id$garbage", false)
	if _, err := e.a.Login(context.Background(), "admin", testPassword, "", now); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginRehashesOutdatedParameters(t *testing.T) {
	e := newLoginEnv(t)
	saved := currentParams
	currentParams = Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	old := e.hash(testPassword)
	currentParams = saved
	e.user("u1", "admin", "admin", old, false)

	if _, err := e.a.Login(context.Background(), "admin", testPassword, "", now); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := e.a.db.Reader.QueryRow(`SELECT password_hash FROM users WHERE id = 'u1'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == old || !strings.Contains(stored, "m=19456,t=2,p=1") {
		t.Errorf("hash not upgraded: %s", stored)
	}
	if ok, rehash, err := VerifyPassword(testPassword, stored); !ok || rehash || err != nil {
		t.Errorf("new hash: ok %v rehash %v err %v", ok, rehash, err)
	}
}

func TestReauthenticate(t *testing.T) {
	e := newLoginEnv(t)
	e.user("u1", "admin", "admin", e.hash(testPassword), false)
	e.user("u2", "off", "admin", e.hash(testPassword), true)
	ctx := context.Background()

	if err := e.a.Reauthenticate(ctx, "u1", testPassword, "", "203.0.113.9", now); err != nil {
		t.Fatalf("right password: %v", err)
	}
	for _, tc := range []struct{ uid, pw string }{{"u1", "wrong password!"}, {"u2", testPassword}, {"gone", testPassword}} {
		*e.calls = nil
		if err := e.a.Reauthenticate(ctx, tc.uid, tc.pw, "", "203.0.113.9", now); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s/%s: err = %v, want ErrInvalidCredentials", tc.uid, tc.pw, err)
		}
		if len(*e.calls) != 1 {
			t.Errorf("%s: %d verifications, want 1", tc.uid, len(*e.calls))
		}
	}
	got := e.audits()
	if len(got) != 4 || got[0].event != "auth.reauthenticated" || got[0].userID != "u1" || got[1].event != "auth.reauth_failed" {
		t.Errorf("audit = %+v", got)
	}
}

func TestRecentlyAuthenticated(t *testing.T) {
	s := Session{ReauthenticatedAt: now}
	if !s.RecentlyAuthenticated(now.Add(ReauthWindow-time.Second)) || s.RecentlyAuthenticated(now.Add(ReauthWindow)) {
		t.Error("window boundary wrong")
	}
	if (Session{}).RecentlyAuthenticated(now) {
		t.Error("a session never re-authenticated counts as recent")
	}
}
