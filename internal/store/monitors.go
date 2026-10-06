package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/drilonrecica/sinjal/internal/db"
	"github.com/drilonrecica/sinjal/internal/ids"
)

// Defaults for a new monitor (docs/08, mirrored by the schema).
const (
	DefaultIntervalSeconds  = 30
	DefaultTimeoutMS        = 5000
	DefaultFailureThreshold = 2
	DefaultRetryDelayMS     = 5000
	DefaultSuccessThreshold = 1
	DefaultExpectedStatus   = "200-399"
	DefaultMaxBodyBytes     = 1048576
	DefaultTLSWarningDays   = "[30,14,7]"
	MaxNameLen              = 100
)

// Monitor is one row of monitors.
type Monitor struct {
	ID                    string
	Name                  string
	Type                  string
	Enabled               bool
	State                 string
	StateSince            time.Time
	FlappingSince         *time.Time
	IntervalSeconds       int
	TimeoutMS             int
	FailureThreshold      int
	RetryDelayMS          int
	SuccessThreshold      int
	ParentMonitorID       string
	NotificationProfileID string
	LastCheckAt           *time.Time
	LastSuccessAt         *time.Time
	LastFailureAt         *time.Time
	TLSNotAfter           *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// HTTPConfig is one row of http_monitor_config. Empty strings in the
// nullable text fields are stored as NULL.
type HTTPConfig struct {
	URL                string
	Method             string
	FollowRedirects    bool
	ExpectedStatus     string
	BodyContains       string
	BodyNotContains    string
	JSONAssertions     string // JSON text
	Headers            string // JSON text; values that are secrets live in monitor_secrets
	RequestBody        string
	CustomUserAgent    string
	MaxBodyBytes       int
	TLSExpiryEnabled   bool
	TLSWarningDays     string // JSON array text
	InsecureSkipVerify bool
	ProxyURL           string
	IPFamily           string
}

// MonitorInput is the editable shape of a monitor: the monitor row's
// user-set fields, the config of its type and its tags. Only the config
// matching Type is read. Zero numeric fields and empty text fields take the
// defaults above.
type MonitorInput struct {
	Type                  string // TypeHTTP when empty
	Name                  string
	Enabled               bool
	IntervalSeconds       int // heartbeat: taken from Heartbeat.ExpectedIntervalSeconds
	TimeoutMS             int
	FailureThreshold      int
	RetryDelayMS          int
	SuccessThreshold      int
	ParentMonitorID       string
	NotificationProfileID string
	HTTP                  HTTPConfig
	TCP                   TCPConfig
	ICMP                  ICMPConfig
	DNS                   DNSConfig
	Heartbeat             HeartbeatSettings
	Tags                  []string
}

func (m *MonitorInput) applyDefaults() {
	set := func(p *int, def int) {
		if *p == 0 {
			*p = def
		}
	}
	if m.Type == "" {
		m.Type = TypeHTTP
	}
	if m.Type == TypeHeartbeat {
		// A heartbeat monitor runs no check of its own: its cadence is the
		// expected interval, and the timeout is never used.
		m.IntervalSeconds = m.Heartbeat.ExpectedIntervalSeconds
	}
	set(&m.IntervalSeconds, DefaultIntervalSeconds)
	set(&m.TimeoutMS, DefaultTimeoutMS)
	set(&m.FailureThreshold, DefaultFailureThreshold)
	set(&m.RetryDelayMS, DefaultRetryDelayMS)
	set(&m.SuccessThreshold, DefaultSuccessThreshold)
	m.Name = strings.TrimSpace(m.Name)
	switch m.Type {
	case TypeHTTP:
		c := &m.HTTP
		set(&c.MaxBodyBytes, DefaultMaxBodyBytes)
		c.URL = strings.TrimSpace(c.URL)
		if c.Method == "" {
			c.Method = "GET"
		}
		if c.ExpectedStatus == "" {
			c.ExpectedStatus = DefaultExpectedStatus
		}
		if c.TLSWarningDays == "" {
			c.TLSWarningDays = DefaultTLSWarningDays
		}
	case TypeTCP:
		m.TCP.Host = strings.TrimSpace(m.TCP.Host)
	case TypeICMP:
		m.ICMP.Host = strings.TrimSpace(m.ICMP.Host)
	case TypeDNS:
		c := &m.DNS
		c.Hostname, c.Resolver = strings.TrimSpace(c.Hostname), strings.TrimSpace(c.Resolver)
		if c.QueryType == "" {
			c.QueryType = "A"
		}
		if c.MatchMode == "" {
			c.MatchMode = "all"
		}
	case TypeHeartbeat:
		m.Heartbeat.SourceLabel = strings.TrimSpace(m.Heartbeat.SourceLabel)
	}
}

// CreateMonitor inserts the monitor, the config of its type and its tags in
// one transaction and returns the new id. The initial state is pending (no
// check has run), or paused when created disabled. Invalid input is a
// FieldErrors covering every rule of docs/38, and nothing is written. A
// heartbeat monitor needs Heartbeat.TokenHash (NewHeartbeatToken).
func CreateMonitor(ctx context.Context, d *db.DB, in MonitorInput, now time.Time) (string, error) {
	in.applyDefaults()
	if in.Type == TypeHeartbeat && len(in.Heartbeat.TokenHash) == 0 {
		return "", errors.New("store: a heartbeat monitor needs a token hash")
	}
	errs := in.validate("")
	id := ids.New()
	ts := formatTime(now)
	state := "pending"
	if !in.Enabled {
		state = "paused"
	}
	err := db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := checkRefs(ctx, tx, "", &in, errs); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO monitors
			(id, name, type, enabled, current_state, current_state_since, interval_seconds, timeout_ms,
			 failure_threshold, retry_delay_ms, success_threshold, parent_monitor_id, notification_profile_id,
			 created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.Name, in.Type, b2i(in.Enabled), state, ts, in.IntervalSeconds, in.TimeoutMS,
			in.FailureThreshold, in.RetryDelayMS, in.SuccessThreshold, nullStr(in.ParentMonitorID), nullStr(in.NotificationProfileID),
			ts, ts); err != nil {
			return err
		}
		if err := insertConfig(ctx, tx, id, &in); err != nil {
			return err
		}
		if err := setTags(ctx, tx, id, in.Tags); err != nil {
			return err
		}
		// A monitor created paused is paused from its first moment: the
		// interval keeps that time out of uptime, as for any pause.
		if !in.Enabled {
			if _, err := tx.ExecContext(ctx, `INSERT INTO monitor_pauses (monitor_id, paused_at) VALUES (?, ?)`, id, ts); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// UpdateMonitor replaces the editable fields, config and tags of a monitor
// of type in.Type; the type itself never changes (ErrNotFound for a monitor
// of another type). State, enabled-ness and check history are untouched
// (pausing and resuming are separate operations), as are a heartbeat
// monitor's token and last beat. Invalid input is a FieldErrors and nothing
// is written.
func UpdateMonitor(ctx context.Context, d *db.DB, id string, in MonitorInput, now time.Time) error {
	in.applyDefaults()
	errs := in.validate(id)
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := checkRefs(ctx, tx, id, &in, errs); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE monitors SET name = ?, interval_seconds = ?, timeout_ms = ?,
			failure_threshold = ?, retry_delay_ms = ?, success_threshold = ?, parent_monitor_id = ?,
			notification_profile_id = ?, updated_at = ? WHERE id = ? AND type = ?`,
			in.Name, in.IntervalSeconds, in.TimeoutMS, in.FailureThreshold, in.RetryDelayMS, in.SuccessThreshold,
			nullStr(in.ParentMonitorID), nullStr(in.NotificationProfileID), formatTime(now), id, in.Type)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if err := replaceConfig(ctx, tx, id, &in); err != nil {
			return err
		}
		if err := setTags(ctx, tx, id, in.Tags); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// DeleteMonitor removes a monitor; config, secrets, pauses, tag links and
// check results go with it (ON DELETE CASCADE), and tags no monitor uses
// any more are pruned.
func DeleteMonitor(ctx context.Context, d *db.DB, id string) error {
	return db.Retry(ctx, func() error {
		tx, err := d.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(ctx, `DELETE FROM monitors WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id NOT IN (SELECT tag_id FROM monitor_tags)`); err != nil {
			return err
		}
		return tx.Commit()
	})
}

const monitorColumns = `id, name, type, enabled, current_state, current_state_since, flapping_since,
	interval_seconds, timeout_ms, failure_threshold, retry_delay_ms, success_threshold,
	parent_monitor_id, notification_profile_id, last_check_at, last_success_at, last_failure_at,
	tls_not_after, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanMonitor(r scanner) (Monitor, error) {
	var m Monitor
	var enabled int
	var since, created, updated string
	var flap, parent, profile, lastCheck, lastOK, lastFail, tlsAfter sql.NullString
	err := r.Scan(&m.ID, &m.Name, &m.Type, &enabled, &m.State, &since, &flap,
		&m.IntervalSeconds, &m.TimeoutMS, &m.FailureThreshold, &m.RetryDelayMS, &m.SuccessThreshold,
		&parent, &profile, &lastCheck, &lastOK, &lastFail, &tlsAfter, &created, &updated)
	if err != nil {
		return Monitor{}, err
	}
	m.Enabled = enabled == 1
	m.StateSince = parseTime(since)
	m.FlappingSince = parseNullTime(flap)
	m.ParentMonitorID = parent.String
	m.NotificationProfileID = profile.String
	m.LastCheckAt = parseNullTime(lastCheck)
	m.LastSuccessAt = parseNullTime(lastOK)
	m.LastFailureAt = parseNullTime(lastFail)
	m.TLSNotAfter = parseNullTime(tlsAfter)
	m.CreatedAt = parseTime(created)
	m.UpdatedAt = parseTime(updated)
	return m, nil
}

// GetMonitor returns one monitor row. It never reads monitor_secrets.
func GetMonitor(ctx context.Context, q *sql.DB, id string) (Monitor, error) {
	m, err := scanMonitor(q.QueryRowContext(ctx, `SELECT `+monitorColumns+` FROM monitors WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Monitor{}, ErrNotFound
	}
	return m, err
}

// ListMonitors returns every monitor, by name, without secrets and without
// type-specific config.
func ListMonitors(ctx context.Context, q *sql.DB) ([]Monitor, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+monitorColumns+` FROM monitors ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetHTTPConfig returns the HTTP config of a monitor.
func GetHTTPConfig(ctx context.Context, q *sql.DB, monitorID string) (HTTPConfig, error) {
	var c HTTPConfig
	var follow, tlsOn, insecure int
	var contains, notContains, jsonA, headers, body, ua, proxy, family sql.NullString
	err := q.QueryRowContext(ctx, `SELECT url, method, follow_redirects, expected_status, body_contains,
		body_not_contains, json_assertions_json, headers_json, request_body, custom_user_agent, max_body_bytes,
		tls_expiry_enabled, tls_warning_days_json, insecure_skip_verify, proxy_url, ip_family
		FROM http_monitor_config WHERE monitor_id = ?`, monitorID).
		Scan(&c.URL, &c.Method, &follow, &c.ExpectedStatus, &contains, &notContains, &jsonA, &headers, &body,
			&ua, &c.MaxBodyBytes, &tlsOn, &c.TLSWarningDays, &insecure, &proxy, &family)
	if errors.Is(err, sql.ErrNoRows) {
		return HTTPConfig{}, ErrNotFound
	}
	if err != nil {
		return HTTPConfig{}, err
	}
	c.FollowRedirects, c.TLSExpiryEnabled, c.InsecureSkipVerify = follow == 1, tlsOn == 1, insecure == 1
	c.BodyContains, c.BodyNotContains, c.JSONAssertions = contains.String, notContains.String, jsonA.String
	c.Headers, c.RequestBody, c.CustomUserAgent = headers.String, body.String, ua.String
	c.ProxyURL, c.IPFamily = proxy.String, family.String
	return c, nil
}

func insertHTTPConfig(ctx context.Context, x execer, id string, c HTTPConfig) error {
	_, err := x.ExecContext(ctx, `INSERT INTO http_monitor_config
		(monitor_id, url, method, follow_redirects, expected_status, body_contains, body_not_contains,
		 json_assertions_json, headers_json, request_body, custom_user_agent, max_body_bytes,
		 tls_expiry_enabled, tls_warning_days_json, insecure_skip_verify, proxy_url, ip_family)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, c.URL, c.Method, b2i(c.FollowRedirects), c.ExpectedStatus, nullStr(c.BodyContains),
		nullStr(c.BodyNotContains), nullStr(c.JSONAssertions), nullStr(c.Headers), nullStr(c.RequestBody),
		nullStr(c.CustomUserAgent), c.MaxBodyBytes, b2i(c.TLSExpiryEnabled), c.TLSWarningDays,
		b2i(c.InsecureSkipVerify), nullStr(c.ProxyURL), nullStr(c.IPFamily))
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
