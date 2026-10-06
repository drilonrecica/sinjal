package integration

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/maintenance"
	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/secret"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/vault"
)

// hooks is a fake webhook endpoint: one path per channel, every payload
// kept, answering with the status set for its path (204 by default).
type hooks struct {
	*httptest.Server
	mu       sync.Mutex
	payloads map[string][]map[string]any
	status   map[string]int
}

func newHooks(t *testing.T) *hooks {
	t.Helper()
	h := &hooks{payloads: map[string][]map[string]any{}, status: map[string]int{}}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p map[string]any
		_ = json.Unmarshal(b, &p)
		h.mu.Lock()
		h.payloads[r.URL.Path] = append(h.payloads[r.URL.Path], p)
		status := h.status[r.URL.Path]
		h.mu.Unlock()
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hooks) setStatus(path string, status int) {
	h.mu.Lock()
	h.status[path] = status
	h.mu.Unlock()
}

// events are the event names a path received, in order.
func (h *hooks) events(path string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, p := range h.payloads[path] {
		out = append(out, p["event"].(string))
	}
	return out
}

func (h *hooks) payload(path string, i int) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.payloads[path][i]
}

// targets is a monitored server: one path per monitor, failing (503)
// while its flag is set.
type targets struct {
	*httptest.Server
	mu      sync.Mutex
	failing map[string]bool
}

func newTargets(t *testing.T) *targets {
	t.Helper()
	tg := &targets{failing: map[string]bool{}}
	tg.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tg.mu.Lock()
		fail := tg.failing[r.URL.Path]
		tg.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(tg.Close)
	return tg
}

func (tg *targets) set(path string, failing bool) {
	tg.mu.Lock()
	tg.failing[path] = failing
	tg.mu.Unlock()
}

// fakeSMTP is a minimal SMTP server with implicit TLS: it accepts every
// message without authentication and keeps its subject lines.
type fakeSMTP struct {
	port     int
	mu       sync.Mutex
	subjects []string
}

func newFakeSMTP(t *testing.T, cert tls.Certificate) *fakeSMTP {
	t.Helper()
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	f := &fakeSMTP{port: l.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	say := func(s string) { io.WriteString(conn, s+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250 fake")
		case strings.HasPrefix(cmd, "DATA"):
			say("354 go ahead")
			var subject string
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				if s, ok := strings.CutPrefix(l, "Subject: "); ok && subject == "" {
					subject = strings.TrimSpace(s)
				}
			}
			f.mu.Lock()
			f.subjects = append(f.subjects, subject)
			f.mu.Unlock()
			say("250 queued")
		case strings.HasPrefix(cmd, "QUIT"):
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.subjects...)
}

// trustFile writes a server's certificate as a PEM file for SSL_CERT_FILE,
// so the binary trusts the fakes through its system roots: nothing in the
// product is configured for the test.
func trustFile(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roots.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// m5env is a data directory whose database and key exist, opened for
// seeding while the server is stopped.
type m5env struct {
	t   *testing.T
	dir string
	d   *db.DB
	key *vault.Key
}

func newM5Env(t *testing.T) *m5env {
	t.Helper()
	dir := t.TempDir()
	if err := start(t, dir).stop(); err != nil {
		t.Fatal(err)
	}
	d := openDB(t, dir)
	key, err := vault.LoadOrCreate(context.Background(), dir, d.Reader, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return &m5env{t: t, dir: dir, d: d, key: key}
}

func (e *m5env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.d.Writer.Exec(q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *m5env) channel(name string, cfg notify.Config) string {
	e.t.Helper()
	id, err := store.CreateChannel(context.Background(), e.d, e.key, store.ChannelInput{Name: name, Enabled: true, Config: cfg}, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// profile routes the given severities to the channels.
func (e *m5env) profile(name string, in store.ProfileInput, routes map[string][]string) string {
	e.t.Helper()
	in.Name, in.Routes = name, routes
	id, err := store.CreateProfile(context.Background(), e.d, in, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// monitor is an HTTP monitor checked every second with the profile.
func (e *m5env) monitor(name, url, profile string) string {
	e.t.Helper()
	id := seedMonitor(e.t, e.d, name, url)
	e.exec(`UPDATE monitors SET interval_seconds = 1, notification_profile_id = NULLIF(?, '') WHERE id = ?`, profile, id)
	return id
}

func (e *m5env) events(monitorID string) []string {
	e.t.Helper()
	rows, err := e.d.Reader.Query(`SELECT e.event_type || ' ' || COALESCE(e.message, '') FROM incident_events e
		JOIN incidents i ON i.id = e.incident_id WHERE i.monitor_id = ? ORDER BY e.id`, monitorID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (e *m5env) hasEvent(monitorID, event string) func() bool {
	return func() bool { return slices.Contains(e.events(monitorID), event) }
}

func (e *m5env) state(monitorID string) string {
	return queryString(e.t, e.d, `SELECT current_state FROM monitors WHERE id = ?`, monitorID)
}

func all(channels ...string) map[string][]string {
	return map[string][]string{"info": channels, "warning": channels, "critical": channels}
}

// clock is t in the instance time zone (UTC here) as HH:MM.
func clock(t time.Time) string { return t.UTC().Format("15:04") }

// TestNotificationScenarios (M5-12) runs the built binary against local
// targets and fake endpoints for every channel the binary can reach:
// webhook over HTTP, Discord and SMTP over TLS trusted through
// SSL_CERT_FILE (Telegram's API address is fixed; see
// notify.TestTelegramDeliversEveryKind). Each monitor has its own webhook
// path, so each scenario is read on its own:
//
//	api      one DOWN however many failing checks, one reminder, the
//	         recovery with its duration and latency; DOWN and reminder
//	         also by Discord and email
//	quieton  quiet hours with the critical bypass: DOWN sent, recovery
//	         held and recorded
//	quietoff quiet hours without it: DOWN held and recorded
//	maint    scenario 5: held during maintenance, announced once the
//	         window is over and the monitor still down
//	child    scenario 7: held while the parent is down, announced once
//	         the parent recovered
func TestNotificationScenarios(t *testing.T) {
	skipShort(t)
	e := newM5Env(t)
	tg, hk := newTargets(t), newHooks(t)
	discord := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p struct {
			Embeds []struct {
				Title string `json:"title"`
			} `json:"embeds"`
		}
		_ = json.Unmarshal(b, &p)
		if len(p.Embeds) == 1 {
			hk.mu.Lock()
			hk.payloads["discord"] = append(hk.payloads["discord"], map[string]any{"event": p.Embeds[0].Title})
			hk.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer discord.Close()
	mail := newFakeSMTP(t, discord.TLS.Certificates[0])

	hook := func(name string) string {
		return e.channel(name, notify.Webhook{URL: hk.URL + "/" + name})
	}
	dc := e.channel("Discord", notify.Discord{WebhookURL: secret.String(discord.URL + "/api/webhooks/1/token")})
	smtp := e.channel("Mail", notify.SMTP{Host: "127.0.0.1", Port: mail.port, Security: notify.SecurityTLS,
		From: "sinjal@example.com", To: []string{"ops@example.com"}})
	api := hook("api")
	apiProfile := e.profile("API", store.ProfileInput{}, map[string][]string{"info": {api}, "warning": {api}, "critical": {api, dc, smtp}})
	e.exec(`UPDATE notification_profiles SET reminder_after_seconds = 2 WHERE id = ?`, apiProfile)

	now := time.Now()
	quiet := store.ProfileInput{QuietEnabled: true, QuietStart: clock(now.Add(-time.Hour)), QuietEnd: clock(now.Add(time.Hour))}
	quiet.CriticalBypass = true
	quietOn := e.profile("Quiet bypass", quiet, all(hook("quieton")))
	quiet.CriticalBypass = false
	quietOff := e.profile("Quiet strict", quiet, all(hook("quietoff")))

	ids := map[string]string{}
	for _, m := range []struct{ name, profile string }{{"api", apiProfile}, {"quieton", quietOn}, {"quietoff", quietOff},
		{"maint", e.profile("Maint", store.ProfileInput{}, all(hook("maint")))}, {"parent", ""},
		{"child", e.profile("Child", store.ProfileInput{}, all(hook("child")))}} {
		ids[m.name] = e.monitor(m.name, tg.URL+"/"+m.name, m.profile)
		tg.set("/"+m.name, m.name != "child")
	}
	e.exec(`UPDATE monitors SET parent_monitor_id = ? WHERE id = ?`, ids["parent"], ids["child"])
	window, err := store.CreateMaintenance(context.Background(), e.d, maintenance.Window{Name: "w", Start: now.Add(-time.Minute),
		Duration: time.Hour, Recurrence: maintenance.None, Suppress: true, Scope: maintenance.Scope{Monitors: []string{ids["maint"]}}}, now)
	if err != nil {
		t.Fatal(err)
	}

	s := start(t, e.dir, "SINJAL_TIMEZONE=UTC", "SSL_CERT_FILE="+trustFile(t, discord))
	defer s.stop()

	// The api outage: DOWN, then the reminder two seconds after its start.
	waitFor(t, s, "the api reminder", func() bool { return len(hk.events("/api")) >= 2 })
	// The parent is down by now; the child fails too and is held back.
	waitFor(t, s, "the parent down", func() bool { return e.state(ids["parent"]) == "down" })
	tg.set("/child", true)
	waitFor(t, s, "the child held", e.hasEvent(ids["child"], "notification_suppressed down: parent"))
	waitFor(t, s, "the maint DOWN held", e.hasEvent(ids["maint"], "notification_suppressed down: maintenance"))
	waitFor(t, s, "the strict quiet DOWN held", e.hasEvent(ids["quietoff"], "notification_suppressed down: quiet_hours"))
	waitFor(t, s, "the bypassing DOWN", func() bool { return len(hk.events("/quieton")) == 1 })

	// Recoveries: api announced, quieton's held by quiet hours.
	tg.set("/api", false)
	tg.set("/quieton", false)
	waitFor(t, s, "the api recovery", func() bool { return len(hk.events("/api")) == 3 })
	waitFor(t, s, "the quiet recovery held", e.hasEvent(ids["quieton"], "notification_suppressed recovery: quiet_hours"))
	// The window ends while maint is still down; the parent recovers.
	e.exec(`UPDATE maintenance_windows SET duration_seconds = 60 WHERE id = ?`, window)
	tg.set("/parent", false)
	waitFor(t, s, "the maint DOWN after the window", func() bool { return len(hk.events("/maint")) == 1 })
	waitFor(t, s, "the child DOWN after the parent", func() bool { return len(hk.events("/child")) == 1 })
	time.Sleep(2 * time.Second) // more failing checks: nothing more may follow

	if got, want := hk.events("/api"), []string{"monitor.down", "monitor.reminder", "monitor.recovered"}; !slices.Equal(got, want) {
		t.Errorf("api received %v, want %v", got, want)
	}
	rec := hk.payload("/api", 2)
	if inc, _ := rec["incident"].(map[string]any); inc == nil || inc["duration_seconds"].(float64) < 2 || rec["latency_ms"] == nil || rec["severity"] != "info" {
		t.Errorf("recovery payload %v", rec)
	}
	if rem := hk.payload("/api", 1); rem["severity"] != "critical" || rem["reason"] != "status 503, expected 200-399" {
		t.Errorf("reminder payload %v", rem)
	}
	if got := hk.events("discord"); !slices.Equal(got, []string{"api is DOWN", "api is still DOWN"}) {
		t.Errorf("discord received %v", got)
	}
	if got := mail.received(); len(got) != 2 || got[0] != "api is DOWN" || got[1] != "api is still DOWN" {
		t.Errorf("mail received %v", got)
	}
	for name, want := range map[string][]string{"quieton": {"monitor.down"}, "quietoff": nil, "maint": {"monitor.down"}, "child": {"monitor.down"}} {
		if got := hk.events("/" + name); !slices.Equal(got, want) {
			t.Errorf("%s received %v, want %v", name, got, want)
		}
	}
	if !slices.Contains(e.events(ids["maint"]), "notification_resumed ") || !slices.Contains(e.events(ids["child"]), "notification_resumed ") {
		t.Errorf("no release recorded: maint %v child %v", e.events(ids["maint"]), e.events(ids["child"]))
	}
	if n := count(t, e.d, `SELECT COUNT(*) FROM incidents WHERE monitor_id = ? AND reminder_sent_at IS NOT NULL`, ids["api"]); n != 1 {
		t.Errorf("reminder marked on %d incidents", n)
	}
	if n := count(t, e.d, `SELECT COUNT(*) FROM notification_channels WHERE health_state <> 'healthy' AND id IN (?, ?, ?)`, api, dc, smtp); n != 0 {
		t.Errorf("%d used channels not healthy", n)
	}
}

// TestProviderOutageScenario (scenario 9; docs/19 "Notification provider
// outage"): the endpoint fails while an outage begins and ends. The DOWN's
// first attempt fails, the channel turns warning, the outage ends, and the
// DOWN's retry 30 s later is dropped rather than sent stale; the
// recovery's retry goes through once the endpoint is back and the channel
// is healthy again. Giving up after the fourth attempt (~12.5 min) is
// covered by the dispatcher's own tests.
func TestProviderOutageScenario(t *testing.T) {
	skipShort(t)
	e := newM5Env(t)
	tg, hk := newTargets(t), newHooks(t)
	ch := e.channel("Hook", notify.Webhook{URL: hk.URL + "/hook"})
	id := e.monitor("api", tg.URL+"/api", e.profile("Ops", store.ProfileInput{}, all(ch)))
	tg.set("/api", true)
	hk.setStatus("/hook", http.StatusInternalServerError)

	s := start(t, e.dir)
	defer s.stop()
	attempts := func(event, status string) int {
		return count(t, e.d, `SELECT COUNT(*) FROM notification_deliveries WHERE channel_id = ? AND event_type = ? AND status = ?`, ch, event, status)
	}
	waitFor(t, s, "the failed DOWN", func() bool { return attempts("down", "failed") == 1 })
	if h := queryString(t, e.d, `SELECT health_state FROM notification_channels WHERE id = ?`, ch); h != "warning" {
		t.Errorf("health after the first failure = %s", h)
	}
	tg.set("/api", false)
	waitFor(t, s, "the failed recovery", func() bool { return attempts("recovery", "failed") == 1 })
	hk.setStatus("/hook", http.StatusNoContent)

	deadline := time.Now().Add(45 * time.Second)
	for attempts("down", "dropped") != 1 || attempts("recovery", "sent") != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("retries did not settle\n%s", s.logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := hk.events("/hook"); !slices.Equal(got, []string{"monitor.down", "monitor.recovered", "monitor.recovered"}) {
		t.Errorf("endpoint received %v: the failed DOWN, the failed recovery and its retry, never the DOWN again", got)
	}
	if attempts("down", "sent") != 0 {
		t.Error("a stale DOWN was delivered")
	}
	if h := queryString(t, e.d, `SELECT health_state FROM notification_channels WHERE id = ?`, ch); h != "healthy" {
		t.Errorf("health = %s", h)
	}
	ev := e.events(id)
	if !slices.ContainsFunc(ev, func(s string) bool {
		return strings.HasPrefix(s, "notification_failed down via Hook: not sent again, the incident had ended (last error: webhook: 500")
	}) || !slices.Contains(ev, "notification_sent recovery via Hook") {
		t.Errorf("timeline %v", ev)
	}
}

// TestSimulationLeavesHistoryUntouched: "Simulate incident" through the
// running binary delivers [TEST] messages and writes nothing but its audit
// entry.
func TestSimulationLeavesHistoryUntouched(t *testing.T) {
	skipShort(t)
	e := newM5Env(t)
	hk := newHooks(t)
	ch := e.channel("Hook", notify.Webhook{URL: hk.URL + "/hook"})
	p := e.profile("Ops", store.ProfileInput{}, all(ch))
	// Some history to leave alone: an ended incident of a paused monitor.
	m := seedMonitor(t, e.d, "api", "http://127.0.0.1:1/")
	e.exec(`UPDATE monitors SET enabled = 0, current_state = 'paused' WHERE id = ?`, m)
	e.exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, created_at) VALUES ('i1', ?, '2026-10-01T10:00:00Z', '2026-10-01T10:05:00Z', '2026-10-01T10:00:00Z')`, m)
	e.exec(`INSERT INTO incident_events (incident_id, event_type, message, created_at) VALUES ('i1', 'recovered', 'down for 5m0s', '2026-10-01T10:05:00Z')`)
	history := func() string {
		return queryString(t, e.d, `SELECT (SELECT COUNT(*) FROM incidents) || '/' || (SELECT COUNT(*) FROM incident_events) || '/' ||
			(SELECT COUNT(*) FROM notification_deliveries) || '/' || (SELECT COUNT(*) FROM check_results) || '/' ||
			(SELECT health_state || COALESCE(last_success_at, '') FROM notification_channels)`)
	}
	before := history()

	s := start(t, e.dir)
	defer s.stop()
	createAdmin(t, s)
	c := signIn(t, s, "admin", adminPassword)
	resp, body := c.post("/notifications/profiles/"+p+"/simulate", url.Values{})
	if resp.StatusCode != 200 || !strings.Contains(body, "[TEST] DOWN") || !strings.Contains(body, "[TEST] RECOVERY") {
		t.Fatalf("simulate = %d\n%s", resp.StatusCode, body)
	}
	// Sent in parallel: in either order.
	if got := slices.Sorted(slices.Values(hk.events("/hook"))); !slices.Equal(got, []string{"monitor.down", "monitor.recovered"}) {
		t.Fatalf("endpoint received %v", got)
	}
	for i := range 2 {
		if hk.payload("/hook", i)["test"] != true {
			t.Errorf("payload %d not marked test: %v", i, hk.payload("/hook", i))
		}
	}
	if after := history(); after != before {
		t.Errorf("history %s → %s", before, after)
	}
	if n := count(t, e.d, `SELECT COUNT(*) FROM audit_events WHERE event_type = 'notification.profile_simulated'`); n != 1 {
		t.Errorf("%d simulation audit rows", n)
	}
}
