package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The functions in this file are the write path for check results. They
// take the result processor's transaction: workers never write to SQLite
// themselves (docs/09_DATABASE.md).

// CheckResult is one row of check_results.
type CheckResult struct {
	MonitorID      string
	CheckedAt      time.Time
	Duration       time.Duration
	Success        bool
	ProtocolStatus string // "" is stored as NULL, like the fields below
	ErrorKind      string
	ErrorMessage   string
	Snippet        string
	Metadata       string // JSON text
}

// CheckState is what applying a result needs from the monitor row.
type CheckState struct {
	State            string
	StateSince       string // as stored; see FormatTime
	FailureThreshold int
	SuccessThreshold int
	RetryDelay       time.Duration
}

// FormatTime is the text form timestamps are stored in.
func FormatTime(t time.Time) string { return formatTime(t) }

// GetCheckState reads a monitor's state and thresholds inside tx, or
// ErrNotFound when the monitor has been deleted.
func GetCheckState(ctx context.Context, tx *sql.Tx, monitorID string) (CheckState, error) {
	var s CheckState
	var retryMS int
	err := tx.QueryRowContext(ctx, `SELECT current_state, current_state_since, failure_threshold,
		success_threshold, retry_delay_ms FROM monitors WHERE id = ?`, monitorID).
		Scan(&s.State, &s.StateSince, &s.FailureThreshold, &s.SuccessThreshold, &retryMS)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckState{}, ErrNotFound
	}
	s.RetryDelay = time.Duration(retryMS) * time.Millisecond
	return s, err
}

// InsertCheckResult appends one raw result.
func InsertCheckResult(ctx context.Context, tx *sql.Tx, r CheckResult) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO check_results
		(monitor_id, checked_at, duration_ms, success, protocol_status, error_kind, error_message,
		 diagnostic_snippet, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.MonitorID, formatTime(r.CheckedAt), float64(r.Duration)/float64(time.Millisecond), b2i(r.Success),
		nullStr(r.ProtocolStatus), nullStr(r.ErrorKind), nullStr(r.ErrorMessage), nullStr(r.Snippet), nullStr(r.Metadata))
	return err
}

// CheckUpdate is the effect of one result on its monitor row.
type CheckUpdate struct {
	State        string
	StateChanged bool // current_state_since moves to CheckedAt
	CheckedAt    time.Time
	Success      bool
	TLSNotAfter  *time.Time // certificate expiry seen by this check, if any
}

// ApplyCheck records a result on the monitor row: state, last check and
// last success or failure. The certificate expiry is replaced when the
// check saw one, cleared when a check succeeded without one (the monitor is
// no longer HTTPS, or expiry checks are off) and otherwise kept, because a
// failed check says nothing about the certificate.
func ApplyCheck(ctx context.Context, tx *sql.Tx, monitorID string, u CheckUpdate) error {
	at := formatTime(u.CheckedAt)
	var since, ok, failed, tls any
	if u.StateChanged {
		since = at
	}
	if u.Success {
		ok = at
	} else {
		failed = at
	}
	if u.TLSNotAfter != nil {
		tls = formatTime(*u.TLSNotAfter)
	}
	_, err := tx.ExecContext(ctx, `UPDATE monitors SET
		current_state = ?,
		current_state_since = COALESCE(?, current_state_since),
		last_check_at = ?,
		last_success_at = COALESCE(?, last_success_at),
		last_failure_at = COALESCE(?, last_failure_at),
		tls_not_after = CASE WHEN ? IS NOT NULL THEN ? WHEN ? THEN NULL ELSE tls_not_after END
		WHERE id = ?`,
		u.State, since, at, ok, failed, tls, tls, b2i(u.Success), monitorID)
	return err
}
