package web

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/monitor/httpcheck"
	"github.com/drilonrecica/sinjal/internal/recent"
	"github.com/drilonrecica/sinjal/internal/store"
)

// BenchmarkMonitorListPage is docs/18 scenario 6 through the whole handler:
// the shell's monitor column (GET /fragments/monitors, M8-05) for an admin
// with 1,000 monitors (two tags each, 100 raw results each, one in ten
// down with an active incident, every one with an incident earlier in the
// day for its 24-hour uptime, full beat bars in the engine's memory),
// measured from the request to the written HTML, session check included.
// The budget is 50 ms for a localhost page.
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
			if _, err := tx.Exec(`INSERT INTO incidents (id, monitor_id, started_at, created_at) VALUES (?, ?, ?, ?)`,
				id+"-now", id, store.FormatTime(monitorsNow.Add(-time.Hour)), store.FormatTime(monitorsNow.Add(-time.Hour))); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, created_at) VALUES (?, ?, ?, ?, ?)`,
			id+"-day", id, store.FormatTime(monitorsNow.Add(-10*time.Hour)), store.FormatTime(monitorsNow.Add(-9*time.Hour)), store.FormatTime(monitorsNow.Add(-10*time.Hour))); err != nil {
			b.Fatal(err)
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
	for _, id := range ids {
		for r := range recent.Cap {
			c := recent.Check{At: monitorsNow.Add(-time.Duration(recent.Cap-r) * 30 * time.Second).Unix(), MS: int32(40 + r%7), OK: true}
			if r%17 == 0 {
				c = recent.Check{At: c.At, MS: 5000, Kind: httpcheck.KindTimeout}
			}
			e.engine.Recent().Push(id, c)
		}
	}

	b.ReportAllocs()
	var body []byte
	for b.Loop() {
		rec := e.serve(withCookie(req("GET", "/fragments/monitors", nil), cookie))
		if rec.Code != 200 {
			b.Fatalf("status %d", rec.Code)
		}
		body = rec.Body.Bytes()
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(body)
	w.Close()
	b.ReportMetric(float64(len(body))/1024, "KiB/page")
	b.ReportMetric(float64(gz.Len())/1024, "KiB-gzip/page")
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
		Theme: "daylight", IncidentDays: 30, Monitors: mons}, now)
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
	for _, f := range []string{"api.json", "feed.xml"} {
		b.Run(f, func(b *testing.B) {
			b.ReportAllocs()
			size := 0
			for b.Loop() {
				rec := e.serve(req("GET", "/status/bench/"+f, nil))
				if rec.Code != 200 {
					b.Fatalf("status %d", rec.Code)
				}
				size = rec.Body.Len()
			}
			b.ReportMetric(float64(size), "bytes/doc")
		})
	}
}

// BenchmarkStatusPageHeartline is BenchmarkStatusPage's page in the
// Heartline layout with "Show latency" on for every service, each with a
// check every minute over the last day (36,000 results): what the pulses
// add to a cold build (25 reads of 24 hours in 96 buckets) and to a cached
// visit (25 SVG pulses in the HTML).
func BenchmarkStatusPageHeartline(b *testing.B) {
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
		for m := 0; m < 24*60; m++ {
			if _, err := tx.Exec(`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success) VALUES (?, ?, ?, 1)`,
				id, store.FormatTime(now.Add(-time.Duration(m)*time.Minute)), 40+(m*7+i)%60); err != nil {
				b.Fatal(err)
			}
		}
		mons = append(mons, store.StatusPageMonitorInput{MonitorID: id, DisplayName: fmt.Sprintf("Service %d", i), ShowLatency: true, Sort: i})
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	id, err := store.CreateStatusPage(b.Context(), e.db, store.StatusPageInput{Slug: "bench", Title: "Bench", Visibility: "public",
		Theme: "daylight", Layout: "heartline", IncidentDays: 30, Monitors: mons}, now)
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
		Theme: "daylight", IncidentDays: 30, Hosts: []string{"status.example.com"}}, time.Now()); err != nil {
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

// BenchmarkMonitorDetail is the detail page's Overview (M8-16): the header
// with its 120-beat bar from memory, the 90-day strip of a monitor with an
// incident every three days, and the 24-hour graph; then a list row's
// tips as beats.js fetches them on the first hover.
func BenchmarkMonitorDetail(b *testing.B) {
	e := newAppEnv(b)
	e.addUser(b, "a1", "admin", "admin", "")
	cookie, _ := e.signIn(b, "a1")
	id := e.addMonitor(b, "API", "https://api.example.com")
	now := time.Now().Truncate(time.Second)
	if _, err := e.db.Writer.Exec(`UPDATE monitors SET created_at = ? WHERE id = ?`, store.FormatTime(now.AddDate(0, 0, -100)), id); err != nil {
		b.Fatal(err)
	}
	for d := 1; d <= 90; d += 3 {
		start := now.AddDate(0, 0, -d)
		if _, err := e.db.Writer.Exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, created_at) VALUES (?, ?, ?, ?, ?)`,
			fmt.Sprintf("i%d", d), id, store.FormatTime(start), store.FormatTime(start.Add(20*time.Minute)), store.FormatTime(start)); err != nil {
			b.Fatal(err)
		}
	}
	for r := range recent.Cap {
		e.engine.Recent().Push(id, recent.Check{At: now.Add(-time.Duration(recent.Cap-r) * 30 * time.Second).Unix(), MS: 40, OK: r%17 != 0})
	}
	b.Run("overview", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if rec := e.serve(withCookie(req("GET", "/monitors/"+id, nil), cookie)); rec.Code != 200 {
				b.Fatalf("status %d", rec.Code)
			}
		}
	})
	b.Run("tips", func(b *testing.B) {
		path := fmt.Sprintf("/fragments/monitors/%s/beats?until=%d", id, now.Unix())
		b.ReportAllocs()
		for b.Loop() {
			if rec := e.serve(withCookie(req("GET", path, nil), cookie)); rec.Code != 200 {
				b.Fatalf("status %d", rec.Code)
			}
		}
	})
}
