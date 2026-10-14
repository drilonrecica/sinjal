package db

import (
	"context"
	"database/sql"
	"testing"
)

// 009_themes maps every stored theme to the three Clear themes without
// touching the rows that reference users (the reason it avoids a table
// rebuild: see the migration's header).
func TestUpgradeTo009MapsThemesAndKeepsSessions(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(embeddedFiles(t, "001_foundation.sql", "002_auth.sql", "003_monitors.sql", "004_incidents.sql",
		"005_monitor_types.sql", "006_notifications.sql", "007_aggregates.sql", "008_status_pages.sql")); err != nil {
		t.Fatal(err)
	}
	old := map[string]string{"carbon": "stark", "midnight": "stark", "paper": "daylight", "terminal": "terminal"}
	for from := range old {
		e.mustExec(t, `INSERT INTO users (id, login, role, theme, created_at, updated_at) VALUES (?, ?, 'admin', ?, 'x', 'x')`, "u-"+from, "u-"+from, from)
		e.mustExec(t, `INSERT INTO sessions (id, token_hash, user_id, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, 'x', 'x', 'x')`, "s-"+from, "h-"+from, "u-"+from)
		e.mustExec(t, `INSERT INTO passkeys (id, user_id, credential_id, public_key, created_at) VALUES (?, ?, ?, x'00', 'x')`, "p-"+from, "u-"+from, "c-"+from)
		e.mustExec(t, `INSERT INTO status_pages (id, slug, title, visibility, theme, created_at, updated_at) VALUES (?, ?, 'S', 'public', ?, 'x', 'x')`, "sp-"+from, "sp-"+from, from)
	}
	e.mustExec(t, `INSERT INTO users (id, login, role, created_at, updated_at) VALUES ('u-default', 'u-default', 'viewer', 'x', 'x')`)

	if err := Migrate(context.Background(), e.db, e.backupDir, "0.9.0", quiet); err != nil {
		t.Fatal(err)
	}
	for from, to := range old {
		var user, page string
		var ambient int
		if err := e.db.Writer.QueryRow(`SELECT theme, ambient_light FROM users WHERE id=?`, "u-"+from).Scan(&user, &ambient); err != nil {
			t.Fatal(err)
		}
		if err := e.db.Writer.QueryRow(`SELECT theme FROM status_pages WHERE id=?`, "sp-"+from).Scan(&page); err != nil {
			t.Fatal(err)
		}
		if user != to || page != to || ambient != 1 {
			t.Errorf("%s: user %q page %q ambient %d, want %q, %q, 1", from, user, page, ambient, to, to)
		}
	}
	var def sql.NullString
	if err := e.db.Writer.QueryRow(`SELECT theme FROM users WHERE id='u-default'`).Scan(&def); err != nil || def.Valid {
		t.Errorf("a user without a theme keeps the instance default: %v %v", def, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM sessions`); n != len(old) {
		t.Errorf("sessions = %d after the migration, want %d", n, len(old))
	}
	if n := e.count(t, `SELECT COUNT(*) FROM passkeys`); n != len(old) {
		t.Errorf("passkeys = %d after the migration, want %d", n, len(old))
	}
	if n := e.count(t, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("%d foreign key violations", n)
	}
	if err := e.exec(t, `UPDATE users SET theme='paper' WHERE id='u-default'`); err == nil {
		t.Error("an old theme name is still accepted")
	}
}
