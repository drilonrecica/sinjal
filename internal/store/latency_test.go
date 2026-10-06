package store

import (
	"context"
	"testing"
	"time"
)

func TestLastDuration(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	withResults := create(t, d, sample("a"))
	noResults := create(t, d, sample("b"))
	nullDuration := create(t, d, sample("c"))

	ins := func(id string, at time.Time, ms any) {
		t.Helper()
		if _, err := d.Writer.Exec(`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success)
			VALUES (?, ?, ?, 1)`, id, formatTime(at), ms); err != nil {
			t.Fatal(err)
		}
	}
	ins(withResults, now, 250.0)
	ins(withResults, now.Add(time.Minute), 12.5) // newest by time wins, not the highest or last-inserted
	ins(withResults, now.Add(-time.Hour), 999.0)
	ins(nullDuration, now, 40.0)
	ins(nullDuration, now.Add(time.Minute), nil) // newest has no duration: no stale older value

	got, ok, err := LastDuration(ctx, d.Reader, withResults)
	if err != nil || !ok || got != 12500*time.Microsecond {
		t.Errorf("LastDuration = %v, %v, %v; want 12.5ms", got, ok, err)
	}
	for name, id := range map[string]string{"no results": noResults, "null duration": nullDuration, "unknown monitor": "nope"} {
		if got, ok, err := LastDuration(ctx, d.Reader, id); err != nil || ok || got != 0 {
			t.Errorf("%s: LastDuration = %v, %v, %v; want absent", name, got, ok, err)
		}
	}

	all, err := LastDurations(ctx, d.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[withResults] != 12500*time.Microsecond {
		t.Errorf("LastDurations = %v, want only the monitor with a latest duration", all)
	}
}
