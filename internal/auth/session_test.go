package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

type sessionEnv struct {
	db  *db.DB
	s   *Sessions
	uid string
	ctx context.Context
}

func newSessionEnv(t *testing.T) sessionEnv {
	t.Helper()
	d := testDB(t)
	e := sessionEnv{db: d, s: NewSessions(d, slog.New(slog.NewTextHandler(io.Discard, nil))), uid: "u1", ctx: context.Background()}
	e.addUser(t, "u1", "admin", "admin")
	return e
}

func (e sessionEnv) addUser(t *testing.T, id, login, role string) {
	t.Helper()
	if _, err := e.db.Writer.Exec(`INSERT INTO users (id, login, role, password_hash, theme, created_at, updated_at)
		VALUES (?, ?, ?, 'x', 'paper', 'now', 'now')`, id, login, role); err != nil {
		t.Fatal(err)
	}
}

func (e sessionEnv) create(t *testing.T, uid string, at time.Time) (string, Session) {
	t.Helper()
	tok, sess, err := e.s.Create(e.ctx, uid, "Mozilla/5.0", "203.0.113.9", at)
	if err != nil {
		t.Fatal(err)
	}
	return tok, sess
}

func (e sessionEnv) lastSeen(t *testing.T, id string) string {
	t.Helper()
	var v string
	if err := e.db.Reader.QueryRow(`SELECT last_seen_at FROM sessions WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSessionCreateAndLookup(t *testing.T) {
	e := newSessionEnv(t)
	tok, sess := e.create(t, e.uid, now)

	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(tok) {
		t.Fatalf("token %q is not 32 bytes of base64url", tok)
	}
	if !sess.ExpiresAt.Equal(now.Add(30*24*time.Hour)) || !sess.ReauthenticatedAt.Equal(now) {
		t.Errorf("session = %+v", sess)
	}

	// Only the SHA-256 of the token is stored.
	var hash []byte
	var ua, ip string
	if err := e.db.Reader.QueryRow(`SELECT token_hash, user_agent, ip_hint FROM sessions WHERE id = ?`, sess.ID).Scan(&hash, &ua, &ip); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(tok)
	want := sha256.Sum256(raw)
	if !bytes.Equal(hash, want[:]) || bytes.Contains(hash, raw) || ua != "Mozilla/5.0" || ip != "203.0.113.9" {
		t.Errorf("stored row: hash ok=%v ua=%q ip=%q", bytes.Equal(hash, want[:]), ua, ip)
	}

	got, u, err := e.s.Lookup(e.ctx, tok, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != sess.ID || u.ID != e.uid || u.Login != "admin" || u.Role != "admin" || u.Theme != "paper" || u.Density != "comfortable" {
		t.Errorf("Lookup = %+v %+v", got, u)
	}

	tok2, _ := e.create(t, e.uid, now)
	if tok2 == tok {
		t.Error("tokens are not unique")
	}
}

func TestSessionLookupRejects(t *testing.T) {
	e := newSessionEnv(t)
	tok, _ := e.create(t, e.uid, now)

	for name, bad := range map[string]string{
		"empty": "", "garbage": "not a token", "short": tok[:42], "padded": tok + "=",
		"unknown": base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
	} {
		if _, _, err := e.s.Lookup(e.ctx, bad, now); !errors.Is(err, ErrNoSession) {
			t.Errorf("%s: err = %v, want ErrNoSession", name, err)
		}
	}

	if _, _, err := e.s.Lookup(e.ctx, tok, now.Add(SessionLifetime)); !errors.Is(err, ErrNoSession) {
		t.Errorf("at expiry: err = %v, want ErrNoSession", err)
	}
	if _, _, err := e.s.Lookup(e.ctx, tok, now.Add(SessionLifetime-time.Second)); err != nil {
		t.Errorf("just before expiry: %v", err)
	}

	if _, err := e.db.Writer.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, e.uid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.Lookup(e.ctx, tok, now); !errors.Is(err, ErrNoSession) {
		t.Errorf("disabled user: err = %v, want ErrNoSession", err)
	}
}

func TestSessionLastSeenIsThrottled(t *testing.T) {
	e := newSessionEnv(t)
	tok, sess := e.create(t, e.uid, now)

	for _, d := range []time.Duration{time.Second, time.Minute, LastSeenInterval - time.Second} {
		if _, _, err := e.s.Lookup(e.ctx, tok, now.Add(d)); err != nil {
			t.Fatal(err)
		}
		if got := e.lastSeen(t, sess.ID); got != "2026-10-06T12:00:00Z" {
			t.Fatalf("after %v: last_seen_at = %s, want no write", d, got)
		}
	}

	later := now.Add(LastSeenInterval)
	got, _, err := e.s.Lookup(e.ctx, tok, later)
	if err != nil {
		t.Fatal(err)
	}
	if v := e.lastSeen(t, sess.ID); v != "2026-10-06T12:05:00Z" || !got.LastSeenAt.Equal(later) {
		t.Errorf("last_seen_at = %s / %v, want 12:05", v, got.LastSeenAt)
	}
	// The window restarts from the new value; expiry is unchanged (absolute).
	e.s.Lookup(e.ctx, tok, later.Add(time.Minute))
	if v := e.lastSeen(t, sess.ID); v != "2026-10-06T12:05:00Z" {
		t.Errorf("second write inside the window: %s", v)
	}
	if got.ExpiresAt != sess.ExpiresAt {
		t.Error("lifetime was extended")
	}
}

func TestSessionRotate(t *testing.T) {
	e := newSessionEnv(t)
	oldTok, old := e.create(t, e.uid, now)

	later := now.Add(time.Hour)
	newTok, sess, err := e.s.Rotate(e.ctx, old, "UA2", "198.51.100.1", later)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == old.ID || newTok == oldTok || sess.UserID != e.uid || !sess.ReauthenticatedAt.Equal(later) {
		t.Errorf("rotated session = %+v", sess)
	}
	if _, _, err := e.s.Lookup(e.ctx, oldTok, later); !errors.Is(err, ErrNoSession) {
		t.Errorf("old token still valid: %v", err)
	}
	if _, _, err := e.s.Lookup(e.ctx, newTok, later); err != nil {
		t.Errorf("new token: %v", err)
	}
	if _, _, err := e.s.Rotate(e.ctx, old, "", "", later); !errors.Is(err, ErrNoSession) {
		t.Errorf("rotating a deleted session: %v, want ErrNoSession", err)
	}
}

func TestSessionDeletion(t *testing.T) {
	e := newSessionEnv(t)
	e.addUser(t, "u2", "viewer", "viewer")
	a, sa := e.create(t, e.uid, now)
	b, _ := e.create(t, e.uid, now)
	c, _ := e.create(t, e.uid, now)
	v, _ := e.create(t, "u2", now)
	valid := func(tok string) bool { _, _, err := e.s.Lookup(e.ctx, tok, now); return err == nil }

	if n, err := e.s.DeleteOthers(e.ctx, e.uid, sa.ID); err != nil || n != 2 {
		t.Fatalf("DeleteOthers = %d, %v", n, err)
	}
	if !valid(a) || valid(b) || valid(c) || !valid(v) {
		t.Error("DeleteOthers removed the wrong sessions")
	}

	if err := e.s.Delete(e.ctx, sa.ID); err != nil || valid(a) {
		t.Errorf("Delete: %v, still valid %v", err, valid(a))
	}

	if n, err := e.s.DeleteAllForUser(e.ctx, "u2"); err != nil || n != 1 || valid(v) {
		t.Errorf("DeleteAllForUser = %d, %v", n, err)
	}
}

func TestDeleteExpired(t *testing.T) {
	e := newSessionEnv(t)
	_, old := e.create(t, e.uid, now.Add(-SessionLifetime))
	fresh, _ := e.create(t, e.uid, now.Add(-time.Hour))

	n, err := e.s.DeleteExpired(e.ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpired = %d, %v", n, err)
	}
	if c := count(t, e.db, `SELECT COUNT(*) FROM sessions WHERE id = ?`, old.ID); c != 0 {
		t.Error("expired session kept")
	}
	if _, _, err := e.s.Lookup(e.ctx, fresh, now); err != nil {
		t.Errorf("live session removed: %v", err)
	}
}

func TestSessionTruncatesUserAgent(t *testing.T) {
	e := newSessionEnv(t)
	_, sess, err := e.s.Create(e.ctx, e.uid, strings.Repeat("ä", 1000), "", now)
	if err != nil {
		t.Fatal(err)
	}
	var ua string
	var ip *string
	e.db.Reader.QueryRow(`SELECT user_agent, ip_hint FROM sessions WHERE id = ?`, sess.ID).Scan(&ua, &ip)
	if len([]rune(ua)) != maxUserAgent || ip != nil {
		t.Errorf("user_agent %d runes, ip_hint %v", len([]rune(ua)), ip)
	}
}

func TestSetPasswordRevokesOtherSessions(t *testing.T) {
	saved := currentParams
	currentParams = Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	t.Cleanup(func() { currentParams = saved })

	e := newSessionEnv(t)
	keep, sk := e.create(t, e.uid, now)
	other, _ := e.create(t, e.uid, now)

	if err := SetPassword(e.ctx, e.db, e.uid, "short", sk.ID, now); err == nil {
		t.Error("short password accepted")
	}
	if _, _, err := e.s.Lookup(e.ctx, other, now); err != nil {
		t.Fatal("a rejected password change revoked sessions")
	}

	if err := SetPassword(e.ctx, e.db, e.uid, "a brand new passphrase", sk.ID, now); err != nil {
		t.Fatal(err)
	}
	var hash string
	e.db.Reader.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, e.uid).Scan(&hash)
	if ok, _, _ := VerifyPassword("a brand new passphrase", hash); !ok {
		t.Error("new password does not verify")
	}
	if _, _, err := e.s.Lookup(e.ctx, keep, now); err != nil {
		t.Errorf("current session revoked: %v", err)
	}
	if _, _, err := e.s.Lookup(e.ctx, other, now); !errors.Is(err, ErrNoSession) {
		t.Errorf("other session survived: %v", err)
	}

	if err := SetPassword(e.ctx, e.db, e.uid, "yet another passphrase", "", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.Lookup(e.ctx, keep, now); !errors.Is(err, ErrNoSession) {
		t.Error("keepID \"\" did not revoke every session")
	}
	if err := SetPassword(e.ctx, e.db, "nobody", "yet another passphrase", "", now); err == nil {
		t.Error("unknown user accepted")
	}
}
