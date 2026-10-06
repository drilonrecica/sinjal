package db

import (
	"context"
	"strings"
	"testing"
)

var incidentTables = []string{"incidents", "incident_events", "maintenance_windows"}

var incidentIndexes = []string{"idx_incidents_monitor_time", "idx_incidents_active", "idx_incident_events_incident"}

func (e env) addIncident(t *testing.T, id, monitorID string, ended any) {
	t.Helper()
	e.mustExec(t, `INSERT INTO incidents (id, monitor_id, started_at, ended_at, created_at) VALUES (?, ?, ?, ?, ?)`, id, monitorID, now, ended, now)
}

func TestIncidentTablesAndIndexesExist(t *testing.T) {
	e := migratedEnv(t)
	for _, tb := range incidentTables {
		if !e.tableExists(t, tb) {
			t.Errorf("table %s is missing", tb)
		}
	}
	for _, idx := range incidentIndexes {
		if e.count(t, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx) != 1 {
			t.Errorf("index %s is missing", idx)
		}
	}
}

func TestIncidentMigrationMatchesSpecSchema(t *testing.T) {
	assertMatchesSpec(t, migratedEnv(t), incidentTables, incidentIndexes)
}

func TestOnlyOneActiveIncidentPerMonitor(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	e.addMonitor(t, "m2")
	e.addIncident(t, "i1", "m1", nil)
	if err := e.exec(t, `INSERT INTO incidents (id, monitor_id, started_at, created_at) VALUES ('i2', 'm1', ?, ?)`, now, now); err == nil {
		t.Error("a second active incident for the same monitor must be rejected")
	}
	e.addIncident(t, "i3", "m2", nil)
	e.mustExec(t, `UPDATE incidents SET ended_at = ? WHERE id = 'i1'`, now)
	e.addIncident(t, "i4", "m1", nil)
	e.addIncident(t, "i5", "m1", now)
	if n := e.count(t, `SELECT COUNT(*) FROM incidents WHERE monitor_id='m1'`); n != 3 {
		t.Errorf("resolved incidents must be kept as history, got %d rows", n)
	}
	if err := e.exec(t, `UPDATE incidents SET ended_at = NULL WHERE id = 'i5'`); err == nil {
		t.Error("reopening an incident next to an active one must be rejected")
	}
}

func TestIncidentDefaultsAndConstraints(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	e.addIncident(t, "i1", "m1", nil)
	if n := e.count(t, `SELECT COUNT(*) FROM incidents WHERE id='i1' AND suppressed_by_parent = 0 AND maintenance_overlap = 0
		AND down_notified_at IS NULL AND reminder_sent_at IS NULL AND recovery_notified_at IS NULL`); n != 1 {
		t.Error("a new incident must be unsuppressed and not notified")
	}
	if err := e.exec(t, `INSERT INTO incidents (id, monitor_id, started_at, created_at) VALUES ('x', 'ghost', ?, ?)`, now, now); err == nil {
		t.Error("an incident for a missing monitor must be rejected")
	}
	e.mustExec(t, `INSERT INTO incident_events (incident_id, event_type, created_at) VALUES ('i1', 'manual_note', ?)`, now)
	if n := e.count(t, `SELECT COUNT(*) FROM incident_events WHERE published = 0`); n != 1 {
		t.Error("a note must be unpublished by default")
	}
	if err := e.exec(t, `INSERT INTO incident_events (incident_id, event_type, created_at) VALUES ('ghost', 'detected', ?)`, now); err == nil {
		t.Error("an event for a missing incident must be rejected")
	}

	e.mustExec(t, `INSERT INTO maintenance_windows (id, name, starts_at, duration_seconds, created_at, updated_at) VALUES ('w1', 'deploy', ?, 3600, ?, ?)`, now, now, now)
	if n := e.count(t, `SELECT COUNT(*) FROM maintenance_windows WHERE recurrence = 'none' AND weekday_mask IS NULL
		AND suppress_notifications = 1 AND exclude_from_adjusted_uptime = 1 AND scope_json IS NULL`); n != 1 {
		t.Error("maintenance window defaults are wrong")
	}
	if err := e.exec(t, `INSERT INTO maintenance_windows (id, name, starts_at, duration_seconds, recurrence, created_at, updated_at) VALUES ('w2', 'x', ?, 60, 'monthly', ?, ?)`, now, now, now); err == nil {
		t.Error("unknown recurrence must be rejected")
	}
}

func TestDeletingMonitorCascadesToIncidents(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	e.addMonitor(t, "m2")
	e.addIncident(t, "i1", "m1", nil)
	e.addIncident(t, "i2", "m1", now)
	e.addIncident(t, "i3", "m2", nil)
	for _, id := range []string{"i1", "i2", "i3"} {
		e.mustExec(t, `INSERT INTO incident_events (incident_id, event_type, created_at) VALUES (?, 'detected', ?)`, id, now)
	}
	e.mustExec(t, `DELETE FROM monitors WHERE id = 'm1'`)
	if n := e.count(t, `SELECT COUNT(*) FROM incidents`); n != 1 {
		t.Errorf("incidents left: %d, want the other monitor's one", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM incident_events WHERE incident_id = 'i3'`); n != 1 {
		t.Error("the other monitor's events must survive")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM incident_events`); n != 1 {
		t.Errorf("events left: %d, want 1", n)
	}
}

// The two incident reads must use their indexes: a monitor's recent
// incidents (flapping, history) and one incident's timeline.
func TestIncidentQueriesUseIndexes(t *testing.T) {
	e := migratedEnv(t)
	for query, index := range map[string]string{
		`SELECT started_at, ended_at FROM incidents WHERE monitor_id='m1' ORDER BY started_at DESC LIMIT 4`: "idx_incidents_monitor_time",
		`SELECT event_type FROM incident_events WHERE incident_id='i1' ORDER BY id`:                         "idx_incident_events_incident",
		`UPDATE incidents SET ended_at='x' WHERE monitor_id='m1' AND ended_at IS NULL`:                      "idx_incidents_",
	} {
		rows, err := e.db.Writer.Query(`EXPLAIN QUERY PLAN ` + query)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var a, b, c int
			var d string
			if err := rows.Scan(&a, &b, &c, &d); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(d + "\n")
		}
		rows.Close()
		if !strings.Contains(plan.String(), index) || strings.Contains(plan.String(), "TEMP B-TREE") {
			t.Errorf("%s\nplan = %q", query, plan.String())
		}
	}
}

// A monitor that is DOWN when 004 is applied gets its active incident;
// nothing else does, and existing data and the backup rule hold.
func TestUpgradeFrom003BackfillsActiveIncidents(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(embeddedFiles(t, "001_foundation.sql", "002_auth.sql", "003_monitors.sql")); err != nil {
		t.Fatal(err)
	}
	const since = "2026-10-01T08:30:00Z"
	for id, state := range map[string]string{"down1": "down", "down2": "down", "up": "up", "pending": "pending", "paused": "paused"} {
		e.mustExec(t, `INSERT INTO monitors (id, name, type, current_state, current_state_since, created_at, updated_at) VALUES (?, ?, 'http', ?, ?, ?, ?)`,
			id, id, state, since, now, now)
	}
	e.mustExec(t, `INSERT INTO check_results (monitor_id, checked_at, success) VALUES ('down1', ?, 0)`, now)
	if e.tableExists(t, "incidents") {
		t.Fatal("precondition: incidents must not exist before 004")
	}

	if err := Migrate(context.Background(), e.db, e.backupDir, "0.4.0", quiet); err != nil {
		t.Fatal(err)
	}
	if got, want := e.versions(t), embeddedVersions(t); !equalInts(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM monitors`) + e.count(t, `SELECT COUNT(*) FROM check_results`); n != 6 {
		t.Error("existing data was lost in the upgrade")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM incidents`); n != 2 {
		t.Fatalf("incidents = %d, want one per DOWN monitor", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM incidents WHERE monitor_id IN ('down1','down2') AND ended_at IS NULL
		AND started_at = ? AND created_at = ? AND length(id) = 32 AND id = lower(id)`, since, since); n != 2 {
		t.Error("a backfilled incident must be active since the monitor went down, with a regular id")
	}
	if n := e.count(t, `SELECT COUNT(DISTINCT id) FROM incidents`); n != 2 {
		t.Error("backfilled ids must differ")
	}
	backups := e.backups(t)
	if len(backups) != 1 || !strings.Contains(backups[0], "pre-migration-0.4.0-") {
		t.Fatalf("backups = %v", backups)
	}
}
