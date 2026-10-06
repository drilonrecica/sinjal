package web

import (
	"fmt"
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
