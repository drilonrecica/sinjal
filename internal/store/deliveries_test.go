package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/vault"
)

func exec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Writer.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// profile inserts a notification profile and returns its id.
func profile(t *testing.T, d *db.DB, id string, quiet bool, start, end string, bypass bool) string {
	t.Helper()
	exec(t, d, `INSERT INTO notification_profiles (id, name, quiet_hours_enabled, quiet_start, quiet_end, critical_bypass, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, "profile "+id, b2i(quiet), nullStr(start), nullStr(end), b2i(bypass), formatTime(now), formatTime(now))
	return id
}

func webhookChannel(t *testing.T, d *db.DB, k *vault.Key, name string, enabled bool) string {
	t.Helper()
	id, err := CreateChannel(context.Background(), d, k, ChannelInput{Name: name, Enabled: enabled,
		Config: notify.Webhook{URL: "https://hooks.example.com/" + name}}, now)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// outage opens an incident for the monitor from two failures and returns
// its id.
func outage(t *testing.T, d *db.DB, monitorID string, started time.Time) string {
	t.Helper()
	ctx := context.Background()
	tx, err := d.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i, r := range []CheckResult{
		{Success: true, Duration: 74 * time.Millisecond},
		{Success: false, Duration: 5 * time.Second, ErrorKind: "timeout", ErrorMessage: "timeout after 5s"},
		{Success: false, Duration: 5 * time.Second, ErrorKind: "timeout", ErrorMessage: "timeout after 5s"},
	} {
		r.MonitorID, r.CheckedAt = monitorID, started.Add(time.Duration(i-1)*30*time.Second)
		if err := InsertCheckResult(ctx, tx, r); err != nil {
			t.Fatal(err)
		}
	}
	id, _, err := OpenIncident(ctx, tx, NewIncident{MonitorID: monitorID, StartedAt: started, DeclaredAt: started.Add(30 * time.Second),
		FailureKind: "timeout", Detected: "timeout after 5s", Summary: "timeout after 5s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestNotificationTarget(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	m, err := CreateMonitor(ctx, d, sample("api"), now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := GetNotificationTarget(ctx, d.Reader, m)
	if err != nil {
		t.Fatal(err)
	}
	if got.MonitorName != "api" || got.MonitorType != TypeHTTP || got.ProfileID != "" || !got.CriticalBypass {
		t.Errorf("without a profile: %+v", got)
	}

	p := profile(t, d, "p1", true, "23:00", "07:00", false)
	exp := now.Add(72 * time.Hour)
	exec(t, d, `UPDATE monitors SET notification_profile_id = ?, tls_not_after = ? WHERE id = ?`, p, formatTime(exp), m)
	got, err = GetNotificationTarget(ctx, d.Reader, m)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProfileID != p || !got.QuietEnabled || got.QuietStart != "23:00" || got.QuietEnd != "07:00" || got.CriticalBypass ||
		got.TLSNotAfter == nil || !got.TLSNotAfter.Equal(exp) {
		t.Errorf("with a profile: %+v", got)
	}
	if _, err := GetNotificationTarget(ctx, d.Reader, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing monitor: %v", err)
	}
}

func TestRouteChannels(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	p := profile(t, d, "p1", false, "", "", true)
	tg := webhookChannel(t, d, k, "Telegram", true)
	dc := webhookChannel(t, d, k, "discord", true)
	off := webhookChannel(t, d, k, "Off", false)
	for _, r := range [][2]string{{"critical", tg}, {"critical", dc}, {"critical", off}, {"info", dc}} {
		exec(t, d, `INSERT INTO notification_routes (profile_id, severity, channel_id) VALUES (?, ?, ?)`, p, r[0], r[1])
	}
	got, err := RouteChannels(ctx, d.Reader, p, "critical")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "discord" || got[0].ID != dc || got[1].Name != "Telegram" {
		t.Errorf("critical routes = %+v; want discord, Telegram (the disabled one left out)", got)
	}
	if got, _ := RouteChannels(ctx, d.Reader, p, "warning"); len(got) != 0 {
		t.Errorf("warning routes = %+v", got)
	}
	if got, _ := RouteChannels(ctx, d.Reader, "other", "info"); len(got) != 0 {
		t.Errorf("another profile's routes = %+v", got)
	}
}

func TestIncidentFactsAndLatency(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	m, _ := CreateMonitor(ctx, d, sample("api"), now)
	id := outage(t, d, m, now)

	f, err := GetIncidentFacts(ctx, d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if !f.StartedAt.Equal(now) || f.EndedAt != nil || f.Summary != "timeout after 5s" || f.Attempts != 2 {
		t.Errorf("facts = %+v", f)
	}
	if _, err := GetIncidentFacts(ctx, d.Reader, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing incident: %v", err)
	}
	// The last good latency before the outage; none after it yet.
	if l, ok, err := LastLatency(ctx, d.Reader, m, now); err != nil || !ok || l != 74*time.Millisecond {
		t.Errorf("latency before the outage = %v, %v, %v", l, ok, err)
	}
	if _, ok, err := LastLatency(ctx, d.Reader, m, now.Add(-time.Hour)); err != nil || ok {
		t.Errorf("latency before any check = %v, %v", ok, err)
	}
	tx, _ := d.Writer.BeginTx(ctx, nil)
	// A beat measures nothing and is no latency; the recovery check is.
	_ = InsertCheckResult(ctx, tx, CheckResult{MonitorID: m, CheckedAt: now.Add(100 * time.Second), Success: true, Duration: 51 * time.Millisecond})
	_ = InsertCheckResult(ctx, tx, CheckResult{MonitorID: m, CheckedAt: now.Add(130 * time.Second), Success: true})
	if _, _, err := CloseIncident(ctx, tx, m, now.Add(100*time.Second), incident.EventRecovered); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if l, ok, err := LastLatency(ctx, d.Reader, m, time.Time{}); err != nil || !ok || l != 51*time.Millisecond {
		t.Errorf("newest latency = %v, %v, %v", l, ok, err)
	}
	f, _ = GetIncidentFacts(ctx, d.Reader, id)
	if f.EndedAt == nil || !f.EndedAt.Equal(now.Add(100*time.Second)) {
		t.Errorf("ended = %v", f.EndedAt)
	}
	if ended, err := IncidentEnded(ctx, d.Reader, id); err != nil || !ended {
		t.Errorf("IncidentEnded = %v, %v", ended, err)
	}
	if ended, err := IncidentEnded(ctx, d.Reader, "gone"); err != nil || !ended {
		t.Errorf("IncidentEnded(gone) = %v, %v", ended, err)
	}
	id2 := outage(t, d, m, now.Add(time.Hour))
	if ended, _ := IncidentEnded(ctx, d.Reader, id2); ended {
		t.Error("an active incident reads as ended")
	}
}

func TestClaimNotificationOnce(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	m, _ := CreateMonitor(ctx, d, sample("api"), now)
	id := outage(t, d, m, now)
	for i, want := range []bool{true, false, false} {
		got, err := ClaimNotification(ctx, d, id, incident.IntentDown, now.Add(time.Duration(i)*time.Second))
		if err != nil || got != want {
			t.Errorf("claim %d = %v, %v; want %v", i, got, err, want)
		}
	}
	var at string
	if err := d.Reader.QueryRow(`SELECT down_notified_at FROM incidents WHERE id = ?`, id).Scan(&at); err != nil || at != formatTime(now) {
		t.Errorf("down_notified_at = %q, %v; want the first claim", at, err)
	}
	if got, err := ClaimNotification(ctx, d, id, incident.IntentRecovery, now); err != nil || !got {
		t.Errorf("recovery claim = %v, %v", got, err)
	}
	if got, err := ClaimNotification(ctx, d, "gone", incident.IntentDown, now); err != nil || got {
		t.Errorf("claim on a missing incident = %v, %v", got, err)
	}
}

func TestAddNotificationEvent(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	m, _ := CreateMonitor(ctx, d, sample("api"), now)
	id := outage(t, d, m, now)
	if err := AddNotificationEvent(ctx, d, id, incident.EventNotificationSuppressed, "down: quiet_hours", now); err != nil {
		t.Fatal(err)
	}
	if err := AddNotificationEvent(ctx, d, "gone", incident.EventNotificationSuppressed, "down: quiet_hours", now); err != nil {
		t.Fatal(err)
	}
	_, events, _ := GetIncident(ctx, d.Reader, id)
	if last := events[len(events)-1]; last.Type != incident.EventNotificationSuppressed || last.Message != "down: quiet_hours" {
		t.Errorf("last event = %+v", last)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incident_events WHERE incident_id = 'gone'`); n != 0 {
		t.Errorf("%d events for a missing incident", n)
	}
}

func TestRecordDeliveryHealth(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	m, _ := CreateMonitor(ctx, d, sample("api"), now)
	inc := outage(t, d, m, now)
	ch := webhookChannel(t, d, k, "Hook", true)
	health := func() Channel {
		t.Helper()
		c, err := GetChannelInfo(ctx, d.Reader, ch)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	attempt := func(n int, status string, final bool, errText string) DeliveryAttempt {
		return DeliveryAttempt{IncidentID: inc, ChannelID: ch, EventType: "down", Attempt: n, Status: status,
			At: now.Add(time.Duration(n) * time.Minute), Error: errText, Final: final, TimelineMessage: "down via Hook"}
	}

	if err := RecordDelivery(ctx, d, attempt(1, DeliveryFailed, false, "webhook: 500 Internal Server Error")); err != nil {
		t.Fatal(err)
	}
	if c := health(); c.HealthState != HealthWarning || c.LastFailureAt == nil || c.LastError != "webhook: 500 Internal Server Error" || c.LastSuccessAt != nil {
		t.Errorf("after one failure: %+v", c)
	}
	if err := RecordDelivery(ctx, d, attempt(2, DeliverySent, true, "")); err != nil {
		t.Fatal(err)
	}
	if c := health(); c.HealthState != HealthHealthy || c.LastSuccessAt == nil || !c.LastSuccessAt.Equal(now.Add(2*time.Minute)) || c.LastError == "" {
		t.Errorf("after a success: %+v (the last error stays readable)", c)
	}
	if err := RecordDelivery(ctx, d, attempt(4, DeliveryFailed, true, "webhook: timed out")); err != nil {
		t.Fatal(err)
	}
	if c := health(); c.HealthState != HealthFailed {
		t.Errorf("after giving up: %+v", c)
	}
	// A failed attempt of the next notification keeps "failed", a drop
	// changes nothing, a success heals.
	_ = RecordDelivery(ctx, d, attempt(1, DeliveryFailed, false, "webhook: timed out"))
	if c := health(); c.HealthState != HealthFailed {
		t.Errorf("failed then another failure: %s", c.HealthState)
	}
	_ = RecordDelivery(ctx, d, attempt(2, DeliveryDropped, true, "not sent again"))
	if c := health(); c.HealthState != HealthFailed {
		t.Errorf("after a drop: %s", c.HealthState)
	}
	_ = RecordDelivery(ctx, d, attempt(1, DeliverySent, true, ""))
	if c := health(); c.HealthState != HealthHealthy {
		t.Errorf("after healing: %s", c.HealthState)
	}

	// Rows: one per attempt, delivered_at only on a success.
	if n := count(t, d, `SELECT COUNT(*) FROM notification_deliveries WHERE channel_id = ? AND incident_id = ?`, ch, inc); n != 6 {
		t.Errorf("%d delivery rows, want 6", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notification_deliveries WHERE delivered_at IS NOT NULL`); n != 2 {
		t.Errorf("%d delivered rows, want 2", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notification_deliveries WHERE status = 'failed' AND error_message IS NULL`); n != 0 {
		t.Error("a failed row without its error")
	}
	// Timeline: only final attempts, sent or failed (a drop is failed).
	_, events, _ := GetIncident(ctx, d.Reader, inc)
	var got []string
	for _, e := range events {
		if e.Type == incident.EventNotificationSent || e.Type == incident.EventNotificationFailed {
			got = append(got, e.Type)
		}
	}
	want := []string{incident.EventNotificationSent, incident.EventNotificationFailed, incident.EventNotificationFailed, incident.EventNotificationSent}
	if len(got) != len(want) {
		t.Fatalf("timeline = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("timeline = %v, want %v", got, want)
		}
	}
}

func TestRecordDeliveryWithoutChannelOrIncident(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	k := testKey(t, d)
	ch := webhookChannel(t, d, k, "Hook", true)
	// A test send belongs to no incident.
	if err := RecordDelivery(ctx, d, DeliveryAttempt{ChannelID: ch, EventType: "test", Attempt: 1, Status: DeliverySent, At: now, Final: true}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notification_deliveries WHERE incident_id IS NULL`); n != 1 {
		t.Errorf("%d rows without an incident", n)
	}
	// An incident deleted meanwhile: the health still moves, no row.
	if err := RecordDelivery(ctx, d, DeliveryAttempt{IncidentID: "gone", ChannelID: ch, EventType: "down", Attempt: 1, Status: DeliveryFailed, At: now, Error: "x", Final: true}); err != nil {
		t.Fatal(err)
	}
	if c, _ := GetChannelInfo(ctx, d.Reader, ch); c.HealthState != HealthFailed {
		t.Errorf("health = %s", c.HealthState)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notification_deliveries`); n != 1 {
		t.Errorf("%d rows after a deleted incident, want 1", n)
	}
	// A channel deleted meanwhile: nothing is written.
	if err := RecordDelivery(ctx, d, DeliveryAttempt{ChannelID: "gone", EventType: "down", Attempt: 1, Status: DeliverySent, At: now}); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted channel: %v", err)
	}
}
