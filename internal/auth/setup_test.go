package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// testDB returns a migrated database in a temp dir.
func testDB(t *testing.T) *db.DB {
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

func count(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Reader.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNormalizeLogin(t *testing.T) {
	for in, want := range map[string]string{"admin": "admin", "  Ops.Team-1 ": "Ops.Team-1", "ädmin": "ädmin"} {
		if got, err := NormalizeLogin(in); err != nil || got != want {
			t.Errorf("NormalizeLogin(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "   ", "two words", "tab\tx", "nul\x00", strings.Repeat("a", MaxLoginLen+1), "\xff"} {
		var ie *InputError
		if _, err := NormalizeLogin(in); !errors.As(err, &ie) || ie.Field != "login" {
			t.Errorf("NormalizeLogin(%q) error = %v, want login InputError", in, err)
		}
	}
	if _, err := NormalizeLogin(strings.Repeat("ä", MaxLoginLen)); err != nil {
		t.Errorf("64 multibyte characters rejected: %v", err)
	}
}

func TestValidatePassword(t *testing.T) {
	for _, ok := range []string{strings.Repeat("a", 12), strings.Repeat("ä", 12), strings.Repeat("a", MaxPasswordLen)} {
		if err := ValidatePassword(ok); err != nil {
			t.Errorf("ValidatePassword(len %d) = %v", len(ok), err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 11), strings.Repeat("a", MaxPasswordLen+1)} {
		var ie *InputError
		if err := ValidatePassword(bad); !errors.As(err, &ie) || ie.Field != "password" {
			t.Errorf("ValidatePassword(len %d) = %v, want password InputError", len(bad), err)
		}
	}
}

func TestSetupToken(t *testing.T) {
	a, b := NewSetupToken(), NewSetupToken()
	if !regexp.MustCompile(`^[a-z2-7]{26}$`).MatchString(a.Value()) {
		t.Fatalf("token %q is not 26 lowercase base32 chars", a.Value())
	}
	if a.Value() == b.Value() {
		t.Fatal("tokens are not unique")
	}
	v := a.Value()
	if !a.Check(v) || a.Check(b.Value()) || a.Check("") || a.Check(v[:25]) || a.Check(strings.ToUpper(v)) {
		t.Error("Check accepts the wrong candidates")
	}
	a.Discard()
	if a.Check(v) || a.Check("") || a.Value() != "" {
		t.Error("discarded token still matches")
	}
}

func TestCreateAdmin(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	if ok, err := AdminExists(ctx, d.Reader); err != nil || ok {
		t.Fatalf("AdminExists on empty db = %v, %v", ok, err)
	}

	id, err := CreateAdmin(ctx, d, " admin ", "correct horse battery", now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := AdminExists(ctx, d.Reader); !ok {
		t.Fatal("AdminExists = false after CreateAdmin")
	}

	var login, role, hash, created string
	if err := d.Reader.QueryRow(`SELECT login, role, password_hash, created_at FROM users WHERE id = ?`, id).
		Scan(&login, &role, &hash, &created); err != nil {
		t.Fatal(err)
	}
	if login != "admin" || role != "admin" || created != "2026-10-06T12:00:00Z" {
		t.Errorf("row = %q %q %q", login, role, created)
	}
	if ok, _, err := VerifyPassword("correct horse battery", hash); !ok || err != nil {
		t.Errorf("stored hash does not verify: %v", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM audit_events
		WHERE user_id = ? AND event_type = 'setup.admin_created' AND object_type = 'user' AND object_id = ?`, id, id); n != 1 {
		t.Errorf("audit events = %d, want 1", n)
	}

	if _, err := CreateAdmin(ctx, d, "second", "another long password", now); !errors.Is(err, ErrAdminExists) {
		t.Errorf("second CreateAdmin = %v, want ErrAdminExists", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM audit_events`); n != 1 {
		t.Errorf("audit events after refused create = %d, want 1", n)
	}
}

func TestCreateAdminValidates(t *testing.T) {
	d := testDB(t)
	var ie *InputError
	if _, err := CreateAdmin(context.Background(), d, "bad login", "long enough password", now); !errors.As(err, &ie) {
		t.Errorf("err = %v, want InputError", err)
	}
	if _, err := CreateAdmin(context.Background(), d, "admin", "short", now); !errors.As(err, &ie) {
		t.Errorf("err = %v, want InputError", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM users`); n != 0 {
		t.Errorf("users = %d after invalid input", n)
	}
}

// Concurrent setup submissions must create exactly one admin.
func TestCreateAdminIsRaceSafe(t *testing.T) {
	saved := currentParams
	currentParams = Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	t.Cleanup(func() { currentParams = saved })

	d := testDB(t)
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var created, refused int
	for i := range n {
		wg.Go(func() {
			_, err := CreateAdmin(context.Background(), d, "admin"+string(rune('a'+i)), "long enough password", now)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, ErrAdminExists):
				refused++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()
	if created != 1 || refused != n-1 {
		t.Errorf("created = %d, refused = %d", created, refused)
	}
	if c := count(t, d, `SELECT COUNT(*) FROM users WHERE role = 'admin'`); c != 1 {
		t.Errorf("admins = %d", c)
	}
	if c := count(t, d, `SELECT COUNT(*) FROM audit_events`); c != 1 {
		t.Errorf("audit events = %d", c)
	}
}
