package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

// heartbeatMonitor turns a new monitor into a heartbeat monitor (creating
// one arrives with the form, M4-06) and returns its id and token.
func heartbeatMonitor(t *testing.T, d *db.DB, intervalSeconds, graceSeconds int) (string, string) {
	t.Helper()
	id := create(t, d, sample("hb-"+t.Name()))
	token, hash, err := NewHeartbeatToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE monitors SET type = 'heartbeat' WHERE id = ?`, []any{id}},
		{`DELETE FROM http_monitor_config WHERE monitor_id = ?`, []any{id}},
		{`INSERT INTO heartbeat_monitor_config (monitor_id, token_hash, expected_interval_seconds, grace_seconds)
			VALUES (?, ?, ?, ?)`, []any{id, hash, intervalSeconds, graceSeconds}},
	} {
		if _, err := d.Writer.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return id, token
}

func TestHeartbeatTokens(t *testing.T) {
	token, hash, err := NewHeartbeatToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 || strings.ContainsAny(token, "+/=") {
		t.Errorf("token %q is not 32 bytes of base64url without padding", token)
	}
	got, ok := HashHeartbeatToken(token)
	if !ok || string(got) != string(hash) || len(hash) != 32 {
		t.Fatalf("hash round trip failed")
	}
	other, _, _ := NewHeartbeatToken()
	if other == token {
		t.Error("two tokens are equal")
	}
	for _, bad := range []string{"", "short", token + "A", token[:42], strings.Replace(token, token[:1], "+", 1), token + "="} {
		if _, ok := HashHeartbeatToken(bad); ok {
			t.Errorf("HashHeartbeatToken(%q) accepted", bad)
		}
	}
}

func TestRecordBeatAndRegenerate(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	id, token := heartbeatMonitor(t, d, 60, 10)
	hash, _ := HashHeartbeatToken(token)

	got, err := RecordBeat(ctx, d, hash, now)
	if err != nil || got != id {
		t.Fatalf("RecordBeat = %q, %v", got, err)
	}
	c, err := GetHeartbeatConfig(ctx, d.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExpectedInterval != time.Minute || c.Grace != 10*time.Second || c.LastBeatAt == nil || !c.LastBeatAt.Equal(now) {
		t.Errorf("config = %+v", c)
	}

	fresh, err := SetHeartbeatToken(ctx, d, id)
	if err != nil || fresh == token {
		t.Fatalf("SetHeartbeatToken = %q, %v", fresh, err)
	}
	if _, err := RecordBeat(ctx, d, hash, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("old token still works: %v", err)
	}
	freshHash, _ := HashHeartbeatToken(fresh)
	if got, err := RecordBeat(ctx, d, freshHash, now); err != nil || got != id {
		t.Errorf("new token: %q, %v", got, err)
	}
	if _, err := SetHeartbeatToken(ctx, d, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetHeartbeatToken(missing) = %v", err)
	}
	if _, err := GetHeartbeatConfig(ctx, d.Reader, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetHeartbeatConfig(missing) = %v", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM heartbeat_monitor_config WHERE token_hash = ?`, []byte(fresh)); n != 0 {
		t.Error("the raw token is stored")
	}
}

func TestHeartbeatDeadline(t *testing.T) {
	watched := now
	after, before := now.Add(time.Hour), now.Add(-time.Hour)
	period := 90 * time.Second
	for _, tc := range []struct {
		name string
		last *time.Time
		want time.Time
	}{
		{"never beaten", nil, watched.Add(period)},
		{"beaten", &after, after.Add(period)},
		{"last beat before creation or resume", &before, watched.Add(period)},
	} {
		c := HeartbeatConfig{ExpectedInterval: time.Minute, Grace: 30 * time.Second, LastBeatAt: tc.last, WatchedSince: watched}
		if got := c.Deadline(); !got.Equal(tc.want) {
			t.Errorf("%s: deadline %s, want %s", tc.name, got, tc.want)
		}
	}
}

// WatchedSince is the creation, or the latest resume after it.
func TestHeartbeatWatchedSince(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	id, _ := heartbeatMonitor(t, d, 60, 0)
	created := now // the create helper's time
	watched := func() time.Time {
		t.Helper()
		c, err := GetHeartbeatConfig(ctx, d.Reader, id)
		if err != nil {
			t.Fatal(err)
		}
		s, err := GetSchedule(ctx, d.Reader, id)
		if err != nil || !s.Deadline.Equal(c.Deadline()) {
			t.Fatalf("schedule deadline %s, config %s (%v)", s.Deadline, c.Deadline(), err)
		}
		return c.WatchedSince
	}
	if got := watched(); !got.Equal(created) {
		t.Fatalf("new monitor: %s, want %s", got, created)
	}
	for i, at := range []time.Time{now.Add(time.Hour), now.Add(3 * time.Hour)} {
		if _, err := PauseMonitor(ctx, d, id, at.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := ResumeMonitor(ctx, d, id, at); err != nil {
			t.Fatal(err)
		}
		if got := watched(); !got.Equal(at) {
			t.Fatalf("after resume %d: %s, want %s", i, got, at)
		}
	}
}

func TestHeartbeatSchedules(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	httpID := create(t, d, sample("http"))
	id, _ := heartbeatMonitor(t, d, 60, 15)
	beat := now.Add(time.Hour)
	if _, err := d.Writer.Exec(`UPDATE heartbeat_monitor_config SET last_beat_at = ? WHERE monitor_id = ?`, formatTime(beat), id); err != nil {
		t.Fatal(err)
	}
	all, err := ListSchedules(ctx, d.Reader)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListSchedules = %v, %v", all, err)
	}
	for _, s := range all {
		got, err := GetSchedule(ctx, d.Reader, s.ID)
		if err != nil || got != s {
			t.Errorf("GetSchedule(%s) = %+v, %v; listed %+v", s.ID, got, err, s)
		}
		switch s.ID {
		case httpID:
			if s.Interval != 30*time.Second || !s.Deadline.IsZero() {
				t.Errorf("http schedule = %+v", s)
			}
		case id:
			if s.Interval != 75*time.Second || !s.Deadline.Equal(beat.Add(75*time.Second)) {
				t.Errorf("heartbeat schedule = %+v", s)
			}
		}
	}
	if _, err := GetSchedule(ctx, d.Reader, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSchedule(missing) = %v", err)
	}
}

func TestHeartbeatConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		c     HeartbeatConfig
		field string
	}{
		{HeartbeatConfig{ExpectedInterval: time.Minute}, ""},
		{HeartbeatConfig{ExpectedInterval: time.Minute, Grace: time.Minute, SourceLabel: strings.Repeat("é", 100)}, ""},
		{HeartbeatConfig{}, "expected_interval"},
		{HeartbeatConfig{ExpectedInterval: time.Minute, Grace: -time.Second}, "grace"},
		{HeartbeatConfig{ExpectedInterval: time.Minute, SourceLabel: strings.Repeat("x", 101)}, "source_label"},
	} {
		err := tc.c.Validate()
		var ie *InputError
		if tc.field == "" && err != nil || tc.field != "" && (!errors.As(err, &ie) || ie.Field != tc.field) {
			t.Errorf("Validate(%+v) = %v, want field %q", tc.c, err, tc.field)
		}
	}
}
