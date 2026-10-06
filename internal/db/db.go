// Package db owns the SQLite connections: one writer, a pool of readers,
// and the bounded busy-retry helper (docs/09_DATABASE.md).
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"runtime"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// basePragmas is the documented baseline (docs/09_DATABASE.md), applied to
// every connection by the driver.
var basePragmas = []string{
	"journal_mode(WAL)",
	"foreign_keys(1)",
	"synchronous(NORMAL)",
	"busy_timeout(5000)",
}

// DB holds the two connection handles. All writes go through Writer, which
// has a single connection, so there is exactly one write path. Reader is a
// pool that cannot write.
type DB struct {
	Writer *sql.DB
	Reader *sql.DB
}

// Open opens (creating if needed) the database file at path.
func Open(path string) (*DB, error) {
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, fmt.Errorf("open sqlite writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	// Opening the writer first establishes WAL mode before readers connect.
	if err := w.Ping(); err != nil {
		w.Close()
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}

	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("open sqlite reader: %w", err)
	}
	r.SetMaxOpenConns(max(4, runtime.NumCPU()))
	if err := r.Ping(); err != nil {
		w.Close()
		r.Close()
		return nil, fmt.Errorf("open database %q for reading: %w", path, err)
	}
	return &DB{Writer: w, Reader: r}, nil
}

// Close closes both handles, returning the first error.
func (d *DB) Close() error {
	return errors.Join(d.Reader.Close(), d.Writer.Close())
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range basePragmas {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}
