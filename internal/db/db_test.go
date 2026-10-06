package db

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func pragma(t *testing.T, h *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := h.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

func TestPragmas(t *testing.T) {
	d := openTemp(t)
	for name, h := range map[string]*sql.DB{"writer": d.Writer, "reader": d.Reader} {
		want := map[string]string{
			"journal_mode": "wal",
			"foreign_keys": "1",
			"synchronous":  "1", // NORMAL
			"busy_timeout": "5000",
		}
		for p, w := range want {
			if got := pragma(t, h, p); got != w {
				t.Errorf("%s: PRAGMA %s = %q, want %q", name, p, got, w)
			}
		}
	}
	if got := pragma(t, d.Reader, "query_only"); got != "1" {
		t.Errorf("reader query_only = %q, want 1", got)
	}
	if got := pragma(t, d.Writer, "query_only"); got != "0" {
		t.Errorf("writer query_only = %q, want 0", got)
	}
}

func TestWriterIsSingleConnection(t *testing.T) {
	d := openTemp(t)
	if got := d.Writer.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("writer MaxOpenConnections = %d, want 1", got)
	}
	if got := d.Reader.Stats().MaxOpenConnections; got < 4 {
		t.Errorf("reader MaxOpenConnections = %d, want >= 4", got)
	}
}

func TestReaderCannotWrite(t *testing.T) {
	d := openTemp(t)
	if _, err := d.Writer.Exec("CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	_, err := d.Reader.Exec("INSERT INTO t VALUES (1)")
	if err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Errorf("reader write err = %v, want a read-only error", err)
	}
}

func TestReadWhileWriting(t *testing.T) {
	d := openTemp(t)
	if _, err := d.Writer.Exec("CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer.Exec("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}

	tx, err := d.Writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO t VALUES (2)"); err != nil {
		t.Fatal(err)
	}

	// WAL: readers see the last committed state without blocking.
	var n int
	if err := d.Reader.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reader saw %d rows during an open write tx, want 1", n)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	d := openTemp(t)
	stmts := []string{
		"CREATE TABLE p (id INTEGER PRIMARY KEY)",
		"CREATE TABLE c (pid INTEGER REFERENCES p(id))",
	}
	for _, s := range stmts {
		if _, err := d.Writer.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Writer.Exec("INSERT INTO c VALUES (99)"); err == nil {
		t.Error("foreign key violation was accepted")
	}
}

func TestOpenPathWithSpecialCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "my data? #1", "sinjal.db")
	if err := mkdirAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Writer.Exec("CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailsClearly(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "missing-dir", "sinjal.db"))
	if err == nil {
		t.Fatal("expected an error for a missing parent directory")
	}
}

// busyFixture returns a handle that holds the write lock until release is
// called, and a second raw connection (busy_timeout=0) that fails at once
// with SQLITE_BUSY while the lock is held.
func busyFixture(t *testing.T) (contender *sql.DB, release func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "busy.db")
	open := func() *sql.DB {
		q := url.Values{}
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "busy_timeout(0)")
		h, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String())
		if err != nil {
			t.Fatal(err)
		}
		h.SetMaxOpenConns(1)
		t.Cleanup(func() { h.Close() })
		return h
	}
	holder, contender := open(), open()
	if _, err := holder.Exec("CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	tx, err := holder.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES (0)"); err != nil { // takes the write lock
		t.Fatal(err)
	}
	return contender, func() { tx.Rollback() }
}

func fastRetries(t *testing.T) {
	t.Helper()
	old := retryDelays
	retryDelays = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond}
	t.Cleanup(func() { retryDelays = old })
}

func TestIsBusyOnRealBusyError(t *testing.T) {
	contender, release := busyFixture(t)
	defer release()
	_, err := contender.Exec("INSERT INTO t VALUES (1)")
	if !IsBusy(err) {
		t.Fatalf("IsBusy(%v) = false, want true", err)
	}
	if IsBusy(errors.New("nope")) || IsBusy(nil) {
		t.Error("IsBusy accepted a non-busy error")
	}
}

func TestRetrySucceedsImmediately(t *testing.T) {
	calls := 0
	if err := Retry(context.Background(), func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestRetryNonBusyErrorNotRetried(t *testing.T) {
	calls := 0
	boom := errors.New("boom")
	err := Retry(context.Background(), func() error { calls++; return boom })
	if !errors.Is(err, boom) || calls != 1 {
		t.Errorf("err = %v, calls = %d; want boom after 1 call", err, calls)
	}
}

func TestRetrySucceedsAfterBusy(t *testing.T) {
	fastRetries(t)
	contender, release := busyFixture(t)

	calls := 0
	err := Retry(context.Background(), func() error {
		calls++
		if calls == 3 {
			release()
		}
		_, err := contender.Exec("INSERT INTO t VALUES (1)")
		return err
	})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (two busy, then success once the lock is released)", calls)
	}
}

func TestRetryExhausted(t *testing.T) {
	fastRetries(t)
	contender, release := busyFixture(t)
	defer release()

	calls := 0
	err := Retry(context.Background(), func() error {
		calls++
		_, err := contender.Exec("INSERT INTO t VALUES (1)")
		return err
	})
	var be *BusyExhaustedError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want *BusyExhaustedError", err)
	}
	if be.Attempts != 5 || calls != 5 {
		t.Errorf("attempts = %d, calls = %d; want 5 each (1 try + 4 retries)", be.Attempts, calls)
	}
	if !IsBusy(err) {
		t.Error("exhausted error must still unwrap to SQLITE_BUSY")
	}
}

func TestRetryContextCancelled(t *testing.T) {
	old := retryDelays
	retryDelays = []time.Duration{time.Hour}
	defer func() { retryDelays = old }()

	contender, release := busyFixture(t)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Retry(ctx, func() error {
		_, err := contender.Exec("INSERT INTO t VALUES (1)")
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("Retry did not stop when the context ended")
	}
}

func TestDefaultRetryDelays(t *testing.T) {
	want := []time.Duration{25 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond, time.Second}
	if len(retryDelays) != len(want) {
		t.Fatalf("retryDelays = %v", retryDelays)
	}
	for i := range want {
		if retryDelays[i] != want[i] {
			t.Errorf("retryDelays[%d] = %v, want %v", i, retryDelays[i], want[i])
		}
	}
}

func mkdirAll(p string) error { return os.MkdirAll(p, 0o700) }
