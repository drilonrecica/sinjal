package web

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
)

// BenchmarkMonitorListPage is docs/18 scenario 6 through the whole handler:
// GET /monitors for an admin with 1,000 monitors (two tags each, 100 raw
// results each, one in ten down), measured from the request to the written
// HTML, session check included. The budget is 50 ms for a localhost page.
func BenchmarkMonitorListPage(b *testing.B) {
	e := newAppEnv(b)
	e.addUser(b, "a1", "admin", "admin", "")
	cookie, _ := e.signIn(b, "a1")

	ids := make([]string, 1000)
	for i := range ids {
		id, err := store.CreateMonitor(b.Context(), e.db, store.MonitorInput{
			Name: fmt.Sprintf("monitor %04d", i), Enabled: true, Tags: []string{"prod", fmt.Sprintf("group-%d", i%20)},
			HTTP: store.HTTPConfig{URL: fmt.Sprintf("https://host%04d.example.com/health", i), FollowRedirects: true},
		}, monitorsNow)
		if err != nil {
			b.Fatal(err)
		}
		ids[i] = id
	}
	tx, err := e.db.Writer.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i, id := range ids {
		if i%10 == 0 {
			if _, err := tx.Exec(`UPDATE monitors SET current_state = 'down' WHERE id = ?`, id); err != nil {
				b.Fatal(err)
			}
		}
		for r := 0; r < 100; r++ {
			if _, err := tx.Exec(`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success) VALUES (?, ?, ?, 1)`,
				id, store.FormatTime(monitorsNow.Add(-time.Duration(100-r)*30*time.Second)), 40+float64(r%7)); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	size := 0
	for b.Loop() {
		rec := e.serve(withCookie(req("GET", "/monitors", nil), cookie))
		if rec.Code != 200 {
			b.Fatalf("status %d", rec.Code)
		}
		size = rec.Body.Len()
	}
	b.ReportMetric(float64(size)/1024, "KiB/page")
}

// BenchmarkStatusPage builds a 25-service page whose monitors had an
// incident every few days for 90 days: cold (every figure read) and as a
// visitor gets it within the cache TTL.
func BenchmarkStatusPage(b *testing.B) {
	e := newAppEnv(b)
	now := time.Now()
	var mons []store.StatusPageMonitorInput
	var ids []string
	for i := 0; i < 25; i++ {
		id, err := store.CreateMonitor(b.Context(), e.db, store.MonitorInput{Name: fmt.Sprintf("monitor %d", i), Enabled: true,
			HTTP: store.HTTPConfig{URL: "https://x.example.com/"}}, now.AddDate(0, 0, -120))
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, id)
	}
	tx, err := e.db.Writer.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i, id := range ids {
		for d := 0; d < 90; d += 3 {
			start := now.AddDate(0, 0, -d).Add(-time.Duration(i) * time.Minute)
			if _, err := tx.Exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, created_at) VALUES (?, ?, ?, ?, ?)`,
				fmt.Sprintf("i%02d%029d", i, d), id, store.FormatTime(start), store.FormatTime(start.Add(7*time.Minute)), store.FormatTime(start)); err != nil {
				b.Fatal(err)
			}
		}
		mons = append(mons, store.StatusPageMonitorInput{MonitorID: id, DisplayName: fmt.Sprintf("Service %d", i), ShowLatency: i%2 == 0, Sort: i})
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	id, err := store.CreateStatusPage(b.Context(), e.db, store.StatusPageInput{Slug: "bench", Title: "Bench", Visibility: "public",
		Theme: "paper", IncidentDays: 30, Monitors: mons}, now)
	if err != nil {
		b.Fatal(err)
	}
	p, err := store.GetStatusPage(b.Context(), e.db.Reader, id)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("cold", func(b *testing.B) {
		h := NewPublic(e.db, "", nil, time.UTC, quietLoggerOnly())
		b.ReportAllocs()
		for b.Loop() {
			if _, err := h.build(b.Context(), p, now); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		size := 0
		for b.Loop() {
			rec := e.serve(req("GET", "/status/bench", nil))
			if rec.Code != 200 {
				b.Fatalf("status %d", rec.Code)
			}
			size = rec.Body.Len()
		}
		b.ReportMetric(float64(size), "bytes/page")
	})
}

// BenchmarkHostRouter is the cost every request pays for custom hostnames:
// one primary-key lookup of the request's host (here an unmapped one, the
// common case for the admin UI), measured on /healthz-sized work.
func BenchmarkHostRouter(b *testing.B) {
	e := newAppEnv(b)
	if _, err := store.CreateStatusPage(b.Context(), e.db, store.StatusPageInput{Slug: "p", Title: "P", Visibility: "public",
		Theme: "paper", IncidentDays: 30, Hosts: []string{"status.example.com"}}, time.Now()); err != nil {
		b.Fatal(err)
	}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	h := HostRouter(e.db, "localhost", next, quietLoggerOnly())(next)
	r := req("GET", "/", nil)
	r.Host = "other.example.com"
	b.ReportAllocs()
	for b.Loop() {
		h.ServeHTTP(nil, r)
	}
}
