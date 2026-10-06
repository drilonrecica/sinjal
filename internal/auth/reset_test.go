package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/drilonrecica/sinjal/internal/db"
)

// seedResetUser inserts a user holding a TOTP secret, one session and one
// passkey, so a reset has something to clear.
func seedResetUser(t *testing.T, d *db.DB, id, login, role string) {
	t.Helper()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users (id, login, role, password_hash, totp_secret_enc, totp_last_step, created_at, updated_at)
			VALUES (?, ?, ?, 'old-hash', x'0102', 7, 'now', 'now')`, []any{id, login, role}},
		{`INSERT INTO sessions (id, token_hash, user_id, created_at, expires_at, last_seen_at)
			VALUES (?, ?, ?, 'now', 'later', 'now')`, []any{"s-" + id, []byte("s-" + id), id}},
		{`INSERT INTO passkeys (id, user_id, credential_id, public_key, created_at)
			VALUES (?, ?, ?, x'00', 'now')`, []any{"p-" + id, id, []byte("p-" + id)}},
	} {
		if _, err := d.Writer.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResetAdmin(t *testing.T) {
	for _, removePasskeys := range []bool{false, true} {
		d := testDB(t)
		seedResetUser(t, d, "a1", "admin", "admin")
		seedResetUser(t, d, "v1", "viewer", "viewer") // must be untouched

		pw, err := ResetAdmin(context.Background(), d, "", removePasskeys, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePassword(pw); err != nil {
			t.Errorf("generated password is not acceptable to the policy: %v", err)
		}

		var hash string
		var totp []byte
		var step *int64
		if err := d.Reader.QueryRow(`SELECT password_hash, totp_secret_enc, totp_last_step FROM users WHERE id = 'a1'`).Scan(&hash, &totp, &step); err != nil {
			t.Fatal(err)
		}
		if ok, _, _ := VerifyPassword(pw, hash); !ok {
			t.Error("the printed password does not match the stored hash")
		}
		if totp != nil || step != nil {
			t.Errorf("TOTP not cleared: secret %v step %v", totp, step)
		}
		if n := count(t, d, `SELECT COUNT(*) FROM sessions WHERE user_id = 'a1'`); n != 0 {
			t.Errorf("admin sessions = %d, want 0", n)
		}
		wantPasskeys := 1
		if removePasskeys {
			wantPasskeys = 0
		}
		if n := count(t, d, `SELECT COUNT(*) FROM passkeys WHERE user_id = 'a1'`); n != wantPasskeys {
			t.Errorf("removePasskeys=%v: passkeys = %d, want %d", removePasskeys, n, wantPasskeys)
		}
		if n := count(t, d, `SELECT COUNT(*) FROM sessions WHERE user_id = 'v1'`) + count(t, d, `SELECT COUNT(*) FROM passkeys WHERE user_id = 'v1'`); n != 2 {
			t.Error("a viewer's sessions or passkeys were touched")
		}
		if n := count(t, d, `SELECT COUNT(*) FROM users WHERE id = 'v1' AND password_hash = 'old-hash' AND totp_secret_enc IS NOT NULL`); n != 1 {
			t.Error("a viewer's credentials were touched")
		}
		if n := count(t, d, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'admin_reset_cli' AND user_id = 'a1'`); n != 1 {
			t.Errorf("admin_reset_cli events = %d, want 1", n)
		}
		var meta string
		d.Reader.QueryRow(`SELECT metadata_json FROM audit_events`).Scan(&meta)
		if got := map[bool]string{false: `{"passkeys_removed":"false"}`, true: `{"passkeys_removed":"true"}`}[removePasskeys]; meta != got {
			t.Errorf("audit metadata = %s, want %s", meta, got)
		}
	}
}

func TestResetAdminSelection(t *testing.T) {
	ctx := context.Background()

	d := testDB(t)
	if _, err := ResetAdmin(ctx, d, "", false, now); !errors.Is(err, ErrNoAdmin) {
		t.Errorf("no admin: %v", err)
	}

	seedResetUser(t, d, "a1", "alice", "admin")
	seedResetUser(t, d, "a2", "bob", "admin")
	seedResetUser(t, d, "v1", "vera", "viewer")
	if _, err := ResetAdmin(ctx, d, "", false, now); !errors.Is(err, ErrAdminAmbiguous) {
		t.Errorf("two admins without --login: %v", err)
	}
	if _, err := ResetAdmin(ctx, d, "carol", false, now); !errors.Is(err, ErrAdminNotFound) {
		t.Errorf("unknown login: %v", err)
	}
	if _, err := ResetAdmin(ctx, d, "vera", false, now); !errors.Is(err, ErrAdminNotFound) {
		t.Errorf("a viewer must not be resettable here: %v", err)
	}
	var ie *InputError
	if _, err := ResetAdmin(ctx, d, "two words", false, now); !errors.As(err, &ie) {
		t.Errorf("malformed login: %v", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM audit_events`); n != 0 {
		t.Errorf("failed resets wrote %d audit events", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM users WHERE password_hash <> 'old-hash'`); n != 0 {
		t.Error("a failed reset changed a password")
	}

	if _, err := ResetAdmin(ctx, d, " bob ", false, now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM users WHERE id = 'a2' AND password_hash <> 'old-hash'`); n != 1 {
		t.Error("bob was not reset")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM users WHERE id = 'a1' AND password_hash = 'old-hash'`); n != 1 {
		t.Error("alice was reset too")
	}
}
