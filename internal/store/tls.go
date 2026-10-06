package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// TLSWarningDays returns the warning thresholds of a monitor whose HTTPS
// certificate expiry is watched, nil when it is not (no HTTP settings, or
// expiry warnings off).
func TLSWarningDays(ctx context.Context, tx *sql.Tx, monitorID string) ([]int, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT tls_warning_days_json FROM http_monitor_config
		WHERE monitor_id = ? AND tls_expiry_enabled = 1`, monitorID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var days []int
	if err := json.Unmarshal([]byte(raw), &days); err != nil {
		return nil, err
	}
	return days, nil
}

// CrossTLSThresholds records that a certificate reached the given warning
// thresholds and reports whether any of them is new for that certificate
// (docs/06 "TLS"). A certificate with another expiry, a renewed one,
// starts afresh: rows of the monitor's earlier certificates are removed
// when the new one first reaches a threshold. Nothing is written when
// every threshold had been recorded.
func CrossTLSThresholds(ctx context.Context, tx *sql.Tx, monitorID string, notAfter time.Time, thresholds []int, at time.Time) (bool, error) {
	if len(thresholds) == 0 {
		return false, nil
	}
	cert := formatTime(notAfter)
	rows, err := tx.QueryContext(ctx, `SELECT threshold_days FROM tls_warnings WHERE monitor_id = ? AND cert_not_after = ?`, monitorID, cert)
	if err != nil {
		return false, err
	}
	var seen []int
	for rows.Next() {
		var d int
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return false, err
		}
		seen = append(seen, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	var fresh []int
	for _, d := range thresholds {
		if !slices.Contains(seen, d) && !slices.Contains(fresh, d) {
			fresh = append(fresh, d)
		}
	}
	if len(fresh) == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tls_warnings WHERE monitor_id = ? AND cert_not_after <> ?`, monitorID, cert); err != nil {
		return false, err
	}
	for _, d := range fresh {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tls_warnings (monitor_id, cert_not_after, threshold_days, notified_at)
			VALUES (?, ?, ?, ?)`, monitorID, cert, d, formatTime(at)); err != nil {
			return false, err
		}
	}
	return true, nil
}
