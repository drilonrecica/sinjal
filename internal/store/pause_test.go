package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

func create(t *testing.T, d *db.DB, in HTTPMonitor) string {
	t.Helper()
	id, err := CreateHTTPMonitor(context.Background(), d, in, now)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// pauses returns a monitor's pause intervals, oldest first; an open one has
// an empty end.
func pauses(t *testing.T, d *db.DB, id string) [][2]string {
	t.Helper()
	rows, err := d.Reader.Query(`SELECT paused_at, resumed_at FROM monitor_pauses WHERE monitor_id = ? ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var from string
		var to sql.NullString
		if err := rows.Scan(&from, &to); err != nil {
			t.Fatal(err)
		}
		out = append(out, [2]string{from, to.String})
	}
	return out
}

func TestPauseMonitor(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	id := create(t, d, sample("api"))
	if _, err := d.Writer.Exec(`UPDATE monitors SET current_state = 'down', flapping_since = ? WHERE id = ?`, formatTime(now), id); err != nil {
		t.Fatal(err)
	}

	at := now.Add(time.Hour)
	changed, err := PauseMonitor(ctx, d, id, at)
	if err != nil || !changed {
		t.Fatalf("PauseMonitor = %v, %v", changed, err)
	}
	m, err := GetMonitor(ctx, d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != "paused" || m.Enabled || !m.StateSince.Equal(at) || m.FlappingSince != nil || !m.UpdatedAt.Equal(at) {
		t.Fatalf("after pause: %+v", m)
	}
	if got, want := pauses(t, d, id), [][2]string{{formatTime(at), ""}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pause intervals = %v, want %v", got, want)
	}

	// Pausing again changes nothing: the interval keeps its start.
	changed, err = PauseMonitor(ctx, d, id, at.Add(time.Hour))
	if err != nil || changed {
		t.Fatalf("second PauseMonitor = %v, %v", changed, err)
	}
	if m2, _ := GetMonitor(ctx, d.Reader, id); !m2.StateSince.Equal(at) {
		t.Fatalf("second pause moved state-since to %v", m2.StateSince)
	}
	if got := pauses(t, d, id); len(got) != 1 {
		t.Fatalf("pause intervals after a second pause = %v", got)
	}
}

func TestResumeMonitor(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	id := create(t, d, sample("api"))
	paused, resumed := now.Add(time.Hour), now.Add(3*time.Hour)
	if _, err := PauseMonitor(ctx, d, id, paused); err != nil {
		t.Fatal(err)
	}

	changed, err := ResumeMonitor(ctx, d, id, resumed)
	if err != nil || !changed {
		t.Fatalf("ResumeMonitor = %v, %v", changed, err)
	}
	m, err := GetMonitor(ctx, d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != "pending" || !m.Enabled || !m.StateSince.Equal(resumed) || !m.UpdatedAt.Equal(resumed) {
		t.Fatalf("after resume: %+v", m)
	}
	if got, want := pauses(t, d, id), [][2]string{{formatTime(paused), formatTime(resumed)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pause intervals = %v, want %v", got, want)
	}

	// Resuming a monitor that is not paused must not reset its state.
	if _, err := d.Writer.Exec(`UPDATE monitors SET current_state = 'up' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	changed, err = ResumeMonitor(ctx, d, id, resumed.Add(time.Hour))
	if err != nil || changed {
		t.Fatalf("second ResumeMonitor = %v, %v", changed, err)
	}
	if m2, _ := GetMonitor(ctx, d.Reader, id); m2.State != "up" || !m2.StateSince.Equal(resumed) {
		t.Fatalf("second resume changed the monitor: %+v", m2)
	}

	// A second pause opens a second interval.
	if _, err := PauseMonitor(ctx, d, id, resumed.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := pauses(t, d, id); len(got) != 2 || got[1][1] != "" {
		t.Fatalf("pause intervals = %v", got)
	}
}

// A monitor created disabled is paused without a pause interval; resuming
// it still works.
func TestResumeMonitorCreatedDisabled(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	in := sample("off")
	in.Enabled = false
	id := create(t, d, in)

	changed, err := ResumeMonitor(ctx, d, id, now.Add(time.Hour))
	if err != nil || !changed {
		t.Fatalf("ResumeMonitor = %v, %v", changed, err)
	}
	if m, _ := GetMonitor(ctx, d.Reader, id); m.State != "pending" || !m.Enabled {
		t.Fatalf("after resume: %+v", m)
	}
	if got := pauses(t, d, id); len(got) != 0 {
		t.Fatalf("pause intervals = %v", got)
	}
}

func TestPauseResumeUnknownMonitor(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	if _, err := PauseMonitor(ctx, d, "missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PauseMonitor: %v", err)
	}
	if _, err := ResumeMonitor(ctx, d, "missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResumeMonitor: %v", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM monitor_pauses`); n != 0 {
		t.Fatalf("%d pause intervals written", n)
	}
}

func TestListSchedules(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	fast := sample("fast")
	fast.IntervalSeconds = 10
	fast.TimeoutMS = 2000
	a, b, c := create(t, d, fast), create(t, d, sample("default")), create(t, d, sample("paused"))
	if _, err := PauseMonitor(ctx, d, c, now); err != nil {
		t.Fatal(err)
	}

	got, err := ListSchedules(ctx, d.Reader)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Duration{a: 10 * time.Second, b: 30 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("ListSchedules = %v", got)
	}
	for i, s := range got {
		if want[s.ID] != s.Interval {
			t.Fatalf("ListSchedules = %v, want %v", got, want)
		}
		if i > 0 && got[i-1].ID >= s.ID {
			t.Fatalf("not ordered by id: %v", got)
		}
	}
}
