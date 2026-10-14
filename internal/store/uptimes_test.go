package store

import (
	"context"
	"testing"
	"time"
)

// UptimesRaw is the raw uptime of every monitor at once, for the list
// column: it must agree with Uptime for each of them.
func TestUptimesRawAgreesWithUptime(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	h := func(n int) time.Time { return now.Add(time.Duration(n) * time.Hour) }
	a, b, c, quiet := create(t, d, sample("a")), create(t, d, sample("b")), create(t, d, sample("c")), create(t, d, sample("quiet"))

	addIncident(t, d, a, h(1), h(2), false)
	addIncident(t, d, a, h(-3), h(-1), false) // before the range
	addIncident(t, d, a, h(-1), h(3), false)  // across its start
	addIncident(t, d, b, h(20), time.Time{}, false)
	for _, p := range []struct {
		id       string
		from, to time.Time
	}{{a, h(10), h(14)}, {b, h(-5), h(2)}, {c, h(18), time.Time{}}} {
		var to any
		if !p.to.IsZero() {
			to = formatTime(p.to)
		}
		if _, err := d.Writer.Exec(`INSERT INTO monitor_pauses (monitor_id, paused_at, resumed_at) VALUES (?, ?, ?)`, p.id, formatTime(p.from), to); err != nil {
			t.Fatal(err)
		}
	}
	// A monitor created inside the range.
	late := create(t, d, sample("late"))
	if _, err := d.Writer.Exec(`UPDATE monitors SET created_at = ? WHERE id = ?`, formatTime(h(12)), late); err != nil {
		t.Fatal(err)
	}

	from, to, at := now, h(24), h(22)
	got, err := UptimesRaw(ctx, d.Reader, from, to, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b, c, quiet, late} {
		want, _, err := Uptime(ctx, d.Reader, id, from, to, at, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		if got[id] != want {
			t.Errorf("%s: %+v, want %+v", id, got[id], want)
		}
	}
	if len(got) != 5 {
		t.Errorf("%d monitors, want 5", len(got))
	}
}

func TestMonitorNames(t *testing.T) {
	d := testDB(t)
	a, b := create(t, d, sample("api")), create(t, d, sample("db"))
	create(t, d, sample("other"))
	got, err := MonitorNames(context.Background(), d.Reader, []string{a, b, "gone", a})
	if err != nil || len(got) != 2 || got[a] != "api" || got[b] != "db" {
		t.Fatalf("%v, %v", got, err)
	}
	if got, err := MonitorNames(context.Background(), d.Reader, nil); err != nil || len(got) != 0 {
		t.Fatalf("no ids: %v, %v", got, err)
	}
}
