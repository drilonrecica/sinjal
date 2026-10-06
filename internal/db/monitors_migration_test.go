package db

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

var monitorTables = []string{
	"notification_profiles", "monitors", "http_monitor_config", "monitor_secrets",
	"monitor_pauses", "tags", "monitor_tags", "check_results",
}

var monitorIndexes = []string{
	"idx_monitors_enabled", "idx_monitors_state", "idx_monitors_parent",
	"idx_monitor_pauses_monitor_time", "idx_monitor_pauses_open", "idx_check_results_monitor_time",
}

func (e env) addMonitor(t *testing.T, id string) {
	t.Helper()
	e.mustExec(t, `INSERT INTO monitors (id, name, type, current_state_since, created_at, updated_at) VALUES (?, ?, 'http', ?, ?, ?)`, id, "m-"+id, now, now, now)
}

func TestMonitorTablesAndIndexesExist(t *testing.T) {
	e := migratedEnv(t)
	for _, tb := range monitorTables {
		if !e.tableExists(t, tb) {
			t.Errorf("table %s is missing", tb)
		}
	}
	for _, idx := range monitorIndexes {
		if e.count(t, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx) != 1 {
			t.Errorf("index %s is missing", idx)
		}
	}
	// M4 tables must not exist yet.
	for _, tb := range []string{"tcp_monitor_config", "icmp_monitor_config", "dns_monitor_config", "heartbeat_monitor_config"} {
		if e.tableExists(t, tb) {
			t.Errorf("table %s belongs to a later milestone", tb)
		}
	}
}

func TestMonitorDefaults(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	var enabled, interval, timeout, failT, retry, succT int
	var state string
	err := e.db.Writer.QueryRow(`SELECT enabled, current_state, interval_seconds, timeout_ms, failure_threshold, retry_delay_ms, success_threshold FROM monitors WHERE id='m1'`).
		Scan(&enabled, &state, &interval, &timeout, &failT, &retry, &succT)
	if err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || state != "pending" || interval != 30 || timeout != 5000 || failT != 2 || retry != 5000 || succT != 1 {
		t.Errorf("defaults = %d %s %d %d %d %d %d", enabled, state, interval, timeout, failT, retry, succT)
	}

	e.mustExec(t, `INSERT INTO http_monitor_config (monitor_id, url) VALUES ('m1', 'https://example.com')`)
	var method, expected, warn string
	var maxBody, follow int
	if err := e.db.Writer.QueryRow(`SELECT method, expected_status, tls_warning_days_json, max_body_bytes, follow_redirects FROM http_monitor_config`).
		Scan(&method, &expected, &warn, &maxBody, &follow); err != nil {
		t.Fatal(err)
	}
	if method != "GET" || expected != "200-399" || warn != "[30,14,7]" || maxBody != 1048576 || follow != 1 {
		t.Errorf("http defaults = %s %s %s %d %d", method, expected, warn, maxBody, follow)
	}
}

func TestMonitorConstraints(t *testing.T) {
	e := migratedEnv(t)
	if err := e.exec(t, `INSERT INTO monitors (id, name, type, current_state_since, created_at, updated_at) VALUES ('x','x','smtp',?,?,?)`, now, now, now); err == nil {
		t.Error("unknown monitor type must be rejected")
	}
	if err := e.exec(t, `INSERT INTO monitors (id, name, type, current_state, current_state_since, created_at, updated_at) VALUES ('x','x','http','degraded',?,?,?)`, now, now, now); err == nil {
		t.Error("unknown state must be rejected (there is no DEGRADED)")
	}
	if err := e.exec(t, `INSERT INTO http_monitor_config (monitor_id, url) VALUES ('ghost', 'u')`); err == nil {
		t.Error("config for a missing monitor must be rejected")
	}
	e.mustExec(t, `INSERT INTO tags (id, name) VALUES ('t1', 'prod')`)
	if err := e.exec(t, `INSERT INTO tags (id, name) VALUES ('t2', 'prod')`); err == nil {
		t.Error("tag names must be unique")
	}
	e.mustExec(t, `INSERT INTO notification_profiles (id, name, created_at, updated_at) VALUES ('p1','default',?,?)`, now, now)
	if err := e.exec(t, `INSERT INTO notification_profiles (id, name, created_at, updated_at) VALUES ('p2','default',?,?)`, now, now); err == nil {
		t.Error("profile names must be unique")
	}
}

func TestOnlyOneOpenPausePerMonitor(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	e.addMonitor(t, "m2")
	e.mustExec(t, `INSERT INTO monitor_pauses (monitor_id, paused_at) VALUES ('m1', ?)`, now)
	if err := e.exec(t, `INSERT INTO monitor_pauses (monitor_id, paused_at) VALUES ('m1', ?)`, now); err == nil {
		t.Error("a second open pause for the same monitor must be rejected")
	}
	e.mustExec(t, `INSERT INTO monitor_pauses (monitor_id, paused_at) VALUES ('m2', ?)`, now)
	e.mustExec(t, `UPDATE monitor_pauses SET resumed_at = ? WHERE monitor_id = 'm1'`, now)
	e.mustExec(t, `INSERT INTO monitor_pauses (monitor_id, paused_at) VALUES ('m1', ?)`, now)
	if n := e.count(t, `SELECT COUNT(*) FROM monitor_pauses WHERE monitor_id='m1'`); n != 2 {
		t.Errorf("closed pauses must be kept as history, got %d rows", n)
	}
}

func TestDeletingMonitorCascadesAndDetachesRelations(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "parent")
	e.addMonitor(t, "child")
	e.mustExec(t, `UPDATE monitors SET parent_monitor_id='parent' WHERE id='child'`)
	e.mustExec(t, `INSERT INTO http_monitor_config (monitor_id, url) VALUES ('parent','u')`)
	e.mustExec(t, `INSERT INTO monitor_secrets (monitor_id, key, value_enc, updated_at) VALUES ('parent','k',x'01',?)`, now)
	e.mustExec(t, `INSERT INTO monitor_pauses (monitor_id, paused_at) VALUES ('parent',?)`, now)
	e.mustExec(t, `INSERT INTO tags (id, name) VALUES ('t1','prod')`)
	e.mustExec(t, `INSERT INTO monitor_tags (monitor_id, tag_id) VALUES ('parent','t1')`)
	e.mustExec(t, `INSERT INTO check_results (monitor_id, checked_at, success) VALUES ('parent',?,1)`, now)

	e.mustExec(t, `DELETE FROM monitors WHERE id='parent'`)

	for _, tb := range []string{"http_monitor_config", "monitor_secrets", "monitor_pauses", "monitor_tags", "check_results"} {
		if n := e.count(t, `SELECT COUNT(*) FROM `+tb); n != 0 {
			t.Errorf("%s kept %d rows after monitor delete", tb, n)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM tags`); n != 1 {
		t.Error("deleting a monitor must keep the tag")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM monitors WHERE id='child' AND parent_monitor_id IS NULL`); n != 1 {
		t.Error("child must survive with parent_monitor_id NULL")
	}

	// Deleting a profile detaches monitors rather than deleting them.
	e.mustExec(t, `INSERT INTO notification_profiles (id, name, created_at, updated_at) VALUES ('p1','d',?,?)`, now, now)
	e.mustExec(t, `UPDATE monitors SET notification_profile_id='p1' WHERE id='child'`)
	e.mustExec(t, `DELETE FROM notification_profiles WHERE id='p1'`)
	if n := e.count(t, `SELECT COUNT(*) FROM monitors WHERE id='child' AND notification_profile_id IS NULL`); n != 1 {
		t.Error("profile delete must set notification_profile_id NULL")
	}
}

func TestCheckResultsIDsAreMonotonic(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	for i := 0; i < 3; i++ {
		e.mustExec(t, `INSERT INTO check_results (monitor_id, checked_at, success) VALUES ('m1',?,1)`, now)
	}
	e.mustExec(t, `DELETE FROM check_results WHERE id = (SELECT MAX(id) FROM check_results)`)
	e.mustExec(t, `INSERT INTO check_results (monitor_id, checked_at, success) VALUES ('m1',?,0)`, now)
	if n := e.count(t, `SELECT COUNT(*) FROM check_results WHERE id = 3`); n != 0 {
		t.Error("AUTOINCREMENT must not reuse deleted ids")
	}
}

// The check_results hot query must use the (monitor_id, checked_at) index.
func TestCheckResultsQueryUsesIndex(t *testing.T) {
	e := migratedEnv(t)
	rows, err := e.db.Writer.Query(`EXPLAIN QUERY PLAN SELECT * FROM check_results WHERE monitor_id='m1' ORDER BY checked_at DESC LIMIT 10`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var a, b, c int
		var d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(d + "\n")
	}
	if !strings.Contains(plan.String(), "idx_check_results_monitor_time") || strings.Contains(plan.String(), "TEMP B-TREE") {
		t.Errorf("plan = %q", plan.String())
	}
}

// embeddedFiles returns the named embedded migrations: the schema of an
// earlier release, to upgrade from.
func embeddedFiles(t *testing.T, names ...string) fstest.MapFS {
	t.Helper()
	embedded, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, n := range names {
		b, err := fs.ReadFile(embedded, n)
		if err != nil {
			t.Fatal(err)
		}
		out[n] = &fstest.MapFile{Data: b}
	}
	return out
}

func TestUpgradeFrom002KeepsDataAndBacksUp(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(embeddedFiles(t, "001_foundation.sql", "002_auth.sql")); err != nil {
		t.Fatal(err)
	}
	e.addUser(t, "u1", "admin")
	if e.tableExists(t, "monitors") {
		t.Fatal("precondition: monitors must not exist before 003")
	}

	if err := Migrate(context.Background(), e.db, e.backupDir, "0.3.0", quiet); err != nil {
		t.Fatal(err)
	}
	if got, want := e.versions(t), embeddedVersions(t); !equalInts(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}
	for _, tb := range monitorTables {
		if !e.tableExists(t, tb) {
			t.Errorf("table %s was not created", tb)
		}
	}
	if e.count(t, `SELECT COUNT(*) FROM users WHERE id='u1'`) != 1 {
		t.Error("existing data was lost in the upgrade")
	}
	backups := e.backups(t)
	if len(backups) != 1 || !strings.Contains(backups[0], "pre-migration-0.3.0-") {
		t.Fatalf("backups = %v", backups)
	}
	b, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var n int
	if err := b.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='monitors'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("backup must predate 003 (n=%d err=%v)", n, err)
	}
}

func TestMonitorMigrationMatchesSpecSchema(t *testing.T) {
	assertMatchesSpec(t, migratedEnv(t), monitorTables, monitorIndexes)
}

// assertMatchesSpec compares the live definition of tables and indexes with
// spec/schema.sql, the consolidated reference: the two must not drift.
func assertMatchesSpec(t *testing.T, e env, tables, indexes []string) {
	t.Helper()
	raw, err := os.ReadFile("../../spec/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	norm := func(s string) string {
		s = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(s, "")
		s = strings.Join(strings.Fields(s), " ")
		s = strings.ReplaceAll(s, "( ", "(")
		s = strings.ReplaceAll(s, " )", ")")
		return strings.TrimSuffix(strings.TrimSpace(s), ";")
	}
	for _, table := range tables {
		m := regexp.MustCompile(`(?s)CREATE TABLE ` + table + ` \(.*?\n\);`).FindString(spec)
		if m == "" {
			t.Fatalf("spec/schema.sql has no CREATE TABLE %s", table)
		}
		var live string
		if err := e.db.Writer.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if norm(live) != norm(m) {
			t.Errorf("table %s differs from spec:\n live: %s\n spec: %s", table, norm(live), norm(m))
		}
	}
	for _, idx := range indexes {
		m := regexp.MustCompile(`CREATE (?:UNIQUE )?INDEX ` + idx + `\s+ON [^;]+;`).FindString(spec)
		var live string
		if err := e.db.Writer.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if m == "" || norm(live) != norm(m) {
			t.Errorf("index %s differs from spec:\n live: %s\n spec: %s", idx, norm(live), norm(m))
		}
	}
}
