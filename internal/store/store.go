// Package store is concrete SQL access for monitors and their satellites
// (docs/08_DATA_MODEL.md, docs/28_FILE_LAYOUT.md). Plain functions over the
// db package's writer and reader handles; there are no repository
// interfaces. Monitor semantics (state, thresholds) live in internal/monitor.
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound means no row has the requested id.
var ErrNotFound = errors.New("store: not found")

// InputError is a user-correctable validation failure on one field.
type InputError struct {
	Field, Message string
}

func (e *InputError) Error() string { return e.Field + ": " + e.Message }

// execer is what both *sql.Tx and *sql.DB offer for statements.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func parseNullTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTime(s.String)
	return &t
}
