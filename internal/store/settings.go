package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Instance-level values kept in system_settings, one text value per key.

// Setting reads a value; ok is false when the key has none.
func Setting(ctx context.Context, q querier, key string) (value string, ok bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT value FROM system_settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetSetting stores a value, replacing the key's previous one.
func SetSetting(ctx context.Context, ex execer, key, value string, now time.Time) error {
	_, err := ex.ExecContext(ctx, `INSERT INTO system_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, formatTime(now))
	return err
}
