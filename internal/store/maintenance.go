package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
	"github.com/drilonrecica/sinjal/internal/maintenance"
)

// MaxMaintenanceDuration bounds one occurrence of a maintenance window.
const MaxMaintenanceDuration = 31 * 24 * time.Hour

// querier is what both *sql.Tx and *sql.DB offer for reads.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const maintenanceColumns = `id, name, starts_at, duration_seconds, recurrence, weekday_mask,
	suppress_notifications, exclude_from_adjusted_uptime, scope_json, created_at, updated_at`

func scanMaintenance(r scanner) (maintenance.Window, error) {
	var w maintenance.Window
	var start, created, updated string
	var seconds int64
	var mask sql.NullInt64
	var scope sql.NullString
	if err := r.Scan(&w.ID, &w.Name, &start, &seconds, &w.Recurrence, &mask,
		&w.Suppress, &w.ExcludeUptime, &scope, &created, &updated); err != nil {
		return w, err
	}
	w.Start, w.CreatedAt, w.UpdatedAt = parseTime(start), parseTime(created), parseTime(updated)
	w.Duration = time.Duration(seconds) * time.Second
	w.Weekdays = uint8(mask.Int64)
	if scope.Valid {
		if err := json.Unmarshal([]byte(scope.String), &w.Scope); err != nil {
			return w, fmt.Errorf("maintenance window %s: scope: %w", w.ID, err)
		}
	}
	return w, nil
}

// ListMaintenance returns every maintenance window, by start.
func ListMaintenance(ctx context.Context, q querier) ([]maintenance.Window, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_windows ORDER BY starts_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []maintenance.Window
	for rows.Next() {
		w, err := scanMaintenance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetMaintenance returns one window, or ErrNotFound.
func GetMaintenance(ctx context.Context, q querier, id string) (maintenance.Window, error) {
	w, err := scanMaintenance(q.QueryRowContext(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_windows WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// MonitorWindows returns the windows whose scope covers the monitor. The
// table is small: every window is read and the scope decided here.
func MonitorWindows(ctx context.Context, q querier, monitorID string) ([]maintenance.Window, error) {
	all, err := ListMaintenance(ctx, q)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT t.name FROM tags t JOIN monitor_tags mt ON mt.tag_id = t.id
		WHERE mt.monitor_id = ?`, monitorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []maintenance.Window
	for _, w := range all {
		if w.Scope.Covers(monitorID, tags) {
			out = append(out, w)
		}
	}
	return out, nil
}

// validateMaintenance checks a window (docs/38 "Maintenance") and puts it
// in canonical form: trimmed name, no weekdays unless weekly, scope lists
// without duplicates. Monitors and tags in the scope must exist.
func validateMaintenance(ctx context.Context, q querier, w *maintenance.Window) (FieldErrors, error) {
	errs := FieldErrors{}
	w.Name = strings.TrimSpace(w.Name)
	switch {
	case w.Name == "":
		errs.add("name", "Enter a name.")
	case len([]rune(w.Name)) > MaxNameLen:
		errs.add("name", fmt.Sprintf("Use at most %d characters.", MaxNameLen))
	}
	if w.Start.IsZero() {
		errs.add("starts_at", "Enter a start date and time.")
	}
	w.Start = w.Start.Truncate(time.Second)
	switch {
	case w.Duration < time.Minute:
		errs.add("duration", "Use a duration of at least one minute.")
	case w.Duration > MaxMaintenanceDuration:
		errs.add("duration", "Use a duration of at most 31 days.")
	}
	w.Duration = w.Duration.Truncate(time.Second)
	switch w.Recurrence {
	case maintenance.None, maintenance.Daily:
		w.Weekdays = 0
	case maintenance.Weekly:
		if w.Weekdays&0x7f == 0 {
			errs.add("weekdays", "Choose at least one weekday.")
		}
		w.Weekdays &= 0x7f
	default:
		errs.add("recurrence", "Choose once, daily or weekly.")
	}
	slices.Sort(w.Scope.Monitors)
	w.Scope.Monitors = slices.Compact(w.Scope.Monitors)
	for _, id := range w.Scope.Monitors {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM monitors WHERE id = ?`, id).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			errs.add("scope", "A selected monitor does not exist any more.")
		}
	}
	var tags []string
	for _, name := range w.Scope.Tags {
		var canonical string
		err := q.QueryRowContext(ctx, `SELECT name FROM tags WHERE name = ? COLLATE NOCASE`, strings.TrimSpace(name)).Scan(&canonical)
		if errors.Is(err, sql.ErrNoRows) {
			errs.add("scope", "A selected tag does not exist any more.")
			continue
		}
		if err != nil {
			return nil, err
		}
		if !slices.Contains(tags, canonical) {
			tags = append(tags, canonical)
		}
	}
	slices.Sort(tags)
	w.Scope.Tags = tags
	return errs, nil
}

// CheckMaintenance validates a window without storing it, for a form that
// already has problems of its own to show all of them at once.
func CheckMaintenance(ctx context.Context, q querier, w maintenance.Window) error {
	errs, err := validateMaintenance(ctx, q, &w)
	if err != nil || len(errs) == 0 {
		return err
	}
	return errs
}

// scopeJSON is the stored scope: NULL for every monitor.
func scopeJSON(s maintenance.Scope) (any, error) {
	if s.All() {
		return nil, nil
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// CreateMaintenance validates and stores a new window and returns its id.
// A failed validation is a FieldErrors error.
func CreateMaintenance(ctx context.Context, d *db.DB, w maintenance.Window, now time.Time) (string, error) {
	id := ids.New()
	return id, writeMaintenance(ctx, d, &w, func(tx *sql.Tx, scope any) (sql.Result, error) {
		return tx.ExecContext(ctx, `INSERT INTO maintenance_windows (`+maintenanceColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, w.Name, formatTime(w.Start), int64(w.Duration/time.Second), string(w.Recurrence), weekdayMask(w),
			b2i(w.Suppress), b2i(w.ExcludeUptime), scope, formatTime(now), formatTime(now))
	})
}

// UpdateMaintenance replaces a window's settings, or returns ErrNotFound.
func UpdateMaintenance(ctx context.Context, d *db.DB, w maintenance.Window, now time.Time) error {
	return writeMaintenance(ctx, d, &w, func(tx *sql.Tx, scope any) (sql.Result, error) {
		return tx.ExecContext(ctx, `UPDATE maintenance_windows SET name = ?, starts_at = ?, duration_seconds = ?,
			recurrence = ?, weekday_mask = ?, suppress_notifications = ?, exclude_from_adjusted_uptime = ?,
			scope_json = ?, updated_at = ? WHERE id = ?`,
			w.Name, formatTime(w.Start), int64(w.Duration/time.Second), string(w.Recurrence), weekdayMask(w),
			b2i(w.Suppress), b2i(w.ExcludeUptime), scope, formatTime(now), w.ID)
	})
}

func weekdayMask(w maintenance.Window) any {
	if w.Recurrence != maintenance.Weekly {
		return nil
	}
	return int(w.Weekdays)
}

// writeMaintenance validates w inside a write transaction, so the scope is
// checked against the monitors and tags as they are when it is stored.
func writeMaintenance(ctx context.Context, d *db.DB, w *maintenance.Window, write func(*sql.Tx, any) (sql.Result, error)) error {
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		errs, err := validateMaintenance(ctx, tx, w)
		if err != nil {
			return err
		}
		if len(errs) > 0 {
			return errs
		}
		scope, err := scopeJSON(w.Scope)
		if err != nil {
			return err
		}
		res, err := write(tx, scope)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return tx.Commit()
	})
}

// DeleteMaintenance removes a window, or returns ErrNotFound.
func DeleteMaintenance(ctx context.Context, d *db.DB, id string) error {
	return db.Retry(ctx, func() error {
		res, err := d.Writer.ExecContext(ctx, `DELETE FROM maintenance_windows WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
