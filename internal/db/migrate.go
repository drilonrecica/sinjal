package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations
var embeddedMigrations embed.FS

var migrationName = regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate brings the database to the newest embedded schema version.
//
// Migrations are forward-only. Before applying anything to a database that
// already has tables, it writes a mandatory backup with VACUUM INTO into
// backupDir; if that fails, nothing is migrated. A database whose recorded
// version is newer than this binary knows is refused untouched.
func Migrate(ctx context.Context, d *DB, backupDir, appVersion string, log *slog.Logger) error {
	fsys, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		return err
	}
	return migrate(ctx, d.Writer, fsys, backupDir, appVersion, log, time.Now)
}

func migrate(ctx context.Context, w *sql.DB, fsys fs.FS, backupDir, appVersion string, log *slog.Logger, now func() time.Time) error {
	migs, err := loadMigrations(fsys)
	if err != nil {
		return err
	}

	// Must be decided before schema_migrations is created below.
	brandNew, err := isEmpty(ctx, w)
	if err != nil {
		return err
	}
	if _, err := w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var current int
	if err := w.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	newest := 0
	if len(migs) > 0 {
		newest = migs[len(migs)-1].version
	}
	if current > newest {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d): upgrade sinjal or restore a backup", current, newest)
	}

	pending := migs[current:] // versions are contiguous from 1
	if len(pending) == 0 {
		return nil
	}

	if !brandNew {
		path, err := backup(ctx, w, backupDir, appVersion, now())
		if err != nil {
			return fmt.Errorf("pre-migration backup failed, nothing was migrated: %w", err)
		}
		log.Info("pre-migration backup written", "path", path, "from_version", current)
	}

	for _, m := range pending {
		if err := apply(ctx, w, m, now()); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		log.Info("migration applied", "version", m.version, "name", m.name)
	}
	return nil
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var migs []migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		m := migrationName.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf("migration file %q must be named NNN_name.sql", name)
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		v, _ := strconv.Atoi(m[1])
		migs = append(migs, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	for i, m := range migs {
		if want := i + 1; m.version != want {
			return nil, fmt.Errorf("migration numbering must be contiguous from 001: expected version %03d, found %s", want, m.name)
		}
	}
	return migs, nil
}

func isEmpty(ctx context.Context, w *sql.DB) (bool, error) {
	var n int
	err := w.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("inspect database: %w", err)
	}
	return n == 0, nil
}

// backup snapshots the live database with VACUUM INTO, which is consistent
// with respect to the WAL and fails rather than overwrite an existing file.
func backup(ctx context.Context, w *sql.DB, dir, appVersion string, t time.Time) (string, error) {
	name := fmt.Sprintf("pre-migration-%s-%s.db", sanitizeVersion(appVersion), t.UTC().Format("20060102T150405Z"))
	path := filepath.Join(dir, name)
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if _, err := w.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// sanitizeVersion keeps the app version safe for use inside a file name.
func sanitizeVersion(v string) string {
	if v == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, v)
}

func apply(ctx context.Context, w *sql.DB, m migration, t time.Time) (err error) {
	tx, err := w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if _, err = tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, t.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}
