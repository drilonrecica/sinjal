package db

import (
	"context"
	"strings"
	"testing"
)

func (e env) addStatusPage(t *testing.T, id, slug, visibility string) error {
	t.Helper()
	return e.exec(t, `INSERT INTO status_pages (id, slug, title, visibility, created_at, updated_at) VALUES (?, ?, 'Status', ?, '2026-10-07T00:00:00Z', '2026-10-07T00:00:00Z')`,
		id, slug, visibility)
}

var statusPageTables = []string{"status_pages", "status_page_groups", "status_page_monitors", "status_page_hosts"}

func TestStatusPageTablesExist(t *testing.T) {
	e := migratedEnv(t)
	for _, table := range statusPageTables {
		if !e.tableExists(t, table) {
			t.Errorf("table %s is missing", table)
		}
	}
}

func TestStatusPagesMigrationMatchesSpecSchema(t *testing.T) {
	assertMatchesSpec(t, migratedEnv(t), statusPageTables, nil)
}

func TestStatusPageSlugVisibilityAndDefaults(t *testing.T) {
	e := migratedEnv(t)
	for i, v := range []string{"public", "authenticated", "password", "unlisted"} {
		if err := e.addStatusPage(t, "p"+v, "slug-"+v, v); err != nil {
			t.Fatalf("visibility %s (%d): %v", v, i, err)
		}
	}
	if err := e.addStatusPage(t, "x", "slug-public", "public"); err == nil {
		t.Error("a slug must be unique")
	}
	if err := e.addStatusPage(t, "y", "other", "private"); err == nil {
		t.Error("an unknown visibility must be rejected")
	}
	var theme string
	var days, powered int
	if err := e.db.Writer.QueryRow(`SELECT theme, incident_days, show_powered_by FROM status_pages WHERE id='ppublic'`).Scan(&theme, &days, &powered); err != nil {
		t.Fatal(err)
	}
	if theme != "paper" || days != 30 || powered != 1 {
		t.Errorf("defaults = %q, %d, %d; want paper, 30, 1", theme, days, powered)
	}
	if err := e.exec(t, `INSERT INTO status_pages (id, slug, title, created_at, updated_at) VALUES ('z', 'z', 'Z', '', '')`); err == nil {
		t.Error("visibility must be required: a page has no default exposure")
	}
}

func TestStatusPageMonitorsGroupsAndHosts(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	e.addMonitor(t, "m2")
	if err := e.addStatusPage(t, "p1", "one", "public"); err != nil {
		t.Fatal(err)
	}
	if err := e.addStatusPage(t, "p2", "two", "public"); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, `INSERT INTO status_page_groups (id, status_page_id, name) VALUES ('g1', 'p1', 'Core')`)
	e.mustExec(t, `INSERT INTO status_page_monitors (status_page_id, monitor_id, group_id, display_name) VALUES ('p1', 'm1', 'g1', 'API')`)
	e.mustExec(t, `INSERT INTO status_page_monitors (status_page_id, monitor_id, display_name) VALUES ('p1', 'm2', 'Site')`)
	e.mustExec(t, `INSERT INTO status_page_monitors (status_page_id, monitor_id, display_name) VALUES ('p2', 'm1', 'API')`) // a monitor may be on several pages

	if err := e.exec(t, `INSERT INTO status_page_monitors (status_page_id, monitor_id, display_name) VALUES ('p1', 'm1', 'again')`); err == nil {
		t.Error("a monitor must appear once per page")
	}
	if err := e.exec(t, `INSERT INTO status_page_monitors (status_page_id, monitor_id) VALUES ('p2', 'm2')`); err == nil {
		t.Error("a public display name is required: no default exposes the internal name")
	}
	if err := e.exec(t, `INSERT INTO status_page_monitors (status_page_id, monitor_id, display_name) VALUES ('nope', 'm2', 'x')`); err == nil {
		t.Error("a mapping for an unknown page must be rejected")
	}
	var latency int
	if err := e.db.Writer.QueryRow(`SELECT show_latency FROM status_page_monitors WHERE status_page_id='p1' AND monitor_id='m1'`).Scan(&latency); err != nil || latency != 0 {
		t.Errorf("show_latency default = %d (%v), want 0: latency is opt-in", latency, err)
	}

	e.mustExec(t, `INSERT INTO status_page_hosts (hostname, status_page_id) VALUES ('status.example.com', 'p1')`)
	if err := e.exec(t, `INSERT INTO status_page_hosts (hostname, status_page_id) VALUES ('status.example.com', 'p2')`); err == nil {
		t.Error("a hostname must map to one page only")
	}

	// Deleting a group keeps its monitors, ungrouped.
	e.mustExec(t, `DELETE FROM status_page_groups WHERE id='g1'`)
	if n := e.count(t, `SELECT COUNT(*) FROM status_page_monitors WHERE status_page_id='p1' AND group_id IS NULL`); n != 2 {
		t.Errorf("ungrouped monitors after deleting the group = %d, want 2", n)
	}
	// Deleting a monitor removes it from every page.
	e.mustExec(t, `DELETE FROM monitors WHERE id='m1'`)
	if n := e.count(t, `SELECT COUNT(*) FROM status_page_monitors WHERE monitor_id='m1'`); n != 0 {
		t.Errorf("mappings of a deleted monitor = %d, want 0", n)
	}
	// Deleting a page removes its groups, mappings and hostnames.
	e.mustExec(t, `INSERT INTO status_page_groups (id, status_page_id, name) VALUES ('g2', 'p1', 'More')`)
	e.mustExec(t, `DELETE FROM status_pages WHERE id='p1'`)
	for _, table := range []string{"status_page_groups", "status_page_monitors", "status_page_hosts"} {
		if n := e.count(t, `SELECT COUNT(*) FROM `+table+` WHERE status_page_id='p1'`); n != 0 {
			t.Errorf("%s rows of a deleted page = %d, want 0", table, n)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM status_pages WHERE id='p2'`); n != 1 {
		t.Error("the other page was removed")
	}
}

func TestUpgradeFrom007KeepsDataAndBacksUp(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(embeddedFiles(t, "001_foundation.sql", "002_auth.sql", "003_monitors.sql", "004_incidents.sql", "005_monitor_types.sql", "006_notifications.sql", "007_aggregates.sql")); err != nil {
		t.Fatal(err)
	}
	e.addMonitor(t, "m1")
	e.addChannel(t, "c1", "discord")
	if e.tableExists(t, "status_pages") {
		t.Fatal("precondition: status_pages must not exist before 008")
	}

	if err := Migrate(context.Background(), e.db, e.backupDir, "0.8.0", quiet); err != nil {
		t.Fatal(err)
	}
	if got, want := e.versions(t), embeddedVersions(t); !equalInts(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}
	for _, table := range statusPageTables {
		if !e.tableExists(t, table) {
			t.Errorf("%s was not created", table)
		}
	}
	if e.count(t, `SELECT COUNT(*) FROM monitors WHERE id='m1'`) != 1 || e.count(t, `SELECT COUNT(*) FROM notification_channels WHERE id='c1'`) != 1 {
		t.Error("existing data was lost in the upgrade")
	}
	backups := e.backups(t)
	if len(backups) != 1 || !strings.Contains(backups[0], "pre-migration-0.8.0-") {
		t.Fatalf("backups = %v", backups)
	}
}
