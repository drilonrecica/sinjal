package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
)

// heartbeatTokenBytes is the size of a heartbeat token before encoding.
const heartbeatTokenBytes = 32

// MaxSourceLabelLen caps a heartbeat's source label.
const MaxSourceLabelLen = 100

// NewHeartbeatToken returns a new heartbeat token (32 random bytes,
// base64url without padding) and the hash that is stored for it. The token
// itself is shown once and never stored.
func NewHeartbeatToken() (token string, hash []byte, err error) {
	raw := make([]byte, heartbeatTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	h := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), h[:], nil
}

// HashHeartbeatToken returns the stored hash of a presented token, or
// false when it cannot be a token Sinjal issued.
func HashHeartbeatToken(token string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != heartbeatTokenBytes {
		return nil, false
	}
	h := sha256.Sum256(raw)
	return h[:], true
}

// HeartbeatConfig is one row of heartbeat_monitor_config, without the
// token hash.
type HeartbeatConfig struct {
	ExpectedInterval time.Duration
	Grace            time.Duration
	SourceLabel      string
	LastBeatAt       *time.Time
	// WatchedSince is when beats were first expected: the monitor's
	// creation or its last resume. Read only.
	WatchedSince time.Time
}

// watchedSince is the SQL for HeartbeatConfig.WatchedSince of monitor m;
// timestamps share one format, so the text maximum is the latest.
const watchedSince = `max(m.created_at, coalesce(
	(SELECT max(p.resumed_at) FROM monitor_pauses p WHERE p.monitor_id = m.id), ''))`

// Validate checks the rules of docs/38: a positive interval, a grace that
// is not negative, a short source label.
func (c HeartbeatConfig) Validate() error {
	switch {
	case c.ExpectedInterval < time.Second:
		return &InputError{Field: "expected_interval", Message: "must be at least one second"}
	case c.Grace < 0:
		return &InputError{Field: "grace", Message: "must not be negative"}
	case len([]rune(c.SourceLabel)) > MaxSourceLabelLen:
		return &InputError{Field: "source_label", Message: "must be at most 100 characters"}
	}
	return nil
}

// Period is how long a heartbeat monitor may go without a beat.
func (c HeartbeatConfig) Period() time.Duration { return c.ExpectedInterval + c.Grace }

// Deadline is when a heartbeat monitor is late: a period after its last
// beat, or after it was created or resumed if that came later, so neither
// a beat from before a pause nor the lack of any beat makes it late at
// once. Stored times have second precision, so the deadline may be up to
// a second early.
func (c HeartbeatConfig) Deadline() time.Time {
	from := c.WatchedSince
	if c.LastBeatAt != nil && c.LastBeatAt.After(from) {
		from = *c.LastBeatAt
	}
	return from.Add(c.Period())
}

// GetHeartbeatConfig returns a heartbeat monitor's configuration.
func GetHeartbeatConfig(ctx context.Context, q *sql.DB, monitorID string) (HeartbeatConfig, error) {
	var c HeartbeatConfig
	var interval, grace int
	var label, last sql.NullString
	var watched string
	err := q.QueryRowContext(ctx, `SELECT h.expected_interval_seconds, h.grace_seconds, h.source_label, h.last_beat_at, `+watchedSince+`
		FROM heartbeat_monitor_config h JOIN monitors m ON m.id = h.monitor_id
		WHERE h.monitor_id = ?`, monitorID).Scan(&interval, &grace, &label, &last, &watched)
	if errors.Is(err, sql.ErrNoRows) {
		return HeartbeatConfig{}, ErrNotFound
	}
	if err != nil {
		return HeartbeatConfig{}, err
	}
	c.ExpectedInterval = time.Duration(interval) * time.Second
	c.Grace = time.Duration(grace) * time.Second
	c.SourceLabel = label.String
	c.LastBeatAt = parseNullTime(last)
	c.WatchedSince = parseTime(watched)
	return c, nil
}

// SetHeartbeatToken gives a heartbeat monitor a new token and returns it;
// the previous token stops working at once. ErrNotFound when the monitor
// has no heartbeat configuration.
func SetHeartbeatToken(ctx context.Context, d *db.DB, monitorID string) (string, error) {
	token, hash, err := NewHeartbeatToken()
	if err != nil {
		return "", err
	}
	err = db.Retry(ctx, func() error {
		res, err := d.Writer.ExecContext(ctx, `UPDATE heartbeat_monitor_config SET token_hash = ? WHERE monitor_id = ?`, hash, monitorID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// RecordBeat stores now as the last beat of the monitor whose token hashes
// to hash and returns its id. ErrNotFound when no monitor has that token.
// The beat is recorded for a paused monitor too; it is not a check result.
func RecordBeat(ctx context.Context, d *db.DB, hash []byte, now time.Time) (string, error) {
	var id string
	err := db.Retry(ctx, func() error {
		err := d.Writer.QueryRowContext(ctx, `UPDATE heartbeat_monitor_config SET last_beat_at = ?
			WHERE token_hash = ? RETURNING monitor_id`, formatTime(now), hash).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return id, err
}
