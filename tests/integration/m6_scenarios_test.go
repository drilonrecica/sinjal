package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/store"
)

// M6-07: scenarios 12 and 13 of docs/20 against the binary. Results older
// than raw retention are seeded while it is stopped and the record of the
// last daily run is removed, so the startup catch-up rolls them. The tier
// and failure edge cases are proven in internal/retention.

// seedHistory stores a paused monitor (no new results) with raw results
// 1, 10, 40 and 400 days old in a stopped server's database, and makes
// the daily job overdue.
func seedHistory(t *testing.T, dataDir string) string {
	t.Helper()
	d, err := db.Open(filepath.Join(dataDir, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	now := time.Now()
	id, err := store.CreateMonitor(ctx, d, store.MonitorInput{Name: "api",
		HTTP: store.HTTPConfig{URL: "http://127.0.0.1:1/"}}, now.AddDate(-2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := d.Writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, days := range []int{1, 10, 40, 400} {
		for i := range 30 {
			r := store.CheckResult{MonitorID: id, CheckedAt: now.AddDate(0, 0, -days).Add(time.Duration(i) * time.Minute),
				Success: i%10 != 0, Duration: time.Duration(20+i) * time.Millisecond}
			if err := store.InsertCheckResult(ctx, tx, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer.Exec(`DELETE FROM system_settings WHERE key = 'last_retention_run'`); err != nil {
		t.Fatal(err)
	}
	return id
}

func lastRetentionRun(t *testing.T, d *db.DB) string {
	t.Helper()
	v, _, err := store.Setting(context.Background(), d.Reader, "last_retention_run")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestRetentionRollupScenario is scenario 12: the startup catch-up rolls
// every result older than 7 days into its tier, keeps the recent ones raw
// and records the run.
func TestRetentionRollupScenario(t *testing.T) {
	skipShort(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := start(t, dataDir).stop(); err != nil {
		t.Fatal(err)
	}
	id := seedHistory(t, dataDir)

	s := start(t, dataDir)
	d := openDB(t, dataDir)
	deadline := time.Now().Add(10 * time.Second)
	for lastRetentionRun(t, d) == "" {
		if time.Now().After(deadline) {
			t.Fatalf("no retention run recorded\n%s", s.logs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if n := count(t, d, `SELECT count(*) FROM check_results WHERE monitor_id = ?`, id); n != 30 {
		t.Errorf("%d raw results left, want the 30 of yesterday", n)
	}
	// 10 days old → 5m, 40 days → 1h, 400 days → 1d; every result counted.
	for res, want := range map[int]int{300: 30, 3600: 30, 86400: 30} {
		if n := count(t, d, `SELECT coalesce(sum(total_count), 0) FROM check_aggregates
			WHERE monitor_id = ? AND resolution_seconds = ?`, id, res); n != want {
			t.Errorf("resolution %d s holds %d results, want %d", res, n, want)
		}
	}
	if n := count(t, d, `SELECT coalesce(sum(failure_count), 0) FROM check_aggregates WHERE monitor_id = ?`, id); n != 9 {
		t.Errorf("rolled failures %d, want 9", n)
	}
	if !hasMsg(s.records(), "history rollup finished") {
		t.Errorf("no rollup log line\n%s", s.logs)
	}
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}
}

// TestRetentionFailureScenario is scenario 13: when the aggregate write
// fails (a trigger stands in for the failure) the raw results stay, no run
// is recorded and the failure is logged; once it is gone the next start
// rolls them.
func TestRetentionFailureScenario(t *testing.T) {
	skipShort(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := start(t, dataDir).stop(); err != nil {
		t.Fatal(err)
	}
	id := seedHistory(t, dataDir)
	seeded := openDB(t, dataDir)
	if _, err := seeded.Writer.Exec(`CREATE TRIGGER fail BEFORE INSERT ON check_aggregates
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	seeded.Close()

	s := start(t, dataDir)
	deadline := time.Now().Add(10 * time.Second)
	for !hasMsg(s.records(), "history rollup failed; source rows of the failed step are kept") {
		if time.Now().After(deadline) {
			t.Fatalf("no rollup failure logged\n%s", s.logs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	d := openDB(t, dataDir)
	if n := count(t, d, `SELECT count(*) FROM check_results WHERE monitor_id = ?`, id); n != 120 {
		t.Errorf("%d raw results after the failed run, want all 120", n)
	}
	if n := count(t, d, `SELECT count(*) FROM check_aggregates`); n != 0 {
		t.Errorf("%d buckets written by the failed run", n)
	}
	if v := lastRetentionRun(t, d); v != "" {
		t.Errorf("failed run recorded at %s", v)
	}
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Writer.Exec(`DROP TRIGGER fail`); err != nil {
		t.Fatal(err)
	}
	s = start(t, dataDir)
	deadline = time.Now().Add(10 * time.Second)
	for lastRetentionRun(t, d) == "" {
		if time.Now().After(deadline) {
			t.Fatalf("the next start did not run the overdue job\n%s", s.logs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if n := count(t, d, `SELECT count(*) FROM check_results WHERE monitor_id = ?`, id); n != 30 {
		t.Errorf("%d raw results after the clean run, want 30", n)
	}
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}
}
