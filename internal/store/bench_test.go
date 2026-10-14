package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

// BenchmarkInsertCheckResult is docs/18 scenario 4: raw check_results
// inserts the way the result processor makes them, 128 to a transaction.
// One operation is one row; the commit is shared by its batch.
func BenchmarkInsertCheckResult(b *testing.B) {
	d := testDB(b)
	ctx := context.Background()
	id := create(b, d, sample("a"))
	const batch = 128
	var tx *sql.Tx
	commit := func() {
		if tx != nil {
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			tx = nil
		}
	}
	at := time.Now()
	n := 0
	b.ReportAllocs()
	for b.Loop() {
		if tx == nil {
			var err error
			if tx, err = d.Writer.BeginTx(ctx, nil); err != nil {
				b.Fatal(err)
			}
		}
		r := CheckResult{MonitorID: id, CheckedAt: at.Add(time.Duration(n) * time.Second), Duration: 42 * time.Millisecond, Success: true, ProtocolStatus: "200"}
		if n%10 == 0 {
			r.Success, r.ProtocolStatus, r.ErrorKind, r.ErrorMessage, r.Snippet = false, "503", "http_status", "status 503, expected 200-399", "<h1>maintenance</h1>"
		}
		if err := InsertCheckResult(ctx, tx, r); err != nil {
			b.Fatal(err)
		}
		n++
		if n%batch == 0 {
			commit()
		}
	}
	commit()
}

// BenchmarkIncidentOpenClose is docs/18 scenario 7: an incident opening and
// closing the way the result processor does it, inside one transaction (so
// the cost includes the commit, the worst case: in a busy batch it is
// shared). The table starts with 10,000 ended incidents among 1,000
// monitors, so the partial unique index and the timeline index have a
// realistic size. One operation is one open plus one close.
func BenchmarkIncidentOpenClose(b *testing.B) {
	d := testDB(b)
	ctx := context.Background()
	const monitors, history = 1000, 10000
	ids := make([]string, monitors)
	for i := range ids {
		ids[i] = create(b, d, sample(fmt.Sprintf("m%04d", i)))
	}
	seedIncidents(b, d, ids, history)
	at := now.Add(24 * time.Hour)
	n := 0
	b.ReportAllocs()
	for b.Loop() {
		id := ids[n%monitors]
		start := at.Add(time.Duration(n) * time.Minute)
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			b.Fatal(err)
		}
		_, opened, err := OpenIncident(ctx, tx, NewIncident{MonitorID: id, StartedAt: start, DeclaredAt: start.Add(time.Minute),
			FailureKind: "timeout", Detected: "timeout after 5s", Summary: "timeout after 5s"})
		if err != nil || !opened {
			b.Fatalf("open: opened %v, %v", opened, err)
		}
		if _, ok, err := CloseIncident(ctx, tx, id, start.Add(5*time.Minute), "recovered"); err != nil || !ok {
			b.Fatalf("close: ok %v, %v", ok, err)
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
		n++
	}
}

// seedIncidents adds n ended incidents with their two opening events and a
// closing one, spread over the monitors and over the days before `now`.
func seedIncidents(b testing.TB, d *db.DB, monitorIDs []string, n int) {
	b.Helper()
	tx, err := d.Writer.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < n; i++ {
		start := now.Add(-time.Duration(i+1) * 20 * time.Minute)
		id := fmt.Sprintf("seed%028d", i)
		if _, err := tx.Exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, created_at) VALUES (?, ?, ?, ?, ?)`,
			id, monitorIDs[i%len(monitorIDs)], FormatTime(start), FormatTime(start.Add(5*time.Minute)), FormatTime(start.Add(time.Minute))); err != nil {
			b.Fatal(err)
		}
		for j, typ := range []string{"detected", "declared_down", "recovered"} {
			if _, err := tx.Exec(`INSERT INTO incident_events (incident_id, event_type, created_at) VALUES (?, ?, ?)`,
				id, typ, FormatTime(start.Add(time.Duration(j)*time.Minute))); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkMonitorList is docs/18 scenario 6: the reads behind the monitor
// list for an admin, 1,000 monitors with 100 raw results each, two tags
// apiece and one in ten down. The page makes exactly these
// four queries however many monitors there are.
func BenchmarkMonitorList(b *testing.B) {
	d := seededList(b, 1000, 100)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		ms, err := ListMonitors(ctx, d.Reader)
		if err != nil || len(ms) != 1000 {
			b.Fatalf("monitors %d, %v", len(ms), err)
		}
		tags, err := TagsByMonitor(ctx, d.Reader)
		if err != nil || len(tags) != 1000 {
			b.Fatalf("tags %d, %v", len(tags), err)
		}
		lat, err := LastDurations(ctx, d.Reader)
		if err != nil || len(lat) != 1000 {
			b.Fatalf("latencies %d, %v", len(lat), err)
		}
		urls, err := Targets(ctx, d.Reader)
		if err != nil || len(urls) != 1000 {
			b.Fatalf("urls %d, %v", len(urls), err)
		}
	}
}

// seededList creates `monitors` HTTP monitors, each with two tags and
// `results` raw check results, the first one in ten down.
func seededList(b testing.TB, monitors, results int) *db.DB {
	b.Helper()
	d := testDB(b)
	ids := make([]string, monitors)
	for i := range ids {
		m := sample(fmt.Sprintf("m%04d", i))
		m.Tags = []string{"prod", fmt.Sprintf("group-%d", i%20)}
		ids[i] = create(b, d, m)
	}
	tx, err := d.Writer.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	for i, id := range ids {
		if i%10 == 0 {
			if _, err := tx.Exec(`UPDATE monitors SET current_state = 'down' WHERE id = ?`, id); err != nil {
				b.Fatal(err)
			}
		}
		for r := 0; r < results; r++ {
			if _, err := tx.Exec(`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success) VALUES (?, ?, ?, 1)`,
				id, FormatTime(now.Add(-time.Duration(results-r)*30*time.Second)), 40+float64(r%7)); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return d
}

// BenchmarkRecentChecks is the engine's start-up seed of the beat bars
// (M8-16): the newest 120 checks of each of 1,000 monitors with 150 each.
func BenchmarkRecentChecks(b *testing.B) {
	d := testDB(b)
	ctx := context.Background()
	start := time.Now().Add(-time.Hour)
	durations := make([]int, 150)
	for i := range durations {
		durations[i] = 40 + i%30
		if i%13 == 0 {
			durations[i] = -5000
		}
	}
	for i := range 1000 {
		addResults(b, d, create(b, d, sample(fmt.Sprintf("m%04d", i))), start, durations...)
	}
	b.ReportAllocs()
	for b.Loop() {
		got, err := RecentChecks(ctx, d.Reader, 120)
		if err != nil || len(got) != 1000 {
			b.Fatal(err, len(got))
		}
	}
}

// BenchmarkUptimesRaw is the list column's uptime (M8-05): the last 24 h
// of 1,000 monitors at once, over a year of history (20 incidents each,
// one in ten active, 50 pauses), which the incident read scans in full.
func BenchmarkUptimesRaw(b *testing.B) {
	d := testDB(b)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	tx, err := d.Writer.Begin()
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]string, 1000)
	for i := range ids {
		ids[i] = fmt.Sprintf("m%04d", i)
		if _, err := tx.Exec(`INSERT INTO monitors (id, name, type, enabled, interval_seconds, timeout_ms, failure_threshold, retry_delay_ms, success_threshold, current_state, current_state_since, created_at, updated_at)
			VALUES (?, ?, 'http', 1, 30, 5000, 2, 5000, 1, 'up', ?, ?, ?)`, ids[i], ids[i], formatTime(now), formatTime(now.AddDate(-1, 0, 0)), formatTime(now)); err != nil {
			b.Fatal(err)
		}
		for j := range 20 {
			start := now.Add(-time.Duration(j*18+i%17) * 24 * time.Hour / 1).Add(-time.Hour)
			var end any = formatTime(start.Add(10 * time.Minute))
			if j == 0 && i%10 == 0 {
				end = nil
			}
			if _, err := tx.Exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, created_at) VALUES (?, ?, ?, ?, ?)`,
				fmt.Sprintf("%s-%d", ids[i], j), ids[i], formatTime(start), end, formatTime(start)); err != nil {
				b.Fatal(err)
			}
		}
		if i%20 == 0 {
			if _, err := tx.Exec(`INSERT INTO monitor_pauses (monitor_id, paused_at, resumed_at) VALUES (?, ?, ?)`,
				ids[i], formatTime(now.Add(-3*time.Hour)), formatTime(now.Add(-2*time.Hour))); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		got, err := UptimesRaw(ctx, d.Reader, now.Add(-24*time.Hour), now, now)
		if err != nil || len(got) != 1000 {
			b.Fatal(err, len(got))
		}
	}
}
