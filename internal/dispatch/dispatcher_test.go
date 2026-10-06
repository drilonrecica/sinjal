package dispatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/vault"
)

var base = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// env is a database with a fake network: sends are recorded, and fail as
// the test says, per channel.
type env struct {
	t   *testing.T
	d   *db.DB
	key *vault.Key
	log *slog.Logger

	mu       sync.Mutex
	sent     []send             // every attempt, in order
	fail     map[string][]error // channel name -> error per attempt (nil: success); beyond: success
	inflight atomic.Int32
	peak     atomic.Int32
	block    chan struct{} // when set, sends wait on it
	updated  []string      // incident ids announced
	channels []string      // channel ids announced
}

type send struct {
	channel string // the channel's name, from its URL
	msg     notify.Message
	err     error
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "backups"), "test", quiet); err != nil {
		t.Fatal(err)
	}
	key, err := vault.LoadOrCreate(context.Background(), dir, d.Reader, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, d: d, key: key, log: quiet, fail: map[string][]error{}}
}

// run starts a dispatcher with millisecond retries and the fake sender.
func (e *env) run(loc *time.Location) *Dispatcher {
	e.t.Helper()
	disp := New(e.d, e.key, loc,
		func(incidentID, _ string) { e.mu.Lock(); e.updated = append(e.updated, incidentID); e.mu.Unlock() },
		func(channelID string) { e.mu.Lock(); e.channels = append(e.channels, channelID); e.mu.Unlock() },
		e.log)
	disp.send = e.send
	disp.delays = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { disp.Run(ctx); close(done) }()
	e.t.Cleanup(func() { cancel(); <-done })
	return disp
}

func (e *env) send(ctx context.Context, c notify.Config, m notify.Message) error {
	name := c.(notify.Webhook).URL[strings.LastIndex(c.(notify.Webhook).URL, "/")+1:]
	n := e.inflight.Add(1)
	defer e.inflight.Add(-1)
	for {
		p := e.peak.Load()
		if n <= p || e.peak.CompareAndSwap(p, n) {
			break
		}
	}
	e.mu.Lock()
	block := e.block
	attempt := 0
	for _, s := range e.sent {
		if s.channel == name {
			attempt++
		}
	}
	var err error
	if errs := e.fail[name]; attempt < len(errs) {
		err = errs[attempt]
	}
	e.sent = append(e.sent, send{channel: name, msg: m, err: err})
	e.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return errors.New("webhook: cancelled")
		}
	}
	return err
}

func (e *env) sends() []send {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]send(nil), e.sent...)
}

// wait polls until cond holds.
func (e *env) wait(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s; sends %+v", what, e.sends())
		}
		time.Sleep(time.Millisecond)
	}
}

func (e *env) sentCount(n int) func() bool {
	return func() bool { return len(e.sends()) >= n }
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.d.Writer.Exec(q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.Reader.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) monitor(name, profileID string) string {
	e.t.Helper()
	id, err := store.CreateMonitor(context.Background(), e.d, store.MonitorInput{Name: name, Enabled: true,
		HTTP: store.HTTPConfig{URL: "https://example.com/" + name, FollowRedirects: true}}, base)
	if err != nil {
		e.t.Fatal(err)
	}
	if profileID != "" {
		e.exec(`UPDATE monitors SET notification_profile_id = ? WHERE id = ?`, profileID, id)
	}
	return id
}

func (e *env) profile(id string, quiet bool, start, end string, bypass bool) string {
	e.t.Helper()
	e.exec(`INSERT INTO notification_profiles (id, name, quiet_hours_enabled, quiet_start, quiet_end, critical_bypass, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, "profile "+id, quiet, start, end, bypass, store.FormatTime(base), store.FormatTime(base))
	return id
}

func (e *env) channel(name string, enabled bool) string {
	e.t.Helper()
	id, err := store.CreateChannel(context.Background(), e.d, e.key, store.ChannelInput{Name: name, Enabled: enabled,
		Config: notify.Webhook{URL: "https://hooks.example.com/" + name}}, base)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) route(profile, channel string, severities ...string) {
	e.t.Helper()
	for _, s := range severities {
		e.exec(`INSERT INTO notification_routes (profile_id, severity, channel_id) VALUES (?, ?, ?)`, profile, s, channel)
	}
}

// setup is the usual case: a monitor with a profile routing every severity
// to the channel "hook".
func (e *env) setup() (monitorID, channelID string) {
	e.t.Helper()
	p := e.profile("p", false, "", "", true)
	ch := e.channel("hook", true)
	e.route(p, ch, "info", "warning", "critical")
	return e.monitor("API", p), ch
}

// outage opens an incident at base from a good check and two failures.
func (e *env) outage(monitorID string) string {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.d.Writer.BeginTx(ctx, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	defer tx.Rollback()
	for i, r := range []store.CheckResult{
		{Success: true, Duration: 74 * time.Millisecond},
		{Success: false, Duration: 5 * time.Second, ErrorMessage: "timeout after 5s"},
		{Success: false, Duration: 5 * time.Second, ErrorMessage: "timeout after 5s"},
	} {
		r.MonitorID, r.CheckedAt = monitorID, base.Add(time.Duration(i-1)*30*time.Second)
		if err := store.InsertCheckResult(ctx, tx, r); err != nil {
			e.t.Fatal(err)
		}
	}
	id, _, err := store.OpenIncident(ctx, tx, store.NewIncident{MonitorID: monitorID, StartedAt: base, DeclaredAt: base.Add(30 * time.Second),
		FailureKind: "timeout", Detected: "timeout after 5s", Summary: "timeout after 5s"})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) recover(monitorID string, at time.Time) {
	e.t.Helper()
	ctx := context.Background()
	tx, _ := e.d.Writer.BeginTx(ctx, nil)
	defer tx.Rollback()
	_ = store.InsertCheckResult(ctx, tx, store.CheckResult{MonitorID: monitorID, CheckedAt: at, Success: true, Duration: 51 * time.Millisecond})
	if _, _, err := store.CloseIncident(ctx, tx, monitorID, at, incident.EventRecovered); err != nil {
		e.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) events(incidentID string) []store.IncidentEvent {
	e.t.Helper()
	_, events, err := store.GetIncident(context.Background(), e.d.Reader, incidentID)
	if err != nil {
		e.t.Fatal(err)
	}
	return events
}

func (e *env) health(channelID string) store.Channel {
	e.t.Helper()
	c, err := store.GetChannelInfo(context.Background(), e.d.Reader, channelID)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func down(monitorID, incidentID string) incident.Intent {
	return incident.Intent{Kind: incident.IntentDown, MonitorID: monitorID, IncidentID: incidentID, At: base.Add(30 * time.Second)}
}

func TestDownIsRenderedAndDelivered(t *testing.T) {
	e := newEnv(t)
	m, ch := e.setup()
	inc := e.outage(m)
	disp := e.run(time.UTC)

	disp.Enqueue(down(m, inc))
	e.wait("the send", e.sentCount(1))
	got := e.sends()[0]
	want := "API is DOWN\nReason: timeout after 5s\nFailed at: 2026-10-06 12:00:00 UTC\nAttempts: 2\nLast latency: 74 ms"
	if got.channel != "hook" || got.msg.Text() != want {
		t.Errorf("sent to %s:\n%s\nwant:\n%s", got.channel, got.msg.Text(), want)
	}
	if got.msg.Event.IncidentID != inc || !got.msg.Event.IncidentStart.Equal(base) {
		t.Errorf("event = %+v", got.msg.Event)
	}

	e.wait("the record", func() bool { return e.count(`SELECT COUNT(*) FROM notification_deliveries`) == 1 })
	var status, eventType string
	var attempt int
	var delivered, errMsg *string
	if err := e.d.Reader.QueryRow(`SELECT status, event_type, attempt, delivered_at, error_message FROM notification_deliveries
		WHERE incident_id = ? AND channel_id = ?`, inc, ch).Scan(&status, &eventType, &attempt, &delivered, &errMsg); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || eventType != "down" || attempt != 1 || delivered == nil || errMsg != nil {
		t.Errorf("row = %s %s %d %v %v", status, eventType, attempt, delivered, errMsg)
	}
	if c := e.health(ch); c.HealthState != store.HealthHealthy || c.LastSuccessAt == nil {
		t.Errorf("health = %+v", c)
	}
	events := e.events(inc)
	if last := events[len(events)-1]; last.Type != incident.EventNotificationSent || last.Message != "down via hook" {
		t.Errorf("last event = %+v", last)
	}
	if n := e.count(`SELECT COUNT(*) FROM incidents WHERE id = ? AND down_notified_at IS NOT NULL`, inc); n != 1 {
		t.Error("down_notified_at not set")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.updated) != 1 || e.updated[0] != inc || len(e.channels) != 1 || e.channels[0] != ch {
		t.Errorf("announced incidents %v, channels %v", e.updated, e.channels)
	}
}

func TestRecoveryMessage(t *testing.T) {
	e := newEnv(t)
	m, _ := e.setup()
	inc := e.outage(m)
	e.recover(m, base.Add(257*time.Second))
	disp := e.run(time.UTC)

	disp.Enqueue(incident.Intent{Kind: incident.IntentRecovery, MonitorID: m, IncidentID: inc, At: base.Add(257 * time.Second)})
	e.wait("the send", e.sentCount(1))
	if got, want := e.sends()[0].msg.Text(), "API recovered\nDowntime: 4m 17s\nCurrent latency: 51 ms"; got != want {
		t.Errorf("message:\n%s\nwant:\n%s", got, want)
	}
	e.wait("the claim", func() bool {
		return e.count(`SELECT COUNT(*) FROM incidents WHERE id = ? AND recovery_notified_at IS NOT NULL`, inc) == 1
	})
}

func TestSeverityRouting(t *testing.T) {
	e := newEnv(t)
	p := e.profile("p", false, "", "", true)
	info, warning, critical := e.channel("info", true), e.channel("warning", true), e.channel("critical", true)
	e.route(p, info, "info")
	e.route(p, warning, "warning")
	e.route(p, critical, "critical", "warning")
	m := e.monitor("API", p)
	inc := e.outage(m)
	disp := e.run(time.UTC)

	disp.Enqueue(incident.Intent{Kind: incident.IntentStable, MonitorID: m, At: base})
	disp.Enqueue(incident.Intent{Kind: incident.IntentFlapping, MonitorID: m, IncidentID: inc, At: base})
	disp.Enqueue(down(m, inc))
	e.wait("four sends", e.sentCount(4))
	time.Sleep(20 * time.Millisecond)
	got := map[string]string{}
	for _, s := range e.sends() {
		got[string(s.msg.Event.Kind)+"->"+s.channel] = s.msg.Title
	}
	want := map[string]string{"stable->info": "API is stable again", "flapping->warning": "API is flapping",
		"flapping->critical": "API is flapping", "down->critical": "API is DOWN"}
	if len(got) != len(want) {
		t.Fatalf("sends = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("sends = %v, want %v", got, want)
		}
	}
	// A notification without an incident leaves no timeline entry and a
	// row without an incident.
	if n := e.count(`SELECT COUNT(*) FROM notification_deliveries WHERE incident_id IS NULL AND event_type = 'stable'`); n != 1 {
		t.Errorf("%d rows for the stable notice", n)
	}
}

func TestNothingToSend(t *testing.T) {
	e := newEnv(t)
	noProfile := e.monitor("A", "")
	p := e.profile("p", false, "", "", true)
	noRoute := e.monitor("B", p)
	q := e.profile("q", false, "", "", true)
	off := e.channel("off", false)
	e.route(q, off, "critical")
	disabledOnly := e.monitor("C", q)
	disp := e.run(time.UTC)

	for _, m := range []string{noProfile, noRoute, disabledOnly, "gone"} {
		disp.Enqueue(down(m, e.outage(noProfile)))
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(e.sends()); n != 0 {
		t.Errorf("%d sends", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM notification_deliveries`); n != 0 {
		t.Errorf("%d rows", n)
	}
	// Nothing to send is not a claim: a DOWN that was never dispatched
	// stays unclaimed.
	if n := e.count(`SELECT COUNT(*) FROM incidents WHERE down_notified_at IS NOT NULL`); n != 0 {
		t.Errorf("%d claimed incidents", n)
	}
}

func TestSuppressedIntentIsNotDelivered(t *testing.T) {
	e := newEnv(t)
	m, _ := e.setup()
	inc := e.outage(m)
	disp := e.run(time.UTC)
	for _, reason := range []incident.Reason{incident.ByFlapping, incident.ByParent, incident.ByMaintenance} {
		in := down(m, inc)
		in.Suppressed = reason
		disp.Enqueue(in)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(e.sends()); n != 0 {
		t.Errorf("%d sends for suppressed intents", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM incidents WHERE down_notified_at IS NOT NULL`); n != 0 {
		t.Error("a suppressed DOWN was claimed")
	}
	// The same DOWN, decided after all (the catch-up), goes out once.
	disp.Enqueue(down(m, inc))
	e.wait("the send", e.sentCount(1))
}

func TestQuietHours(t *testing.T) {
	belgrade, _ := time.LoadLocation("Europe/Belgrade")
	// 22:30 UTC is 00:30 in Belgrade: inside 23:00-07:00 there, not in UTC.
	night := time.Date(2026, 10, 6, 22, 30, 0, 0, time.UTC)
	day := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)

	for _, c := range []struct {
		name   string
		bypass bool
		kind   incident.IntentKind
		at     time.Time
		loc    *time.Location
		sent   bool
	}{
		{"info at night is held", true, incident.IntentRecovery, night, belgrade, false},
		{"warning at night is held", true, incident.IntentFlapping, night, belgrade, false},
		{"critical bypasses", true, incident.IntentDown, night, belgrade, true},
		{"critical without bypass is held", false, incident.IntentDown, night, belgrade, false},
		{"by day everything goes", false, incident.IntentRecovery, day, belgrade, true},
		{"read in the instance zone", true, incident.IntentRecovery, night, time.UTC, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			p := e.profile("p", true, "23:00", "07:00", c.bypass)
			ch := e.channel("hook", true)
			e.route(p, ch, "info", "warning", "critical")
			m := e.monitor("API", p)
			inc := e.outage(m)
			disp := e.run(c.loc)
			disp.Enqueue(incident.Intent{Kind: c.kind, MonitorID: m, IncidentID: inc, At: c.at})
			if c.sent {
				e.wait("the send", e.sentCount(1))
				return
			}
			e.wait("the suppression event", func() bool {
				return e.count(`SELECT COUNT(*) FROM incident_events WHERE incident_id = ? AND event_type = ? AND message = ?`,
					inc, incident.EventNotificationSuppressed, string(c.kind)+": quiet_hours") == 1
			})
			time.Sleep(20 * time.Millisecond)
			if n := len(e.sends()); n != 0 {
				t.Errorf("%d sends", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM incidents WHERE down_notified_at IS NOT NULL OR recovery_notified_at IS NOT NULL`); n != 0 {
				t.Error("a held notification was claimed")
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			if len(e.updated) != 1 {
				t.Errorf("incident announcements = %v", e.updated)
			}
		})
	}
}

func TestOneDownPerIncidentAcrossRestarts(t *testing.T) {
	e := newEnv(t)
	m, _ := e.setup()
	inc := e.outage(m)
	disp := e.run(time.UTC)
	disp.Enqueue(down(m, inc))
	disp.Enqueue(down(m, inc))
	e.wait("the send", e.sentCount(1))
	time.Sleep(30 * time.Millisecond)
	if n := len(e.sends()); n != 1 {
		t.Fatalf("%d sends for one incident", n)
	}
	// Another dispatcher on the same database, as after a restart.
	again := e.run(time.UTC)
	again.Enqueue(down(m, inc))
	time.Sleep(30 * time.Millisecond)
	if n := len(e.sends()); n != 1 {
		t.Errorf("%d sends after the restart", n)
	}
	// A new incident gets its own.
	e.recover(m, base.Add(time.Minute))
	inc2 := e.outage2(m, base.Add(2*time.Minute))
	again.Enqueue(down(m, inc2))
	e.wait("the second incident's send", e.sentCount(2))
}

// outage2 opens another incident for the monitor at the given time.
func (e *env) outage2(monitorID string, at time.Time) string {
	e.t.Helper()
	ctx := context.Background()
	tx, _ := e.d.Writer.BeginTx(ctx, nil)
	defer tx.Rollback()
	id, _, err := store.OpenIncident(ctx, tx, store.NewIncident{MonitorID: monitorID, StartedAt: at, DeclaredAt: at, Summary: "refused"})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestRetryLadderToSuccess(t *testing.T) {
	e := newEnv(t)
	m, ch := e.setup()
	inc := e.outage(m)
	e.fail["hook"] = []error{errors.New("webhook: 502 Bad Gateway"), errors.New("webhook: timed out")}
	disp := e.run(time.UTC)
	disp.Enqueue(down(m, inc))
	e.wait("three attempts", e.sentCount(3))
	e.wait("the records", func() bool { return e.count(`SELECT COUNT(*) FROM notification_deliveries`) == 3 })

	rows, err := e.d.Reader.Query(`SELECT attempt, status, COALESCE(error_message, '') FROM notification_deliveries ORDER BY attempt`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n int
		var s, m string
		_ = rows.Scan(&n, &s, &m)
		got = append(got, strings.TrimSpace(strings.Join([]string{string(rune('0' + n)), s, m}, " ")))
	}
	want := []string{"1 failed webhook: 502 Bad Gateway", "2 failed webhook: timed out", "3 sent"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows = %v, want %v", got, want)
	}
	if c := e.health(ch); c.HealthState != store.HealthHealthy || c.LastFailureAt == nil || c.LastError != "webhook: timed out" {
		t.Errorf("health = %+v", c)
	}
	var sent, failed int
	for _, ev := range e.events(inc) {
		switch ev.Type {
		case incident.EventNotificationSent:
			sent++
		case incident.EventNotificationFailed:
			failed++
		}
	}
	if sent != 1 || failed != 0 {
		t.Errorf("timeline: %d sent, %d failed", sent, failed)
	}
	// Every attempt sends the same message.
	s := e.sends()
	if s[0].msg.Text() != s[2].msg.Text() {
		t.Error("the retry rendered a different message")
	}
}

func TestRetryLadderToFailure(t *testing.T) {
	e := newEnv(t)
	m, ch := e.setup()
	inc := e.outage(m)
	e.fail["hook"] = []error{errors.New("e1"), errors.New("e2"), errors.New("e3"), errors.New("e4"), errors.New("e5")}
	disp := e.run(time.UTC)
	start := time.Now()
	disp.Enqueue(down(m, inc))
	e.wait("the final event", func() bool {
		return e.count(`SELECT COUNT(*) FROM incident_events WHERE incident_id = ? AND event_type = ?`, inc, incident.EventNotificationFailed) == 1
	})
	if took := time.Since(start); took < 60*time.Millisecond {
		t.Errorf("four attempts took %s, less than the 10+20+30 ms of waits", took)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(e.sends()); n != 4 {
		t.Errorf("%d attempts, want 4", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM notification_deliveries WHERE status = 'failed'`); n != 4 {
		t.Errorf("%d failed rows, want 4", n)
	}
	if c := e.health(ch); c.HealthState != store.HealthFailed || c.LastError != "e4" {
		t.Errorf("health = %+v", c)
	}
	events := e.events(inc)
	if last := events[len(events)-1]; last.Message != "down via hook: e4" {
		t.Errorf("last event = %+v", last)
	}
}

func TestDefaultLadder(t *testing.T) {
	want := []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	if len(retryDelays) != len(want) {
		t.Fatalf("retryDelays = %v", retryDelays)
	}
	for i := range want {
		if retryDelays[i] != want[i] {
			t.Errorf("retryDelays = %v, want %v", retryDelays, want)
		}
	}
	d := &Dispatcher{delays: retryDelays}
	if got := d.wait(1, errors.New("x")); got != 30*time.Second {
		t.Errorf("wait(1) = %s", got)
	}
	// A rate limit lengthens a wait, never beyond the longest step.
	if got := d.wait(1, &notify.RateLimitError{Channel: "discord", After: 90 * time.Second}); got != 90*time.Second {
		t.Errorf("wait with a 90 s limit = %s", got)
	}
	if got := d.wait(2, &notify.RateLimitError{Channel: "discord", After: time.Hour}); got != 10*time.Minute {
		t.Errorf("wait with a 1 h limit = %s", got)
	}
	if got := d.wait(3, &notify.RateLimitError{Channel: "discord", After: time.Second}); got != 10*time.Minute {
		t.Errorf("wait with a short limit = %s", got)
	}
}

func TestStaleDownIsDroppedOnceTheIncidentEnded(t *testing.T) {
	e := newEnv(t)
	m, ch := e.setup()
	inc := e.outage(m)
	e.fail["hook"] = []error{errors.New("webhook: timed out"), errors.New("never reached")}
	disp := e.run(time.UTC)
	disp.Enqueue(down(m, inc))
	e.wait("the first attempt", e.sentCount(1))
	// The outage ends before the retry is due.
	e.recover(m, base.Add(time.Minute))
	e.wait("the drop", func() bool {
		return e.count(`SELECT COUNT(*) FROM notification_deliveries WHERE status = 'dropped' AND attempt = 2`) == 1
	})
	time.Sleep(40 * time.Millisecond)
	if n := len(e.sends()); n != 1 {
		t.Errorf("%d sends, the retry went out after the incident ended", n)
	}
	events := e.events(inc)
	last := events[len(events)-1]
	if last.Type != incident.EventNotificationFailed || !strings.Contains(last.Message, "the incident had ended") || !strings.Contains(last.Message, "webhook: timed out") {
		t.Errorf("last event = %+v", last)
	}
	if c := e.health(ch); c.HealthState != store.HealthWarning {
		t.Errorf("health after a drop = %s", c.HealthState)
	}
	// The recovery still goes out, with the next attempt succeeding.
	disp.Enqueue(incident.Intent{Kind: incident.IntentRecovery, MonitorID: m, IncidentID: inc, At: base.Add(time.Minute)})
	e.wait("the recovery", func() bool {
		for _, s := range e.sends() {
			if s.msg.Event.Kind == notify.KindRecovery {
				return true
			}
		}
		return false
	})
}

func TestChannelRemovedDuringRetries(t *testing.T) {
	e := newEnv(t)
	m, ch := e.setup()
	inc := e.outage(m)
	e.fail["hook"] = []error{errors.New("e1"), errors.New("e2"), errors.New("e3")}
	disp := e.run(time.UTC)
	disp.Enqueue(down(m, inc))
	e.wait("the first attempt", e.sentCount(1))
	if err := store.DeleteChannel(context.Background(), e.d, ch); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if n := len(e.sends()); n != 1 {
		t.Errorf("%d sends to a deleted channel", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM incident_events WHERE event_type IN (?, ?)`, incident.EventNotificationSent, incident.EventNotificationFailed); n != 0 {
		t.Errorf("%d outcome events for a deleted channel", n)
	}
}

func TestBoundedConcurrency(t *testing.T) {
	e := newEnv(t)
	p := e.profile("p", false, "", "", true)
	var channels []string
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		ch := e.channel(name, true)
		e.route(p, ch, "critical")
		channels = append(channels, ch)
	}
	m := e.monitor("API", p)
	inc := e.outage(m)
	e.block = make(chan struct{})
	disp := e.run(time.UTC)
	disp.Enqueue(down(m, inc))
	e.wait("the first sends", e.sentCount(maxSending))
	time.Sleep(30 * time.Millisecond)
	if n := len(e.sends()); n != maxSending {
		t.Errorf("%d sends running, want %d", n, maxSending)
	}
	close(e.block)
	e.wait("every channel", e.sentCount(len(channels)))
	if got := e.peak.Load(); got > maxSending {
		t.Errorf("%d sends at once", got)
	}
	e.wait("every record", func() bool {
		return e.count(`SELECT COUNT(*) FROM notification_deliveries WHERE status = 'sent'`) == len(channels)
	})
}

func TestFullQueueDropsWithoutBlocking(t *testing.T) {
	e := newEnv(t)
	m, _ := e.setup()
	inc := e.outage(m)
	disp := New(e.d, e.key, nil, nil, nil, e.log) // not running: nothing drains the queue
	finished := make(chan struct{})
	go func() {
		for range queueSize + 3 {
			disp.Enqueue(down(m, inc))
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}
	if s := disp.Stats(); s.Queued != queueSize || s.Dropped != 3 {
		t.Errorf("stats = %+v", s)
	}
}

func TestShutdownMidSendRecordsNothing(t *testing.T) {
	e := newEnv(t)
	m, ch := e.setup()
	inc := e.outage(m)
	e.block = make(chan struct{})
	disp := New(e.d, e.key, nil, nil, nil, e.log)
	disp.send = e.send
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { disp.Run(ctx); close(done) }()
	disp.Enqueue(down(m, inc))
	e.wait("the send to start", e.sentCount(1))
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if n := e.count(`SELECT COUNT(*) FROM notification_deliveries`); n != 0 {
		t.Errorf("%d rows for a send cut off by shutdown", n)
	}
	if c := e.health(ch); c.HealthState != store.HealthUnknown {
		t.Errorf("health = %s", c.HealthState)
	}
	// The claim stands: after a restart this DOWN is not sent again.
	if n := e.count(`SELECT COUNT(*) FROM incidents WHERE id = ? AND down_notified_at IS NOT NULL`, inc); n != 1 {
		t.Error("the claim was lost")
	}
}

func TestSendThatFinishedAtShutdownIsRecorded(t *testing.T) {
	e := newEnv(t)
	m, _ := e.setup()
	inc := e.outage(m)
	disp := New(e.d, e.key, nil, nil, nil, e.log)
	gate := make(chan struct{})
	disp.send = func(ctx context.Context, c notify.Config, msg notify.Message) error {
		<-gate // succeeds, after the shutdown began
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { disp.Run(ctx); close(done) }()
	disp.Enqueue(down(m, inc))
	e.wait("the claim", func() bool { return e.count(`SELECT COUNT(*) FROM incidents WHERE down_notified_at IS NOT NULL`) == 1 })
	cancel()
	close(gate)
	<-done
	if n := e.count(`SELECT COUNT(*) FROM notification_deliveries WHERE status = 'sent'`); n != 1 {
		t.Errorf("%d sent rows", n)
	}
}

func TestTLSWarningFromTheMonitorRow(t *testing.T) {
	e := newEnv(t)
	m, _ := e.setup()
	exp := base.Add(14*24*time.Hour + time.Hour)
	e.exec(`UPDATE monitors SET tls_not_after = ? WHERE id = ?`, store.FormatTime(exp), m)
	disp := e.run(time.UTC)
	disp.Enqueue(incident.Intent{Kind: incident.IntentTLSWarning, MonitorID: m, At: base})
	e.wait("the send", e.sentCount(1))
	if got, want := e.sends()[0].msg.Text(), "API TLS certificate expires in 14 days\nExpiry: 2026-10-20"; got != want {
		t.Errorf("message:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnreadableConfigurationCountsAsFailure(t *testing.T) {
	e := newEnv(t)
	m, ch := e.setup()
	inc := e.outage(m)
	// A configuration sealed for another channel does not open.
	other := e.channel("other", true)
	e.exec(`UPDATE notification_channels SET config_enc = (SELECT config_enc FROM notification_channels WHERE id = ?) WHERE id = ?`, other, ch)
	disp := e.run(time.UTC)
	disp.Enqueue(down(m, inc))
	e.wait("giving up", func() bool {
		return e.count(`SELECT COUNT(*) FROM incident_events WHERE incident_id = ? AND event_type = ?`, inc, incident.EventNotificationFailed) == 1
	})
	if n := len(e.sends()); n != 0 {
		t.Errorf("%d sends with an unreadable configuration", n)
	}
	if c := e.health(ch); c.HealthState != store.HealthFailed || !strings.Contains(c.LastError, "cannot be read") {
		t.Errorf("health = %+v", c)
	}
}

// A reminder says how long the outage has lasted and why, and is critical:
// the bypass lets it through quiet hours.
func TestReminderMessage(t *testing.T) {
	e := newEnv(t)
	p := e.profile("p", true, "11:00", "13:00", true)
	ch := e.channel("hook", true)
	e.route(p, ch, "critical")
	m := e.monitor("API", p)
	inc := e.outage(m)
	disp := e.run(time.UTC)

	disp.Enqueue(incident.Intent{Kind: incident.IntentReminder, MonitorID: m, IncidentID: inc, At: base.Add(62 * time.Minute)})
	e.wait("the send", e.sentCount(1))
	if got, want := e.sends()[0].msg.Text(), "API is still DOWN\nDuration: 1h 02m\nReason: timeout after 5s"; got != want {
		t.Errorf("message:\n%s\nwant:\n%s", got, want)
	}
	e.wait("the timeline entry", func() bool {
		return e.count(`SELECT COUNT(*) FROM incident_events WHERE incident_id = ? AND event_type = ? AND message = 'reminder via hook'`,
			inc, incident.EventNotificationSent) == 1
	})
}
