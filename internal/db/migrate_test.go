package db

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func fixedClock() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }

func v1() fstest.MapFS {
	return fstest.MapFS{"001_foundation.sql": {Data: []byte(`CREATE TABLE settings (k TEXT PRIMARY KEY, v TEXT NOT NULL);`)}}
}

func v1v2() fstest.MapFS {
	m := v1()
	m["002_extra.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE extra (id INTEGER PRIMARY KEY);`)}
	return m
}

type env struct {
	db        *DB
	backupDir string
}

func newEnv(t *testing.T) env {
	t.Helper()
	root := t.TempDir()
	backups := filepath.Join(root, "backups")
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := Open(filepath.Join(root, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return env{db: d, backupDir: backups}
}

func (e env) migrate(fsys fstest.MapFS) error {
	return migrate(context.Background(), e.db.Writer, fsys, e.backupDir, "1.2.3", quiet, fixedClock)
}

func (e env) backups(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(e.backupDir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func (e env) versions(t *testing.T) []int {
	t.Helper()
	rows, err := e.db.Writer.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func (e env) tableExists(t *testing.T, name string) bool {
	t.Helper()
	var n int
	if err := e.db.Writer.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFreshDatabaseMigratesWithoutBackup(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(v1v2()); err != nil {
		t.Fatal(err)
	}
	if got := e.versions(t); !equalInts(got, []int{1, 2}) {
		t.Errorf("versions = %v, want [1 2]", got)
	}
	if !e.tableExists(t, "settings") || !e.tableExists(t, "extra") {
		t.Error("migrations were not applied")
	}
	if got := e.backups(t); len(got) != 0 {
		t.Errorf("a brand-new database must not be backed up, found %v", got)
	}
	var at string
	if err := e.db.Writer.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 1`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at != "2026-10-06T12:00:00Z" {
		t.Errorf("applied_at = %q", at)
	}
}

func TestRerunIsNoOpWithoutBackup(t *testing.T) {
	e := newEnv(t)
	for range 3 {
		if err := e.migrate(v1v2()); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.backups(t); len(got) != 0 {
		t.Errorf("up-to-date database must not be backed up, found %v", got)
	}
	if got := e.versions(t); !equalInts(got, []int{1, 2}) {
		t.Errorf("versions = %v", got)
	}
}

func TestUpgradeBacksUpFirst(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(v1()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Writer.Exec(`INSERT INTO settings VALUES ('theme', 'carbon')`); err != nil {
		t.Fatal(err)
	}

	if err := e.migrate(v1v2()); err != nil {
		t.Fatal(err)
	}
	if !e.tableExists(t, "extra") {
		t.Error("upgrade was not applied")
	}

	files := e.backups(t)
	want := filepath.Join(e.backupDir, "pre-migration-1.2.3-20261006T120000Z.db")
	if len(files) != 1 || files[0] != want {
		t.Fatalf("backups = %v, want [%s]", files, want)
	}
	if info, _ := os.Stat(want); info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %o, want 600", info.Mode().Perm())
	}

	// The backup is a valid database holding the pre-upgrade state.
	b, err := sql.Open("sqlite", "file:"+want+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var v string
	if err := b.QueryRow(`SELECT v FROM settings WHERE k = 'theme'`).Scan(&v); err != nil || v != "carbon" {
		t.Errorf("backup row = %q, err = %v", v, err)
	}
	var max int
	if err := b.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&max); err != nil || max != 1 {
		t.Errorf("backup schema version = %d, err = %v; want 1 (pre-migration state)", max, err)
	}
	var n int
	if err := b.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'extra'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("backup must not contain the new table (n=%d, err=%v)", n, err)
	}
}

func TestFailingMigrationRollsBackAtomically(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(v1()); err != nil {
		t.Fatal(err)
	}
	bad := v1()
	bad["002_bad.sql"] = &fstest.MapFile{Data: []byte(`
CREATE TABLE half_done (id INTEGER);
CREATE TABLE half_done (id INTEGER);`)}

	err := e.migrate(bad)
	if err == nil || !strings.Contains(err.Error(), "002_bad.sql") {
		t.Fatalf("err = %v, want one naming 002_bad.sql", err)
	}
	if e.tableExists(t, "half_done") {
		t.Error("a failed migration left partial changes behind")
	}
	if got := e.versions(t); !equalInts(got, []int{1}) {
		t.Errorf("versions = %v, want [1]", got)
	}
	if got := e.backups(t); len(got) != 1 {
		t.Errorf("backup must exist after a failed upgrade, got %v", got)
	}
}

func TestFailureKeepsEarlierMigrationsApplied(t *testing.T) {
	e := newEnv(t)
	fsys := v1v2()
	fsys["003_bad.sql"] = &fstest.MapFile{Data: []byte(`THIS IS NOT SQL;`)}
	if err := e.migrate(fsys); err == nil {
		t.Fatal("expected an error")
	}
	if got := e.versions(t); !equalInts(got, []int{1, 2}) {
		t.Errorf("versions = %v, want [1 2]", got)
	}
}

func TestBackupFailureAbortsBeforeMigrating(t *testing.T) {
	for name, setup := range map[string]func(dir string) error{
		"backup dir missing": func(dir string) error { return os.Remove(dir) },
		"backup dir is file": func(dir string) error {
			if err := os.Remove(dir); err != nil {
				return err
			}
			return os.WriteFile(dir, nil, 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if err := e.migrate(v1()); err != nil {
				t.Fatal(err)
			}
			if err := setup(e.backupDir); err != nil {
				t.Fatal(err)
			}
			err := e.migrate(v1v2())
			if err == nil || !strings.Contains(err.Error(), "nothing was migrated") {
				t.Fatalf("err = %v, want a backup failure", err)
			}
			if e.tableExists(t, "extra") {
				t.Error("migration ran although the backup failed")
			}
			if got := e.versions(t); !equalInts(got, []int{1}) {
				t.Errorf("versions = %v, want [1]", got)
			}
		})
	}
}

func TestExistingBackupIsNeverOverwritten(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(v1()); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(e.backupDir, "pre-migration-1.2.3-20261006T120000Z.db")
	if err := os.WriteFile(existing, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.migrate(v1v2()); err == nil {
		t.Fatal("expected the backup to refuse to overwrite")
	}
	if b, _ := os.ReadFile(existing); string(b) != "precious" {
		t.Error("existing backup was overwritten")
	}
	if e.tableExists(t, "extra") {
		t.Error("migration ran although the backup failed")
	}
}

func TestDatabaseNewerThanBinaryIsRefused(t *testing.T) {
	e := newEnv(t)
	if err := e.migrate(v1v2()); err != nil {
		t.Fatal(err)
	}
	err := e.migrate(v1()) // an older binary knowing only version 1
	if err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("err = %v, want a newer-than-binary error", err)
	}
	if got := e.versions(t); !equalInts(got, []int{1, 2}) {
		t.Errorf("versions changed: %v", got)
	}
	if got := e.backups(t); len(got) != 0 {
		t.Errorf("a refused database must not be touched, found %v", got)
	}
}

func TestNumberingMustBeContiguous(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"gap": {
			"001_a.sql": {Data: []byte("SELECT 1;")},
			"003_c.sql": {Data: []byte("SELECT 1;")},
		},
		"does not start at 1": {"002_b.sql": {Data: []byte("SELECT 1;")}},
		"duplicate": {
			"001_a.sql": {Data: []byte("SELECT 1;")},
			"001_b.sql": {Data: []byte("SELECT 1;")},
		},
		"bad name": {"1_a.sql": {Data: []byte("SELECT 1;")}},
	}
	for name, fsys := range tests {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if err := e.migrate(fsys); err == nil {
				t.Fatal("expected a numbering error")
			}
			if e.tableExists(t, "schema_migrations") {
				t.Error("nothing should be touched when the migration set is invalid")
			}
		})
	}
}

func TestNonSQLFilesIgnored(t *testing.T) {
	e := newEnv(t)
	fsys := v1()
	fsys[".gitkeep"] = &fstest.MapFile{}
	fsys["README.md"] = &fstest.MapFile{Data: []byte("notes")}
	if err := e.migrate(fsys); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeVersion(t *testing.T) {
	tests := map[string]string{
		"1.2.3":               "1.2.3",
		"v0.3.0-4-gabc-dirty": "v0.3.0-4-gabc-dirty",
		"../../etc/passwd":    ".._.._etc_passwd",
		"a b'c":               "a_b_c",
		"":                    "unknown",
	}
	for in, want := range tests {
		if got := sanitizeVersion(in); got != want {
			t.Errorf("sanitizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBackupPathWithQuoteIsEscaped(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "it's backups")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := Open(filepath.Join(root, "sinjal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := migrate(context.Background(), d.Writer, v1(), dir, "1", quiet, fixedClock); err != nil {
		t.Fatal(err)
	}
	if err := migrate(context.Background(), d.Writer, v1v2(), dir, "1", quiet, fixedClock); err != nil {
		t.Fatalf("backup into a directory containing a quote failed: %v", err)
	}
}

func TestEmbeddedMigrationsAreValid(t *testing.T) {
	// Real embedded set: must always load (contiguous, well named).
	e := newEnv(t)
	if err := Migrate(context.Background(), e.db, e.backupDir, "test", quiet); err != nil {
		t.Fatal(err)
	}
}
