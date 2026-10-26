package db

import (
	"context"
	"testing"
)

// 011_status_page_layouts: pages that existed under 010 become sentence, and
// the column accepts only the four layouts.
func TestUpgradeTo011GivesExistingPagesSentence(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(embeddedFiles(t, "001_foundation.sql", "002_auth.sql", "003_monitors.sql", "004_incidents.sql",
		"005_monitor_types.sql", "006_notifications.sql", "007_aggregates.sql", "008_status_pages.sql", "009_themes.sql",
		"010_saved_views.sql")); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, `INSERT INTO status_pages (id, slug, title, visibility, theme, created_at, updated_at) VALUES ('old', 'old', 'Old', 'public', 'stark', 'x', 'x')`)

	if err := Migrate(context.Background(), e.db, e.backupDir, "0.9.0", quiet); err != nil {
		t.Fatal(err)
	}
	var layout, theme string
	if err := e.db.Writer.QueryRow(`SELECT layout, theme FROM status_pages WHERE id='old'`).Scan(&layout, &theme); err != nil {
		t.Fatal(err)
	}
	if layout != "sentence" || theme != "stark" {
		t.Errorf("migrated page: layout %q theme %q, want sentence and stark", layout, theme)
	}
	e.mustExec(t, `INSERT INTO status_pages (id, slug, title, visibility, created_at, updated_at) VALUES ('new', 'new', 'New', 'public', 'x', 'x')`)
	if n := e.count(t, `SELECT COUNT(*) FROM status_pages WHERE layout = 'sentence'`); n != 2 {
		t.Errorf("%d pages on sentence, want 2 (a page inserted without a layout gets the default)", n)
	}
	for _, l := range []string{"signal", "heartline", "logbook"} {
		e.mustExec(t, `UPDATE status_pages SET layout = ? WHERE id = 'old'`, l)
	}
	if err := e.exec(t, `UPDATE status_pages SET layout = 'grid' WHERE id = 'old'`); err == nil {
		t.Error("an unknown layout is accepted")
	}
}
