package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/store"
)

// heartbeat adds an enabled heartbeat monitor, created now, with the
// given interval and grace in seconds, and returns its id and token. The
// create path arrives with the form (M4-06); the rows are written here.
func (e *env) heartbeat(name string, interval, grace int) (string, string) {
	e.t.Helper()
	id := e.monitor(name, "http://heartbeat.invalid", nil)
	token, hash, err := store.NewHeartbeatToken()
	if err != nil {
		e.t.Fatal(err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	e.exec(`UPDATE monitors SET type = 'heartbeat', created_at = ?, current_state_since = ? WHERE id = ?`, ts, ts, id)
	e.exec(`DELETE FROM http_monitor_config WHERE monitor_id = ?`, id)
	e.exec(`INSERT INTO heartbeat_monitor_config (monitor_id, token_hash, expected_interval_seconds, grace_seconds)
		VALUES (?, ?, ?, ?)`, id, hash, interval, grace)
	return id, token
}

// lastResult is the newest stored result of a monitor: success, kind and
// message.
func (e *env) lastResult(id string) (bool, string, string) {
	e.t.Helper()
	var ok bool
	var kind, msg *string
	err := e.d.Reader.QueryRow(`SELECT success, error_kind, error_message FROM check_results
		WHERE monitor_id = ? ORDER BY id DESC LIMIT 1`, id).Scan(&ok, &kind, &msg)
	if err != nil {
		e.t.Fatal(err)
	}
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	return ok, deref(kind), deref(msg)
}

func (e *env) failures(id string) int {
	e.t.Helper()
	var n int
	if err := e.d.Reader.QueryRow(`SELECT COUNT(*) FROM check_results WHERE success = 0 AND monitor_id = ?`, id).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestHeartbeatBeatMarksUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, token := e.heartbeat("hb", 60, 0)
	r := e.start()

	if err := r.Beat(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	eventually(t, "up", func() bool { return e.get(id).State == "up" })
	if ok, kind, _ := e.lastResult(id); !ok || kind != "" || e.rows(id) != 1 {
		t.Errorf("result = %v %q, %d rows", ok, kind, e.rows(id))
	}
	c, err := store.GetHeartbeatConfig(context.Background(), e.d.Reader, id)
	if err != nil || c.LastBeatAt == nil || time.Since(*c.LastBeatAt) > 5*time.Second {
		t.Errorf("last beat = %v, %v", c.LastBeatAt, err)
	}
}

func TestHeartbeatExpiry(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, token := e.heartbeat("hb", 1, 1)
	start := time.Now()
	r := e.start()

	// Beats in time keep it up and store no failure.
	for range 8 {
		if err := r.Beat(context.Background(), token); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if e.get(id).State != "up" || e.failures(id) != 0 {
		t.Fatalf("state %s, %d failures while beating", e.get(id).State, e.failures(id))
	}

	// Without beats it is down after interval + grace and a confirmation.
	quiet := time.Now()
	eventually(t, "down", func() bool { return e.get(id).State == "down" })
	if late := time.Since(quiet); late < 1500*time.Millisecond {
		t.Errorf("down after %s, before interval + grace", late)
	}
	_, kind, msg := e.lastResult(id)
	if kind != KindHeartbeatMissed || !strings.HasPrefix(msg, "no heartbeat since ") || !strings.Contains(msg, "(expected every 1s, grace 1s)") {
		t.Errorf("failure = %q %q", kind, msg)
	}
	if e.failures(id) < 2 {
		t.Errorf("%d failures: threshold of 2 not applied", e.failures(id))
	}

	// A beat recovers it.
	if err := r.Beat(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	eventually(t, "up again", func() bool { return e.get(id).State == "up" })
	t.Logf("test ran %s", time.Since(start))
}

func TestHeartbeatNeverBeatenGoesDown(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, _ := e.heartbeat("hb", 1, 0)
	e.start()
	eventually(t, "down", func() bool { return e.get(id).State == "down" })
	if _, kind, msg := e.lastResult(id); kind != KindHeartbeatMissed || msg != "no heartbeat received yet (expected every 1s)" {
		t.Errorf("failure = %q %q", kind, msg)
	}
}

func TestHeartbeatRestart(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	stale, _ := e.heartbeat("stale", 60, 0)
	fresh, _ := e.heartbeat("fresh", 60, 0)
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().UTC().Format(time.RFC3339)
	for id, beat := range map[string]string{stale: old, fresh: recent} {
		e.exec(`UPDATE monitors SET current_state = 'up', current_state_since = ?, created_at = ? WHERE id = ?`, beat, old, id)
		e.exec(`UPDATE heartbeat_monitor_config SET last_beat_at = ? WHERE monitor_id = ?`, beat, id)
	}

	start := time.Now()
	e.start()
	// The stale one was due an hour ago: down at once, not after a period.
	eventually(t, "stale down", func() bool { return e.get(stale).State == "down" })
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("down after %s", took)
	}
	time.Sleep(200 * time.Millisecond)
	if e.get(fresh).State != "up" || e.rows(fresh) != 0 {
		t.Errorf("fresh: state %s, %d results", e.get(fresh).State, e.rows(fresh))
	}
}

func TestHeartbeatPausedAndResumed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, token := e.heartbeat("hb", 1, 0)
	r := e.start()
	ctx := context.Background()
	if _, err := r.Pause(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := r.Beat(ctx, token); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if e.get(id).State != "paused" || e.rows(id) != 0 {
		t.Fatalf("paused monitor: state %s, %d results", e.get(id).State, e.rows(id))
	}

	// Resumed: late only a period after the resume, though the last beat
	// is older than that.
	if _, err := r.Resume(ctx, id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if e.get(id).State != "pending" || e.rows(id) != 0 {
		t.Errorf("just resumed: state %s, %d results", e.get(id).State, e.rows(id))
	}
	eventually(t, "down after the resume", func() bool { return e.get(id).State == "down" })
}

func TestHeartbeatUnknownToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.heartbeat("hb", 60, 0)
	r := e.start()
	other, _, _ := store.NewHeartbeatToken()
	for _, token := range []string{other, "", "not-a-token", other + "x"} {
		if err := r.Beat(context.Background(), token); !errors.Is(err, ErrUnknownToken) {
			t.Errorf("Beat(%q) = %v", token, err)
		}
	}
}

func TestHeartbeatWithoutConfigIsAFailedCheck(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, _ := e.heartbeat("hb", 60, 0)
	e.exec(`DELETE FROM heartbeat_monitor_config WHERE monitor_id = ?`, id)
	e.start()
	eventually(t, "failure", func() bool { return e.rows(id) > 0 })
	if _, kind, msg := e.lastResult(id); kind != "unknown" || !strings.Contains(msg, "heartbeat settings are missing") {
		t.Errorf("failure = %q %q", kind, msg)
	}
}

// firstFailure waits for a monitor's first failed result and returns how
// long after since it appeared.
func (e *env) firstFailure(id string, since time.Time) time.Duration {
	e.t.Helper()
	eventually(e.t, "a failure", func() bool { return e.failures(id) > 0 })
	return time.Since(since)
}

// A beat moves the deadline job, so a missed beat is seen a period after
// the last beat, not on the cadence the job had before.
func TestHeartbeatBeatMovesTheDeadline(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, token := e.heartbeat("hb", 3, 0)
	r := e.start() // first job 3-4 s from now
	ctx := context.Background()
	if err := r.Beat(ctx, token); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if err := r.Beat(ctx, token); err != nil {
		t.Fatal(err)
	}
	last := time.Now()
	// Due 3 s after the last beat; the old cadence would find it 4-5 s after.
	if took := e.firstFailure(id, last); took < 3*time.Second || took > 3600*time.Millisecond {
		t.Errorf("missed beat seen %s after the last beat, want about 3 s", took)
	}
}

// After a restart the job runs at the stored deadline, not a period later.
func TestHeartbeatRestartSchedulesAtTheDeadline(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, _ := e.heartbeat("hb", 3, 0)
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	beat := time.Now().Add(-2 * time.Second).UTC().Format(time.RFC3339)
	e.exec(`UPDATE monitors SET current_state = 'up', created_at = ? WHERE id = ?`, old, id)
	e.exec(`UPDATE heartbeat_monitor_config SET last_beat_at = ? WHERE monitor_id = ?`, beat, id)
	start := time.Now()
	e.start()
	// The deadline is at most a second away (stored times have second
	// precision, the job waits one more); a period from now would be 3 s.
	if took := e.firstFailure(id, start); took > 2700*time.Millisecond {
		t.Errorf("missed deadline seen %s after the start", took)
	}
}

// A job that runs before the deadline (a beat came after it was handed to
// a worker) stores nothing; one after the deadline stores the failure.
func TestHeartbeatJobBeforeTheDeadline(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id, token := e.heartbeat("hb", 60, 0)
	r := e.start()
	ctx := context.Background()
	if err := r.Beat(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.run(ctx, id); ok {
		t.Error("a job before the deadline produced a result")
	}
	e.exec(`UPDATE heartbeat_monitor_config SET last_beat_at = ? WHERE monitor_id = ?`, "2026-01-01T00:00:00Z", id)
	e.exec(`UPDATE monitors SET created_at = ? WHERE id = ?`, "2026-01-01T00:00:00Z", id)
	res, ok := r.run(ctx, id)
	if !ok || res.Success || res.Kind != KindHeartbeatMissed {
		t.Errorf("overdue job = %+v, %v", res, ok)
	}
}
