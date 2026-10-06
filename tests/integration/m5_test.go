package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/notify"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/vault"
)

// TestNotificationsAcrossRestart (docs/19 "Restart during incident"): the
// built binary delivers one DOWN for an outage to a webhook channel, a
// restart while the monitor is still DOWN sends nothing more, and the
// recovery is delivered once, with the delivery rows, the timeline and
// the channel's health recorded.
func TestNotificationsAcrossRestart(t *testing.T) {
	skipShort(t)
	var failing atomic.Bool
	failing.Store(true)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer target.Close()

	var mu sync.Mutex
	var events []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p struct {
			Event string `json:"event"`
		}
		_ = json.Unmarshal(b, &p)
		mu.Lock()
		events = append(events, p.Event)
		mu.Unlock()
	}))
	defer hook.Close()
	received := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), events...)
	}

	dir := t.TempDir()
	stop := func(s *server) {
		t.Helper()
		if err := s.stop(); err != nil {
			t.Fatalf("exit after SIGTERM: %v\n%s", err, s.logs)
		}
	}
	stop(start(t, dir)) // creates the database and the master key
	d := openDB(t, dir)
	ctx := context.Background()
	key, err := vault.LoadOrCreate(ctx, dir, d.Reader, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(ctx, d, key, store.ChannelInput{Name: "Hook", Enabled: true,
		Config: notify.Webhook{URL: hook.URL + "/sinjal"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer.Exec(`INSERT INTO notification_profiles (id, name, created_at, updated_at) VALUES ('p', 'Ops', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	for _, severity := range []string{"info", "critical"} {
		if _, err := d.Writer.Exec(`INSERT INTO notification_routes (profile_id, severity, channel_id) VALUES ('p', ?, ?)`, severity, channel); err != nil {
			t.Fatal(err)
		}
	}
	id := seedMonitor(t, d, "api", target.URL)
	if _, err := d.Writer.Exec(`UPDATE monitors SET notification_profile_id = 'p' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	// Run 1: the outage is confirmed and announced once.
	s := start(t, dir)
	waitFor(t, s, "the DOWN notification", func() bool { return len(received()) >= 1 })
	waitFor(t, s, "the delivery row", func() bool {
		return count(t, d, `SELECT COUNT(*) FROM notification_deliveries WHERE channel_id = ? AND status = 'sent'`, channel) == 1
	})
	stop(s)
	if got := received(); len(got) != 1 || got[0] != "monitor.down" {
		t.Fatalf("after run 1: %v", got)
	}

	// Run 2: still DOWN. The restart announces nothing.
	results := count(t, d, `SELECT COUNT(*) FROM check_results WHERE monitor_id = ?`, id)
	s = start(t, dir)
	waitFor(t, s, "a check after the restart", func() bool {
		return count(t, d, `SELECT COUNT(*) FROM check_results WHERE monitor_id = ?`, id) > results
	})
	time.Sleep(200 * time.Millisecond)
	stop(s)
	if got := received(); len(got) != 1 {
		t.Fatalf("after the restart: %v, want the one DOWN", got)
	}

	// Run 3: the target is back; the recovery is announced once.
	failing.Store(false)
	s = start(t, dir)
	waitFor(t, s, "the recovery notification", func() bool { return len(received()) >= 2 })
	waitFor(t, s, "the recovery row", func() bool {
		return count(t, d, `SELECT COUNT(*) FROM notification_deliveries WHERE channel_id = ? AND status = 'sent'`, channel) == 2
	})
	stop(s)
	if got := received(); len(got) != 2 || got[1] != "monitor.recovered" {
		t.Fatalf("after run 3: %v", got)
	}
	if got := queryString(t, d, `SELECT group_concat(event_type || ':' || COALESCE(message, ''), ' ') FROM (SELECT event_type, message FROM incident_events WHERE incident_id = (SELECT id FROM incidents WHERE monitor_id = ?) ORDER BY id)`, id); got != "detected:status 503, expected 200-399 declared_down:status 503, expected 200-399 notification_sent:down via Hook recovered:down for "+durationOf(t, d, id)+" notification_sent:recovery via Hook" {
		t.Errorf("incident events: %s", got)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE monitor_id = ? AND down_notified_at IS NOT NULL AND recovery_notified_at IS NOT NULL`, id); n != 1 {
		t.Error("the incident's notification state is not set")
	}
	if got := queryString(t, d, `SELECT health_state FROM notification_channels WHERE id = ?`, channel); got != "healthy" {
		t.Errorf("channel health = %s", got)
	}
	if hasMsg(s.records(), "notifications not delivered before shutdown") {
		t.Errorf("a shutdown lost notifications\n%s", s.logs)
	}
}

// durationOf is the stored outage duration of the monitor's one incident,
// as the recovered event words it.
func durationOf(t *testing.T, d *db.DB, monitorID string) string {
	t.Helper()
	var started, ended string
	if err := d.Reader.QueryRow(`SELECT started_at, ended_at FROM incidents WHERE monitor_id = ?`, monitorID).Scan(&started, &ended); err != nil {
		t.Fatal(err)
	}
	from, _ := time.Parse(time.RFC3339, started)
	to, _ := time.Parse(time.RFC3339, ended)
	return to.Sub(from).String()
}
