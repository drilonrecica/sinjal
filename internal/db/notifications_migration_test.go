package db

import (
	"context"
	"strings"
	"testing"
)

var notificationTables = []string{
	"notification_channels", "notification_routes", "notification_deliveries", "tls_warnings",
}

func (e env) addChannel(t *testing.T, id, typ string) {
	t.Helper()
	e.mustExec(t, `INSERT INTO notification_channels (id, name, type, config_enc, created_at, updated_at) VALUES (?, ?, ?, x'01', ?, ?)`, id, "c-"+id, typ, now, now)
}

func (e env) addProfile(t *testing.T, id string) {
	t.Helper()
	e.mustExec(t, `INSERT INTO notification_profiles (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`, id, "p-"+id, now, now)
}

func TestNotificationTablesExist(t *testing.T) {
	e := migratedEnv(t)
	for _, tb := range notificationTables {
		if !e.tableExists(t, tb) {
			t.Errorf("table %s is missing", tb)
		}
	}
}

func TestNotificationsMigrationMatchesSpecSchema(t *testing.T) {
	assertMatchesSpec(t, migratedEnv(t), notificationTables, nil)
}

func TestChannelDefaultsAndChecks(t *testing.T) {
	e := migratedEnv(t)
	e.addChannel(t, "c1", "smtp")
	var enabled int
	var health string
	var okAt, failAt, lastErr *string
	if err := e.db.Writer.QueryRow(`SELECT enabled, health_state, last_success_at, last_failure_at, last_error FROM notification_channels WHERE id='c1'`).
		Scan(&enabled, &health, &okAt, &failAt, &lastErr); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || health != "unknown" || okAt != nil || failAt != nil || lastErr != nil {
		t.Errorf("defaults = %d %q %v %v %v", enabled, health, okAt, failAt, lastErr)
	}
	for _, typ := range []string{"telegram", "discord", "webhook"} {
		e.addChannel(t, "ok-"+typ, typ)
	}
	if err := e.exec(t, `INSERT INTO notification_channels (id, name, type, config_enc, created_at, updated_at) VALUES ('bad', 'x', 'sms', x'01', ?, ?)`, now, now); err == nil {
		t.Error("an unknown channel type must be rejected")
	}
	if err := e.exec(t, `INSERT INTO notification_channels (id, name, type, created_at, updated_at) VALUES ('nocfg', 'x', 'smtp', ?, ?)`, now, now); err == nil {
		t.Error("a channel without config_enc must be rejected")
	}
}

func TestRouteChecksAndCascades(t *testing.T) {
	e := migratedEnv(t)
	e.addProfile(t, "p1")
	e.addChannel(t, "c1", "discord")
	e.addChannel(t, "c2", "telegram")
	e.mustExec(t, `INSERT INTO notification_routes (profile_id, severity, channel_id) VALUES ('p1', 'critical', 'c1'), ('p1', 'critical', 'c2')`)

	if err := e.exec(t, `INSERT INTO notification_routes (profile_id, severity, channel_id) VALUES ('p1', 'critical', 'c1')`); err == nil {
		t.Error("a route must be unique per profile, severity and channel")
	}
	if err := e.exec(t, `INSERT INTO notification_routes (profile_id, severity, channel_id) VALUES ('p1', 'fatal', 'c1')`); err == nil {
		t.Error("severity must be info, warning or critical")
	}
	if err := e.exec(t, `INSERT INTO notification_routes (profile_id, severity, channel_id) VALUES ('nope', 'info', 'c1')`); err == nil {
		t.Error("a route to an unknown profile must be rejected")
	}

	e.mustExec(t, `DELETE FROM notification_channels WHERE id='c1'`)
	if n := e.count(t, `SELECT COUNT(*) FROM notification_routes`); n != 1 {
		t.Errorf("routes after deleting a channel = %d, want 1", n)
	}
	e.mustExec(t, `DELETE FROM notification_profiles`)
	if n := e.count(t, `SELECT COUNT(*) FROM notification_routes`); n != 0 {
		t.Errorf("routes after deleting the profile = %d, want 0", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM notification_channels`); n != 1 {
		t.Errorf("deleting a profile must not delete channels, left %d", n)
	}
}

func TestDeliveriesCascadeAndKeepWithoutIncident(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	e.addIncident(t, "i1", "m1", nil)
	e.addChannel(t, "c1", "webhook")
	e.mustExec(t, `INSERT INTO notification_deliveries (id, incident_id, channel_id, event_type, attempt, status, attempted_at) VALUES ('d1', 'i1', 'c1', 'down', 1, 'sent', ?)`, now)
	e.mustExec(t, `INSERT INTO notification_deliveries (id, incident_id, channel_id, event_type, attempt, status, attempted_at) VALUES ('d2', NULL, 'c1', 'test', 1, 'sent', ?)`, now)
	if err := e.exec(t, `INSERT INTO notification_deliveries (id, channel_id, event_type, attempt, status, attempted_at) VALUES ('d3', 'nope', 'down', 1, 'sent', ?)`, now); err == nil {
		t.Error("a delivery for an unknown channel must be rejected")
	}

	e.mustExec(t, `DELETE FROM incidents`)
	if n := e.count(t, `SELECT COUNT(*) FROM notification_deliveries`); n != 1 {
		t.Errorf("deliveries after deleting the incident = %d, want 1 (the one without an incident)", n)
	}
	e.mustExec(t, `DELETE FROM notification_channels`)
	if n := e.count(t, `SELECT COUNT(*) FROM notification_deliveries`); n != 0 {
		t.Errorf("deliveries after deleting the channel = %d, want 0", n)
	}
}

func TestTLSWarningsKeyAndCascade(t *testing.T) {
	e := migratedEnv(t)
	e.addMonitor(t, "m1")
	ins := `INSERT INTO tls_warnings (monitor_id, cert_not_after, threshold_days, notified_at) VALUES (?, ?, ?, ?)`
	e.mustExec(t, ins, "m1", "2026-12-01T00:00:00Z", 14, now)
	e.mustExec(t, ins, "m1", "2026-12-01T00:00:00Z", 7, now)
	e.mustExec(t, ins, "m1", "2027-03-01T00:00:00Z", 14, now) // a renewed certificate starts over
	if err := e.exec(t, ins, "m1", "2026-12-01T00:00:00Z", 14, now); err == nil {
		t.Error("a threshold must be recorded once per certificate")
	}
	if err := e.exec(t, ins, "nope", "2026-12-01T00:00:00Z", 14, now); err == nil {
		t.Error("a warning for an unknown monitor must be rejected")
	}
	e.mustExec(t, `DELETE FROM monitors`)
	if n := e.count(t, `SELECT COUNT(*) FROM tls_warnings`); n != 0 {
		t.Errorf("tls_warnings kept %d rows after the monitor was deleted", n)
	}
}

func TestUpgradeFrom005KeepsDataAndBacksUp(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(embeddedFiles(t, "001_foundation.sql", "002_auth.sql", "003_monitors.sql", "004_incidents.sql", "005_monitor_types.sql")); err != nil {
		t.Fatal(err)
	}
	e.addMonitor(t, "m1")
	e.addProfile(t, "p1")
	if e.tableExists(t, "notification_channels") {
		t.Fatal("precondition: notification_channels must not exist before 006")
	}

	if err := Migrate(context.Background(), e.db, e.backupDir, "0.6.0", quiet); err != nil {
		t.Fatal(err)
	}
	if got, want := e.versions(t), embeddedVersions(t); !equalInts(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}
	for _, tb := range notificationTables {
		if !e.tableExists(t, tb) {
			t.Errorf("table %s was not created", tb)
		}
	}
	if e.count(t, `SELECT COUNT(*) FROM monitors WHERE id='m1'`) != 1 || e.count(t, `SELECT COUNT(*) FROM notification_profiles WHERE id='p1'`) != 1 {
		t.Error("existing data was lost in the upgrade")
	}
	backups := e.backups(t)
	if len(backups) != 1 || !strings.Contains(backups[0], "pre-migration-0.6.0-") {
		t.Fatalf("backups = %v", backups)
	}
}
