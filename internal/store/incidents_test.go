package store

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/incident"
)

// inTx runs fn in a committed transaction on the writer.
func inTx(t *testing.T, d *db.DB, fn func(tx *sql.Tx)) {
	t.Helper()
	tx, err := d.Writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// timeline returns an incident's events as {type, time, message}.
func timeline(t *testing.T, d *db.DB, incidentID string) [][3]string {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT event_type, created_at, COALESCE(message, '') FROM incident_events WHERE incident_id = ? ORDER BY id`, incidentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var e [3]string
		if err := rows.Scan(&e[0], &e[1], &e[2]); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestOpenAndCloseIncident(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	m := create(t, d, sample("api"))
	other := create(t, d, sample("other"))
	started, declared, ended := now.Add(time.Minute), now.Add(2*time.Minute), now.Add(time.Hour+5*time.Second+900*time.Millisecond)

	var id string
	inTx(t, d, func(tx *sql.Tx) {
		var opened bool
		var err error
		id, opened, err = OpenIncident(ctx, tx, NewIncident{MonitorID: m, StartedAt: started, DeclaredAt: declared,
			FailureKind: "timeout", Detected: "timeout after 5s", Summary: "connection refused"})
		if err != nil || !opened || len(id) != 32 {
			t.Fatalf("OpenIncident = %q, %v, %v", id, opened, err)
		}
		// The monitor is down already: nothing new, the same incident.
		again, opened, err := OpenIncident(ctx, tx, NewIncident{MonitorID: m, StartedAt: ended, DeclaredAt: ended})
		if err != nil || opened || again != id {
			t.Fatalf("second OpenIncident = %q, %v, %v; want %q, false", again, opened, err, id)
		}
	})
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE id = ? AND monitor_id = ? AND started_at = ? AND created_at = ?
		AND ended_at IS NULL AND initial_failure_kind = 'timeout' AND summary = 'connection refused'`,
		id, m, formatTime(started), formatTime(declared)); n != 1 {
		t.Fatal("the incident row is wrong")
	}
	want := [][3]string{
		{incident.EventDetected, formatTime(started), "timeout after 5s"},
		{incident.EventDeclaredDown, formatTime(declared), "connection refused"},
	}
	if got := timeline(t, d, id); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	inTx(t, d, func(tx *sql.Tx) {
		if _, ok, err := CloseIncident(ctx, tx, other, ended, incident.EventRecovered); err != nil || ok {
			t.Fatalf("CloseIncident without an active incident = %v, %v", ok, err)
		}
		closed, ok, err := CloseIncident(ctx, tx, m, ended, incident.EventRecovered)
		if err != nil || !ok || closed != id {
			t.Fatalf("CloseIncident = %q, %v, %v", closed, ok, err)
		}
		if _, ok, err := CloseIncident(ctx, tx, m, ended.Add(time.Hour), incident.EventRecovered); err != nil || ok {
			t.Fatalf("closing twice = %v, %v", ok, err)
		}
	})
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE id = ? AND ended_at = ?`, id, formatTime(ended)); n != 1 {
		t.Fatal("the incident was not ended at the given time")
	}
	// The duration is what the stored times say: whole seconds.
	want = append(want, [3]string{incident.EventRecovered, formatTime(ended), "down for 59m5s"})
	if got := timeline(t, d, id); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incidents`); n != 1 {
		t.Fatalf("%d incidents, want 1", n)
	}
}

func TestPauseClosesTheActiveIncident(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	m := create(t, d, sample("api"))
	var id string
	inTx(t, d, func(tx *sql.Tx) {
		var err error
		if id, _, err = OpenIncident(ctx, tx, NewIncident{MonitorID: m, StartedAt: now, DeclaredAt: now, Summary: "x"}); err != nil {
			t.Fatal(err)
		}
	})
	paused := now.Add(90 * time.Second)
	if changed, err := PauseMonitor(ctx, d, m, paused); err != nil || !changed {
		t.Fatalf("PauseMonitor = %v, %v", changed, err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE id = ? AND ended_at = ?`, id, formatTime(paused)); n != 1 {
		t.Fatal("the incident must end at the pause")
	}
	got := timeline(t, d, id)
	if want := ([3]string{incident.EventPaused, formatTime(paused), "down for 1m30s"}); len(got) != 3 || got[2] != want {
		t.Fatalf("events = %v, want a third one %v", got, want)
	}
	// A second pause changes nothing; neither does pausing a healthy monitor.
	if changed, err := PauseMonitor(ctx, d, m, paused.Add(time.Hour)); err != nil || changed {
		t.Fatalf("second PauseMonitor = %v, %v", changed, err)
	}
	healthy := create(t, d, sample("healthy"))
	if changed, err := PauseMonitor(ctx, d, healthy, paused); err != nil || !changed {
		t.Fatalf("PauseMonitor = %v, %v", changed, err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incident_events`); n != 3 {
		t.Fatalf("%d events, want 3", n)
	}
}

// RecentTransitions is the flapping window: starts and recoveries of the
// last four incidents, newest first, without the ends a pause made.
func TestRecentTransitions(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	m := create(t, d, sample("api"))
	other := create(t, d, sample("other"))
	min := func(n int) time.Time { return now.Add(time.Duration(n) * time.Minute) }
	open := func(monitor string, at time.Time) {
		t.Helper()
		inTx(t, d, func(tx *sql.Tx) {
			if _, opened, err := OpenIncident(ctx, tx, NewIncident{MonitorID: monitor, StartedAt: at, DeclaredAt: at.Add(5 * time.Second)}); err != nil || !opened {
				t.Fatalf("OpenIncident = %v, %v", opened, err)
			}
		})
	}
	closeAt := func(monitor string, at time.Time, eventType string) {
		t.Helper()
		inTx(t, d, func(tx *sql.Tx) {
			if _, ok, err := CloseIncident(ctx, tx, monitor, at, eventType); err != nil || !ok {
				t.Fatalf("CloseIncident = %v, %v", ok, err)
			}
		})
	}
	read := func(monitor string) (out []time.Time, active string) {
		t.Helper()
		inTx(t, d, func(tx *sql.Tx) {
			var err error
			if out, err = RecentTransitions(ctx, tx, monitor); err != nil {
				t.Fatal(err)
			}
			if active, err = ActiveIncidentID(ctx, tx, monitor); err != nil {
				t.Fatal(err)
			}
		})
		return out, active
	}

	if got, active := read(m); len(got) != 0 || active != "" {
		t.Fatalf("a monitor without incidents: %v, active %q", got, active)
	}
	open(m, min(0))
	closeAt(m, min(1), incident.EventRecovered)
	open(m, min(2))
	closeAt(m, min(3), incident.EventPaused) // no recovery
	open(m, min(4))
	closeAt(m, min(5), incident.EventRecovered)
	open(other, min(4))
	closeAt(other, min(6), incident.EventRecovered)
	open(m, min(7))
	got, active := read(m)
	want := []time.Time{min(7), min(5), min(4), min(2), min(1), min(0)}
	if !reflect.DeepEqual(got, want) || len(active) != 32 {
		t.Fatalf("transitions = %v\nwant %v (active %q)", got, want, active)
	}

	// Only the last four incidents are read: the oldest drops out.
	closeAt(m, min(8), incident.EventRecovered)
	open(m, min(9))
	got, _ = read(m)
	want = []time.Time{min(9), min(8), min(7), min(5), min(4), min(2)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transitions = %v\nwant %v", got, want)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM incidents WHERE monitor_id = ?`, m); n <= incident.FlapTransitions {
		t.Fatalf("%d incidents: the limit was not exercised", n)
	}
}

func TestSetFlapping(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	m, other := create(t, d, sample("api")), create(t, d, sample("other"))
	since := now.Add(time.Hour)
	inTx(t, d, func(tx *sql.Tx) {
		if err := SetFlapping(ctx, tx, m, &since); err != nil {
			t.Fatal(err)
		}
	})
	got, _ := GetMonitor(ctx, d.Reader, m)
	if got.FlappingSince == nil || !got.FlappingSince.Equal(since) || got.State != "pending" {
		t.Fatalf("after SetFlapping: %+v", got)
	}
	if o, _ := GetMonitor(ctx, d.Reader, other); o.FlappingSince != nil {
		t.Fatal("another monitor was marked flapping")
	}
	inTx(t, d, func(tx *sql.Tx) {
		if cs, err := GetCheckState(ctx, tx, m); err != nil || !cs.Flapping {
			t.Fatalf("GetCheckState = %+v, %v", cs, err)
		}
		if err := SetFlapping(ctx, tx, m, nil); err != nil {
			t.Fatal(err)
		}
		if cs, err := GetCheckState(ctx, tx, m); err != nil || cs.Flapping {
			t.Fatalf("GetCheckState after clearing = %+v, %v", cs, err)
		}
	})
	if got, _ := GetMonitor(ctx, d.Reader, m); got.FlappingSince != nil {
		t.Fatalf("after clearing: %+v", got.FlappingSince)
	}
}
