package db

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// migratedEnv returns an environment migrated with every embedded migration.
func migratedEnv(t *testing.T) env {
	t.Helper()
	e := newEnv(t)
	if err := Migrate(context.Background(), e.db, e.backupDir, "test", quiet); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e env) exec(t *testing.T, q string, args ...any) error {
	t.Helper()
	_, err := e.db.Writer.Exec(q, args...)
	return err
}

func (e env) mustExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := e.exec(t, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (e env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.Writer.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

const now = "2026-10-06T12:00:00Z"

func (e env) addUser(t *testing.T, id, login string) {
	t.Helper()
	e.mustExec(t, `INSERT INTO users (id, login, role, created_at, updated_at) VALUES (?, ?, 'admin', ?, ?)`, id, login, now, now)
}

type column struct {
	Name    string
	NotNull bool
	Default sql.NullString
}

func columns(t *testing.T, e env, table string) []column {
	t.Helper()
	rows, err := e.db.Writer.Query(`SELECT name, "notnull", dflt_value FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.Name, &c.NotNull, &c.Default); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func TestAuthTableColumns(t *testing.T) {
	e := migratedEnv(t)
	// name:notnull[=default]
	want := map[string]string{
		"users": "id:0 login:1 display_name:0 role:1 password_hash:0 totp_secret_enc:0 totp_last_step:0 disabled:1=0 theme:0 " +
			"density:1='comfortable' sidebar_collapsed:1=0 created_at:1 updated_at:1",
		"sessions":     "id:0 token_hash:1 user_id:1 created_at:1 expires_at:1 last_seen_at:1 reauthenticated_at:0 user_agent:0 ip_hint:0",
		"passkeys":     "id:0 user_id:1 credential_id:1 public_key:1 sign_count:1=0 transports_json:0 backup_eligible:1=0 label:0 created_at:1 last_used_at:0",
		"audit_events": "id:0 user_id:0 event_type:1 object_type:0 object_id:0 metadata_json:0 created_at:1",
	}
	for table, spec := range want {
		var got []string
		for _, c := range columns(t, e, table) {
			s := c.Name + ":0"
			if c.NotNull {
				s = c.Name + ":1"
			}
			if c.Default.Valid {
				s += "=" + c.Default.String
			}
			got = append(got, s)
		}
		// INTEGER PRIMARY KEY / TEXT PRIMARY KEY report notnull 0 in SQLite; normalise ids.
		if strings.Join(got, " ") != spec {
			t.Errorf("%s columns:\n got  %s\n want %s", table, strings.Join(got, " "), spec)
		}
	}
}

func TestAuthIndexes(t *testing.T) {
	e := migratedEnv(t)
	for _, idx := range []string{"idx_sessions_user", "idx_sessions_expiry", "idx_audit_events_time"} {
		if e.count(t, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx) != 1 {
			t.Errorf("index %s is missing", idx)
		}
	}
}

func TestUserConstraintsAndDefaults(t *testing.T) {
	e := migratedEnv(t)
	e.addUser(t, "u1", "admin")

	var disabled, collapsed int
	var density string
	var theme, password sql.NullString
	err := e.db.Writer.QueryRow(`SELECT disabled, density, sidebar_collapsed, theme, password_hash FROM users WHERE id='u1'`).
		Scan(&disabled, &density, &collapsed, &theme, &password)
	if err != nil {
		t.Fatal(err)
	}
	if disabled != 0 || density != "comfortable" || collapsed != 0 || theme.Valid || password.Valid {
		t.Errorf("defaults = disabled %d density %q collapsed %d theme %v password %v", disabled, density, collapsed, theme, password)
	}

	bad := map[string]string{
		"unknown role":    `INSERT INTO users (id, login, role, created_at, updated_at) VALUES ('u2','b','owner','x','x')`,
		"unknown theme":   `INSERT INTO users (id, login, role, theme, created_at, updated_at) VALUES ('u3','c','admin','neon','x','x')`,
		"unknown density": `INSERT INTO users (id, login, role, density, created_at, updated_at) VALUES ('u4','d','admin','huge','x','x')`,
		"duplicate login": `INSERT INTO users (id, login, role, created_at, updated_at) VALUES ('u5','admin','viewer','x','x')`,
		"missing login":   `INSERT INTO users (id, role, created_at, updated_at) VALUES ('u6','viewer','x','x')`,
	}
	for name, q := range bad {
		if err := e.exec(t, q); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for _, theme := range []string{"carbon", "paper", "midnight", "terminal"} {
		e.mustExec(t, `INSERT INTO users (id, login, role, theme, created_at, updated_at) VALUES (?, ?, 'viewer', ?, 'x', 'x')`, "t-"+theme, "t-"+theme, theme)
	}
}

func TestSessionAndPasskeyConstraints(t *testing.T) {
	e := migratedEnv(t)
	e.addUser(t, "u1", "admin")

	addSession := func(id string, token string, user string) error {
		return e.exec(t, `INSERT INTO sessions (id, token_hash, user_id, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, []byte(token), user, now, now, now)
	}
	if err := addSession("s1", "hash-a", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := addSession("s2", "hash-a", "u1"); err == nil {
		t.Error("duplicate token_hash was accepted")
	}
	if err := addSession("s3", "hash-b", "ghost"); err == nil {
		t.Error("session for a non-existent user was accepted (foreign keys must be enforced)")
	}

	addPasskey := func(id, cred string) error {
		return e.exec(t, `INSERT INTO passkeys (id, user_id, credential_id, public_key, created_at) VALUES (?, 'u1', ?, ?, ?)`,
			id, []byte(cred), []byte("pk"), now)
	}
	if err := addPasskey("p1", "cred-a"); err != nil {
		t.Fatal(err)
	}
	if err := addPasskey("p2", "cred-a"); err == nil {
		t.Error("duplicate credential_id was accepted")
	}
	if n := e.count(t, `SELECT sign_count FROM passkeys WHERE id='p1'`); n != 0 {
		t.Errorf("default sign_count = %d, want 0", n)
	}
}

func TestDeletingUserCascadesButKeepsAudit(t *testing.T) {
	e := migratedEnv(t)
	e.addUser(t, "u1", "admin")
	e.addUser(t, "u2", "other")
	e.mustExec(t, `INSERT INTO sessions (id, token_hash, user_id, created_at, expires_at, last_seen_at) VALUES ('s1', x'01', 'u1', ?, ?, ?)`, now, now, now)
	e.mustExec(t, `INSERT INTO sessions (id, token_hash, user_id, created_at, expires_at, last_seen_at) VALUES ('s2', x'02', 'u2', ?, ?, ?)`, now, now, now)
	e.mustExec(t, `INSERT INTO passkeys (id, user_id, credential_id, public_key, created_at) VALUES ('p1', 'u1', x'aa', x'bb', ?)`, now)
	e.mustExec(t, `INSERT INTO audit_events (user_id, event_type, created_at) VALUES ('u1', 'login', ?)`, now)

	e.mustExec(t, `DELETE FROM users WHERE id = 'u1'`)

	if n := e.count(t, `SELECT COUNT(*) FROM sessions WHERE user_id='u1'`); n != 0 {
		t.Errorf("%d sessions survived the user", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM passkeys WHERE user_id='u1'`); n != 0 {
		t.Errorf("%d passkeys survived the user", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM sessions WHERE user_id='u2'`); n != 1 {
		t.Error("another user's session was deleted")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_events WHERE event_type='login' AND user_id IS NULL`); n != 1 {
		t.Error("the audit row must remain with user_id NULL")
	}
}

func TestAuditEventIDsAreMonotonic(t *testing.T) {
	e := migratedEnv(t)
	e.mustExec(t, `INSERT INTO audit_events (event_type, created_at) VALUES ('a', ?)`, now)
	e.mustExec(t, `INSERT INTO audit_events (event_type, created_at) VALUES ('b', ?)`, now)
	e.mustExec(t, `DELETE FROM audit_events`)
	e.mustExec(t, `INSERT INTO audit_events (event_type, created_at) VALUES ('c', ?)`, now)
	if id := e.count(t, `SELECT id FROM audit_events`); id != 3 {
		t.Errorf("id after delete = %d, want 3 (AUTOINCREMENT must never reuse ids)", id)
	}
}

// Upgrade path: a database created by the previous release (version 1, with
// data) must be backed up and then gain the auth tables without losing data.
func TestUpgradeFrom001KeepsDataAndBacksUp(t *testing.T) {
	e := newEnv(t)

	embedded, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	first, err := fs.ReadFile(embedded, "001_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	// What the previous release shipped: only 001.
	if err := e.migrate(fstest.MapFS{"001_foundation.sql": {Data: first}}); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, `INSERT INTO system_settings (key, value, updated_at) VALUES ('instance_name', 'home lab', ?)`, now)
	if e.tableExists(t, "users") {
		t.Fatal("precondition: users must not exist before 002")
	}

	if err := Migrate(context.Background(), e.db, e.backupDir, "0.2.0", quiet); err != nil {
		t.Fatal(err)
	}

	if got, want := e.versions(t), embeddedVersions(t); !equalInts(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}
	for _, table := range []string{"users", "sessions", "passkeys", "audit_events"} {
		if !e.tableExists(t, table) {
			t.Errorf("table %s was not created", table)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM system_settings WHERE key='instance_name' AND value='home lab'`); n != 1 {
		t.Error("existing data was lost in the upgrade")
	}

	backups := e.backups(t)
	if len(backups) != 1 || !strings.Contains(filepath.Base(backups[0]), "pre-migration-0.2.0-") {
		t.Fatalf("backups = %v, want one pre-migration-0.2.0-*.db", backups)
	}
	b, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var n int
	if err := b.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'users'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("backup must be the pre-upgrade state without users (n=%d err=%v)", n, err)
	}
	if err := b.QueryRow(`SELECT COUNT(*) FROM system_settings`).Scan(&n); err != nil || n != 1 {
		t.Errorf("backup lost the settings row (n=%d err=%v)", n, err)
	}
}

// spec/schema.sql is the consolidated reference; the migrations are
// authoritative. They must not drift for the tables this migration owns.
func TestAuthMigrationMatchesSpecSchema(t *testing.T) {
	raw, err := os.ReadFile("../../spec/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)

	e := migratedEnv(t)
	norm := func(s string) string {
		s = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(s, "")
		s = strings.Join(strings.Fields(s), " ")
		s = strings.ReplaceAll(s, "( ", "(")
		s = strings.ReplaceAll(s, " )", ")")
		return strings.TrimSuffix(strings.TrimSpace(s), ";")
	}

	for _, table := range []string{"users", "sessions", "passkeys", "audit_events"} {
		m := regexp.MustCompile(`(?s)CREATE TABLE ` + table + ` \(.*?\n\);`).FindString(spec)
		if m == "" {
			t.Fatalf("spec/schema.sql has no CREATE TABLE %s", table)
		}
		var live string
		if err := e.db.Writer.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if norm(live) != norm(m) {
			t.Errorf("table %s differs from spec/schema.sql:\n live: %s\n spec: %s", table, norm(live), norm(m))
		}
	}
	for _, idx := range []string{"idx_sessions_user", "idx_sessions_expiry", "idx_audit_events_time"} {
		m := regexp.MustCompile(`CREATE INDEX ` + idx + ` ON [^;]+;`).FindString(spec)
		var live string
		if err := e.db.Writer.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if m == "" || norm(live) != norm(m) {
			t.Errorf("index %s differs from spec/schema.sql:\n live: %s\n spec: %s", idx, norm(live), norm(m))
		}
	}
}
