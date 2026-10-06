package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestCreateViewer(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	seedResetUser(t, d, "a1", "admin", "admin")

	id, err := CreateViewer(ctx, d, "a1", "  vera ", "a long enough passphrase", now)
	if err != nil {
		t.Fatal(err)
	}
	var role, login, hash string
	if err := d.Reader.QueryRow(`SELECT role, login, password_hash FROM users WHERE id = ?`, id).Scan(&role, &login, &hash); err != nil {
		t.Fatal(err)
	}
	if role != "viewer" || login != "vera" {
		t.Errorf("created %q as %q", login, role)
	}
	if ok, _, _ := VerifyPassword("a long enough passphrase", hash); !ok {
		t.Error("the stored hash does not match the password")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'user.viewer_created' AND user_id = 'a1' AND object_id = ?`, id); n != 1 {
		t.Errorf("audit events = %d, want 1", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM audit_events WHERE metadata_json LIKE '%passphrase%'`); n != 0 {
		t.Error("the audit log holds the password")
	}

	var ie *InputError
	for name, c := range map[string]struct{ login, password, field string }{
		"short password": {"v2", "short", "password"},
		"bad login":      {"two words", "a long enough passphrase", "login"},
		"taken":          {"vera", "a long enough passphrase", "login"},
		"taken by case":  {"VERA", "a long enough passphrase", "login"},
		"admin's login":  {"ADMIN", "a long enough passphrase", "login"},
	} {
		if _, err := CreateViewer(ctx, d, "a1", c.login, c.password, now); !errors.As(err, &ie) || ie.Field != c.field {
			t.Errorf("%s: error = %v, want %s InputError", name, err, c.field)
		}
	}
	if n := count(t, d, `SELECT COUNT(*) FROM users`); n != 2 {
		t.Errorf("users = %d, want 2 (failed creations must write nothing)", n)
	}
}

func TestSetViewerDisabled(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	seedResetUser(t, d, "a1", "admin", "admin")
	seedResetUser(t, d, "v1", "vera", "viewer")
	seedResetUser(t, d, "v2", "vic", "viewer")

	if err := SetViewerDisabled(ctx, d, "a1", "v1", true, now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM users WHERE id = 'v1' AND disabled = 1`); n != 1 {
		t.Error("v1 is not disabled")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM sessions WHERE user_id = 'v1'`); n != 0 {
		t.Error("disabling left the viewer's sessions")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM sessions WHERE user_id IN ('v2', 'a1')`); n != 2 {
		t.Error("other users' sessions were deleted")
	}

	// A disabled account cannot sign in, even with the right password.
	real, err := CreateViewer(ctx, d, "a1", "real", "a long enough passphrase", now)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAuthenticator(d, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := a.Login(ctx, "real", "a long enough passphrase", "", now); err != nil {
		t.Fatalf("an enabled viewer cannot sign in: %v", err)
	}
	if err := SetViewerDisabled(ctx, d, "a1", real, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Login(ctx, "real", "a long enough passphrase", "", now); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a disabled viewer signed in: %v", err)
	}

	if err := SetViewerDisabled(ctx, d, "a1", "v1", false, now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM users WHERE id = 'v1' AND disabled = 0`); n != 1 {
		t.Error("v1 is not enabled again")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM audit_events WHERE event_type IN ('user.viewer_disabled', 'user.viewer_enabled') AND object_id = 'v1'`); n != 2 {
		t.Errorf("audit events = %d, want 2", n)
	}

	// Admins and unknown ids are not viewers.
	for _, id := range []string{"a1", "nobody"} {
		if err := SetViewerDisabled(ctx, d, "a1", id, true, now); !errors.Is(err, ErrUserNotFound) {
			t.Errorf("disable %q: %v", id, err)
		}
	}
	if n := count(t, d, `SELECT COUNT(*) FROM users WHERE id = 'a1' AND disabled = 1`); n != 0 {
		t.Error("an admin was disabled")
	}
}

func TestListViewers(t *testing.T) {
	d := testDB(t)
	seedResetUser(t, d, "a1", "admin", "admin")
	seedResetUser(t, d, "v1", "vera", "viewer")
	if err := SetViewerDisabled(context.Background(), d, "a1", "v1", true, now); err != nil {
		t.Fatal(err)
	}
	vs, err := ListViewers(context.Background(), d.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].ID != "v1" || vs[0].Login != "vera" || !vs[0].Disabled {
		t.Errorf("viewers = %+v", vs)
	}
}

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	seedResetUser(t, d, "v1", "vera", "viewer")
	if _, err := d.Writer.Exec(`INSERT INTO sessions (id, token_hash, user_id, created_at, expires_at, last_seen_at) VALUES ('keep', x'01', 'v1', 'now', 'later', 'now')`); err != nil {
		t.Fatal(err)
	}

	if err := ChangePassword(ctx, d, "v1", "short", "keep", "1.2.3.4", now); err == nil {
		t.Error("a short password was accepted")
	}
	if err := ChangePassword(ctx, d, "v1", "a brand new passphrase", "keep", "1.2.3.4", now); err != nil {
		t.Fatal(err)
	}
	var hash string
	d.Reader.QueryRow(`SELECT password_hash FROM users WHERE id = 'v1'`).Scan(&hash)
	if ok, _, _ := VerifyPassword("a brand new passphrase", hash); !ok {
		t.Error("the new password does not match")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM sessions WHERE user_id = 'v1'`); n != 1 {
		t.Errorf("sessions = %d, want only the kept one", n)
	}
	var meta string
	if err := d.Reader.QueryRow(`SELECT metadata_json FROM audit_events WHERE event_type = 'auth.password_changed' AND user_id = 'v1'`).Scan(&meta); err != nil {
		t.Fatalf("no audit event: %v", err)
	}
	if strings.Contains(meta, "passphrase") {
		t.Errorf("audit metadata holds the password: %s", meta)
	}
}
