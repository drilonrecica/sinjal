package store

import (
	"context"
	"testing"
	"time"
)

func TestRecentFailures(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	id := create(t, d, sample("a"))
	other := create(t, d, sample("b"))
	ins := func(id string, at time.Time, success int, kind any) {
		t.Helper()
		if _, err := d.Writer.Exec(`INSERT INTO check_results (monitor_id, checked_at, duration_ms, success, error_kind,
			error_message, diagnostic_snippet, protocol_status) VALUES (?, ?, 10, ?, ?, 'msg', 'body', '503')`,
			id, formatTime(at), success, kind); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 30 {
		ins(id, now.Add(time.Duration(i)*time.Minute), i%2, "http_status") // odd minutes succeed
	}
	ins(other, now.Add(time.Hour), 0, "timeout")

	got, err := RecentFailures(ctx, d.Reader, id, 5)
	if err != nil || len(got) != 5 {
		t.Fatalf("RecentFailures = %d, %v", len(got), err)
	}
	if !got[0].CheckedAt.Equal(now.Add(28*time.Minute)) || !got[4].CheckedAt.Equal(now.Add(20*time.Minute)) {
		t.Errorf("order: first %v, last %v", got[0].CheckedAt, got[4].CheckedAt)
	}
	if g := got[0]; g.ErrorKind != "http_status" || g.ErrorMessage != "msg" || g.Snippet != "body" || g.ProtocolStatus != "503" ||
		g.Duration != 10*time.Millisecond || g.Success {
		t.Errorf("row = %+v", g)
	}
	if none, err := RecentFailures(ctx, d.Reader, "nope", 5); err != nil || len(none) != 0 {
		t.Errorf("unknown monitor: %v, %v", none, err)
	}
}
