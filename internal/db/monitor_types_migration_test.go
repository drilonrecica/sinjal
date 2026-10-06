package db

import (
	"context"
	"strings"
	"testing"
)

var monitorTypeTables = []string{
	"tcp_monitor_config", "icmp_monitor_config", "dns_monitor_config", "heartbeat_monitor_config",
}

func (e env) addTypedMonitor(t *testing.T, id, typ string) {
	t.Helper()
	e.mustExec(t, `INSERT INTO monitors (id, name, type, current_state_since, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, id, "m-"+id, typ, now, now, now)
}

func TestMonitorTypeTablesExist(t *testing.T) {
	e := migratedEnv(t)
	for _, tb := range monitorTypeTables {
		if !e.tableExists(t, tb) {
			t.Errorf("table %s is missing", tb)
		}
	}
}

func TestMonitorTypesMigrationMatchesSpecSchema(t *testing.T) {
	assertMatchesSpec(t, migratedEnv(t), monitorTypeTables, nil)
}

func TestMonitorTypeConfigDefaultsAndChecks(t *testing.T) {
	e := migratedEnv(t)
	e.addTypedMonitor(t, "d", "dns")
	e.addTypedMonitor(t, "h", "heartbeat")

	e.mustExec(t, `INSERT INTO dns_monitor_config (monitor_id, hostname, query_type) VALUES ('d', 'example.com', 'A')`)
	var mode string
	if err := e.db.Writer.QueryRow(`SELECT match_mode FROM dns_monitor_config WHERE monitor_id='d'`).Scan(&mode); err != nil || mode != "all" {
		t.Errorf("match_mode default = %q, %v", mode, err)
	}
	if err := e.exec(t, `UPDATE dns_monitor_config SET match_mode='some' WHERE monitor_id='d'`); err == nil {
		t.Error("match_mode must be any or all")
	}

	e.mustExec(t, `INSERT INTO heartbeat_monitor_config (monitor_id, token_hash, expected_interval_seconds) VALUES ('h', x'01', 60)`)
	var grace int
	var label, last *string
	if err := e.db.Writer.QueryRow(`SELECT grace_seconds, source_label, last_beat_at FROM heartbeat_monitor_config WHERE monitor_id='h'`).Scan(&grace, &label, &last); err != nil {
		t.Fatal(err)
	}
	if grace != 0 || label != nil || last != nil {
		t.Errorf("heartbeat defaults = %d %v %v", grace, label, last)
	}
}

func TestHeartbeatTokenHashIsUnique(t *testing.T) {
	e := migratedEnv(t)
	e.addTypedMonitor(t, "h1", "heartbeat")
	e.addTypedMonitor(t, "h2", "heartbeat")
	e.mustExec(t, `INSERT INTO heartbeat_monitor_config (monitor_id, token_hash, expected_interval_seconds) VALUES ('h1', x'aa', 60)`)
	if err := e.exec(t, `INSERT INTO heartbeat_monitor_config (monitor_id, token_hash, expected_interval_seconds) VALUES ('h2', x'aa', 60)`); err == nil {
		t.Error("two monitors must not share a token hash")
	}
}

func TestMonitorTypeConfigCascadesOnDelete(t *testing.T) {
	e := migratedEnv(t)
	e.addTypedMonitor(t, "t", "tcp")
	e.addTypedMonitor(t, "i", "icmp")
	e.addTypedMonitor(t, "d", "dns")
	e.addTypedMonitor(t, "h", "heartbeat")
	e.mustExec(t, `INSERT INTO tcp_monitor_config (monitor_id, host, port) VALUES ('t', 'localhost', 22)`)
	e.mustExec(t, `INSERT INTO icmp_monitor_config (monitor_id, host) VALUES ('i', 'localhost')`)
	e.mustExec(t, `INSERT INTO dns_monitor_config (monitor_id, hostname, query_type) VALUES ('d', 'example.com', 'A')`)
	e.mustExec(t, `INSERT INTO heartbeat_monitor_config (monitor_id, token_hash, expected_interval_seconds) VALUES ('h', x'01', 60)`)

	e.mustExec(t, `DELETE FROM monitors`)
	for _, tb := range monitorTypeTables {
		if n := e.count(t, `SELECT COUNT(*) FROM `+tb); n != 0 {
			t.Errorf("%s kept %d rows after the monitor was deleted", tb, n)
		}
	}
}

func TestMonitorTypeConfigRequiresMonitor(t *testing.T) {
	e := migratedEnv(t)
	if err := e.exec(t, `INSERT INTO tcp_monitor_config (monitor_id, host, port) VALUES ('nope', 'localhost', 22)`); err == nil {
		t.Error("config for an unknown monitor must be rejected")
	}
}

func TestUpgradeFrom004KeepsDataAndBacksUp(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(embeddedFiles(t, "001_foundation.sql", "002_auth.sql", "003_monitors.sql", "004_incidents.sql")); err != nil {
		t.Fatal(err)
	}
	e.addMonitor(t, "m1")
	if e.tableExists(t, "tcp_monitor_config") {
		t.Fatal("precondition: tcp_monitor_config must not exist before 005")
	}

	if err := Migrate(context.Background(), e.db, e.backupDir, "0.5.0", quiet); err != nil {
		t.Fatal(err)
	}
	if got, want := e.versions(t), embeddedVersions(t); !equalInts(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}
	for _, tb := range monitorTypeTables {
		if !e.tableExists(t, tb) {
			t.Errorf("table %s was not created", tb)
		}
	}
	if e.count(t, `SELECT COUNT(*) FROM monitors WHERE id='m1'`) != 1 {
		t.Error("existing data was lost in the upgrade")
	}
	backups := e.backups(t)
	if len(backups) != 1 || !strings.Contains(backups[0], "pre-migration-0.5.0-") {
		t.Fatalf("backups = %v", backups)
	}
}
