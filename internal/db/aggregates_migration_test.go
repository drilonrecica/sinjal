package db

import (
	"context"
	"strings"
	"testing"
)

func (e env) addAggregate(t *testing.T, monitorID string, resolution int, bucket string) error {
	t.Helper()
	return e.exec(t, `INSERT INTO check_aggregates (monitor_id, resolution_seconds, bucket_start, total_count, success_count, failure_count, min_ms, max_ms, avg_ms, p95_ms) VALUES (?, ?, ?, 10, 9, 1, 1.5, 90, 20, 80)`,
		monitorID, resolution, bucket)
}

func TestAggregatesTableExists(t *testing.T) {
	if !migratedEnv(t).tableExists(t, "check_aggregates") {
		t.Error("table check_aggregates is missing")
	}
}

func TestAggregatesMigrationMatchesSpecSchema(t *testing.T) {
	assertMatchesSpec(t, migratedEnv(t), []string{"check_aggregates"}, nil)
}

func TestAggregateKeyAndChecks(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	for _, res := range []int{300, 3600, 86400} {
		if err := e.addAggregate(t, "m1", res, "2026-09-01T00:00:00Z"); err != nil {
			t.Fatalf("resolution %d: %v", res, err)
		}
	}
	if err := e.addAggregate(t, "m1", 300, "2026-09-01T00:00:00Z"); err == nil {
		t.Error("a bucket must be unique per monitor, resolution and start (the upsert key)")
	}
	if err := e.addAggregate(t, "nope", 300, "2026-09-01T00:00:00Z"); err == nil {
		t.Error("an aggregate for an unknown monitor must be rejected")
	}
	// Latency columns are optional: a bucket of only failed checks has none.
	e.mustExec(t, `INSERT INTO check_aggregates (monitor_id, resolution_seconds, bucket_start, total_count, success_count, failure_count) VALUES ('m1', 300, '2026-09-01T00:05:00Z', 3, 0, 3)`)
	if err := e.exec(t, `INSERT INTO check_aggregates (monitor_id, resolution_seconds, bucket_start, total_count, success_count) VALUES ('m1', 300, '2026-09-01T00:10:00Z', 3, 0)`); err == nil {
		t.Error("failure_count must be required")
	}
}

func TestAggregatesCascadeWithMonitor(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	e.addMonitor(t, "m2")
	for _, id := range []string{"m1", "m2"} {
		if err := e.addAggregate(t, id, 3600, "2026-09-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	e.mustExec(t, `DELETE FROM monitors WHERE id='m1'`)
	if n := e.count(t, `SELECT COUNT(*) FROM check_aggregates`); n != 1 {
		t.Errorf("aggregates after deleting a monitor = %d, want 1 (the other monitor's)", n)
	}
}

func TestUpgradeFrom006KeepsDataAndBacksUp(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(embeddedFiles(t, "001_foundation.sql", "002_auth.sql", "003_monitors.sql", "004_incidents.sql", "005_monitor_types.sql", "006_notifications.sql")); err != nil {
		t.Fatal(err)
	}
	e.addMonitor(t, "m1")
	e.addChannel(t, "c1", "discord")
	if e.tableExists(t, "check_aggregates") {
		t.Fatal("precondition: check_aggregates must not exist before 007")
	}

	if err := Migrate(context.Background(), e.db, e.backupDir, "0.7.0", quiet); err != nil {
		t.Fatal(err)
	}
	if got, want := e.versions(t), embeddedVersions(t); !equalInts(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}
	if !e.tableExists(t, "check_aggregates") {
		t.Error("check_aggregates was not created")
	}
	if e.count(t, `SELECT COUNT(*) FROM monitors WHERE id='m1'`) != 1 || e.count(t, `SELECT COUNT(*) FROM notification_channels WHERE id='c1'`) != 1 {
		t.Error("existing data was lost in the upgrade")
	}
	backups := e.backups(t)
	if len(backups) != 1 || !strings.Contains(backups[0], "pre-migration-0.7.0-") {
		t.Fatalf("backups = %v", backups)
	}
}
