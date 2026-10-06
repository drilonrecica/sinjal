package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/maintenance"
)

// addIncident stores an incident from start to end (zero end: active).
func addIncident(t testing.TB, d *db.DB, monitorID string, start, end time.Time, suppressed bool) {
	t.Helper()
	var e any
	if !end.IsZero() {
		e = formatTime(end)
	}
	if _, err := d.Writer.Exec(`INSERT INTO incidents (id, monitor_id, started_at, ended_at, suppressed_by_parent, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, fmt.Sprintf("%s-%d", monitorID, start.Unix()), monitorID, formatTime(start), e, b2i(suppressed), formatTime(start)); err != nil {
		t.Fatal(err)
	}
}

// Raw and adjusted uptime differ by the excluded maintenance; paused time
// is in neither; an incident suppressed by its parent counts like any.
func TestUptime(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	id := create(t, d, sample("api"))
	other := create(t, d, sample("other"))
	day := now.Add(24 * time.Hour)
	h := func(n int) time.Time { return now.Add(time.Duration(n) * time.Hour) }

	addIncident(t, d, id, h(1), h(2), false)    // inside a window
	addIncident(t, d, id, h(5), h(6), true)     // suppressed by the parent
	addIncident(t, d, id, h(-2), h(-1), false)  // before the range
	addIncident(t, d, other, h(3), h(4), false) // another monitor
	if _, err := d.Writer.Exec(`INSERT INTO monitor_pauses (monitor_id, paused_at, resumed_at) VALUES (?, ?, ?)`,
		id, formatTime(h(10)), formatTime(h(14))); err != nil {
		t.Fatal(err)
	}
	for _, w := range []maintenance.Window{
		{Name: "excluded", Start: h(1), Duration: time.Hour, Recurrence: maintenance.None, ExcludeUptime: true},
		{Name: "counted", Start: h(5), Duration: time.Hour, Recurrence: maintenance.None},
		{Name: "elsewhere", Start: h(5), Duration: time.Hour, Recurrence: maintenance.None, ExcludeUptime: true,
			Scope: maintenance.Scope{Monitors: []string{other}}},
	} {
		if _, err := CreateMaintenance(ctx, d, w, now); err != nil {
			t.Fatal(err)
		}
	}
	raw, adj, err := Uptime(ctx, d.Reader, id, now, day, day.Add(time.Hour), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// Observed 20 h; down 2 h; 1 h of it excluded.
	if raw.Up != 18*3600 || raw.Den != 20*3600 || adj.Up != 18*3600 || adj.Den != 19*3600 {
		t.Fatalf("raw %d/%d, adjusted %d/%d", raw.Up, raw.Den, adj.Up, adj.Den)
	}
	if raw.Percent() != "90.00%" || adj.Percent() != "94.73%" {
		t.Fatalf("raw %s, adjusted %s", raw.Percent(), adj.Percent())
	}

	// An active incident runs to now.
	addIncident(t, d, id, h(20), time.Time{}, false)
	raw, _, err = Uptime(ctx, d.Reader, id, now, day, h(22), time.UTC)
	if err != nil || raw.Up != 14*3600 || raw.Den != 18*3600 {
		t.Fatalf("with an active incident: %d/%d, %v", raw.Up, raw.Den, err)
	}

	if _, _, err := Uptime(ctx, d.Reader, "missing", now, day, day, time.UTC); err != ErrNotFound {
		t.Fatalf("missing monitor: %v", err)
	}
}

// A monitor created paused has no data until it is resumed.
func TestUptimeOfAMonitorCreatedPaused(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	in := sample("off")
	in.Enabled = false
	id := create(t, d, in)
	raw, adj, err := Uptime(ctx, d.Reader, id, now, now.Add(time.Hour), now.Add(time.Hour), time.UTC)
	if err != nil || raw.OK() || adj.OK() {
		t.Fatalf("raw %+v, adjusted %+v, %v", raw, adj, err)
	}
	if _, err := ResumeMonitor(ctx, d, id, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	raw, _, err = Uptime(ctx, d.Reader, id, now, now.Add(2*time.Hour), now.Add(2*time.Hour), time.UTC)
	if err != nil || raw.Den != 3600 || raw.Up != 3600 {
		t.Fatalf("after the resume: %+v, %v", raw, err)
	}
}

// The interval reads use the monitor's indexes.
func TestUptimeQueryPlans(t *testing.T) {
	d := testDB(t)
	for _, c := range []struct{ query, index string }{
		{`SELECT paused_at, resumed_at FROM monitor_pauses WHERE monitor_id = ? AND paused_at < ? AND (resumed_at IS NULL OR resumed_at > ?)`, "idx_monitor_pauses_monitor_time"},
		{`SELECT started_at, ended_at FROM incidents WHERE monitor_id = ? AND started_at < ? AND (ended_at IS NULL OR ended_at > ?)`, "idx_incidents_monitor_time"},
	} {
		rows, err := d.Reader.Query(`EXPLAIN QUERY PLAN `+c.query, "m", "b", "a")
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		if p := strings.Join(plan, "; "); !strings.Contains(p, c.index) {
			t.Errorf("%s: plan %q does not use %s", c.query, p, c.index)
		}
	}
}

// BenchmarkUptime is one monitor's uptime over a year with 2,000
// incidents, 50 pauses and a daily maintenance window.
func BenchmarkUptime(b *testing.B) {
	d := testDB(b)
	ctx := context.Background()
	id := create(b, d, sample("api"))
	start := now
	for i := range 2000 {
		at := start.Add(time.Duration(i) * 4 * time.Hour)
		addIncident(b, d, id, at, at.Add(5*time.Minute), false)
	}
	for i := range 50 {
		at := start.Add(time.Duration(i)*7*24*time.Hour + time.Hour)
		if _, err := d.Writer.Exec(`INSERT INTO monitor_pauses (monitor_id, paused_at, resumed_at) VALUES (?, ?, ?)`,
			id, formatTime(at), formatTime(at.Add(30*time.Minute))); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := CreateMaintenance(ctx, d, maintenance.Window{Name: "nightly", Start: start.Add(2 * time.Hour), Duration: time.Hour,
		Recurrence: maintenance.Daily, ExcludeUptime: true}, start); err != nil {
		b.Fatal(err)
	}
	end := start.AddDate(1, 0, 0)
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := Uptime(ctx, d.Reader, id, start, end, end, time.UTC); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkUptimeDay is the last 24 hours of a monitor with a little
// history, what the detail header shows and what a list row would need.
func BenchmarkUptimeDay(b *testing.B) {
	d := testDB(b)
	ctx := context.Background()
	id := create(b, d, sample("api"))
	addIncident(b, d, id, now.Add(time.Hour), now.Add(2*time.Hour), false)
	if _, err := CreateMaintenance(ctx, d, maintenance.Window{Name: "nightly", Start: now.Add(2 * time.Hour), Duration: time.Hour,
		Recurrence: maintenance.Daily, ExcludeUptime: true}, now); err != nil {
		b.Fatal(err)
	}
	at := now.Add(24 * time.Hour)
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := Uptime(ctx, d.Reader, id, at.Add(-24*time.Hour), at, at, time.UTC); err != nil {
			b.Fatal(err)
		}
	}
}
