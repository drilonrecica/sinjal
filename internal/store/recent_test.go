package store

import (
	"context"
	"testing"
	"time"
)

func TestRecentChecks(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	a, b, quiet := create(t, d, sample("a")), create(t, d, sample("b")), create(t, d, sample("quiet"))
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	addResults(t, d, a, start, 10, 20, -30, 40, 50)
	addResults(t, d, b, start, 7)
	tx, err := d.Writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := InsertCheckResult(ctx, tx, CheckResult{MonitorID: b, CheckedAt: start.Add(time.Minute), Duration: time.Second, ErrorKind: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, err := RecentChecks(ctx, d.Reader, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[quiet]; ok || len(got) != 2 {
		t.Fatalf("monitors: %v", got)
	}
	// The newest three of a, oldest first.
	want := []RecentCheck{
		{CheckedAt: start.Add(2 * time.Second), Duration: 30 * time.Millisecond},
		{CheckedAt: start.Add(3 * time.Second), Duration: 40 * time.Millisecond, Success: true},
		{CheckedAt: start.Add(4 * time.Second), Duration: 50 * time.Millisecond, Success: true},
	}
	if len(got[a]) != len(want) {
		t.Fatalf("a: %+v", got[a])
	}
	for i := range want {
		if g := got[a][i]; !g.CheckedAt.Equal(want[i].CheckedAt) || g.Duration != want[i].Duration || g.Success != want[i].Success || g.Kind != "" {
			t.Fatalf("a[%d] = %+v, want %+v", i, g, want[i])
		}
	}
	if len(got[b]) != 2 || got[b][1].Kind != "timeout" || got[b][1].Success || !got[b][0].Success {
		t.Fatalf("b: %+v", got[b])
	}
}
