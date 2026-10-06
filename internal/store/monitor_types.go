package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Monitor types (monitors.type).
const (
	TypeHTTP      = "http"
	TypeTCP       = "tcp"
	TypeICMP      = "icmp"
	TypeDNS       = "dns"
	TypeHeartbeat = "heartbeat"
)

// Types lists the monitor types in the order a form offers them.
var Types = []string{TypeHTTP, TypeTCP, TypeICMP, TypeDNS, TypeHeartbeat}

// TCPConfig is one row of tcp_monitor_config.
type TCPConfig struct {
	Host string
	Port int
}

// ICMPConfig is one row of icmp_monitor_config.
type ICMPConfig struct {
	Host string
}

// DNSConfig is one row of dns_monitor_config. Expected values are stored
// normalized and without duplicates.
type DNSConfig struct {
	Hostname  string
	QueryType string
	Resolver  string // "" for the system's nameservers
	Expected  []string
	MatchMode string // "all" or "any"
}

// HeartbeatSettings are the editable fields of heartbeat_monitor_config.
// TokenHash is set on create only; an update keeps the token and the last
// beat.
type HeartbeatSettings struct {
	ExpectedIntervalSeconds int
	GraceSeconds            int
	SourceLabel             string
	TokenHash               []byte
}

// insertConfig writes the config row of a new monitor's type.
func insertConfig(ctx context.Context, x execer, id string, m *MonitorInput) error {
	var err error
	switch m.Type {
	case TypeHTTP:
		return insertHTTPConfig(ctx, x, id, m.HTTP)
	case TypeTCP:
		_, err = x.ExecContext(ctx, `INSERT INTO tcp_monitor_config (monitor_id, host, port) VALUES (?, ?, ?)`,
			id, m.TCP.Host, m.TCP.Port)
	case TypeICMP:
		_, err = x.ExecContext(ctx, `INSERT INTO icmp_monitor_config (monitor_id, host) VALUES (?, ?)`, id, m.ICMP.Host)
	case TypeDNS:
		c := m.DNS
		_, err = x.ExecContext(ctx, `INSERT INTO dns_monitor_config
			(monitor_id, hostname, query_type, resolver, expected_values_json, match_mode) VALUES (?, ?, ?, ?, ?, ?)`,
			id, c.Hostname, c.QueryType, nullStr(c.Resolver), expectedJSON(c.Expected), c.MatchMode)
	case TypeHeartbeat:
		h := m.Heartbeat
		_, err = x.ExecContext(ctx, `INSERT INTO heartbeat_monitor_config
			(monitor_id, token_hash, expected_interval_seconds, grace_seconds, source_label) VALUES (?, ?, ?, ?, ?)`,
			id, h.TokenHash, h.ExpectedIntervalSeconds, h.GraceSeconds, nullStr(h.SourceLabel))
	default:
		err = errors.New("store: unknown monitor type " + m.Type)
	}
	return err
}

// replaceConfig rewrites the config row of an existing monitor. A
// heartbeat row is updated in place, keeping its token and last beat.
func replaceConfig(ctx context.Context, x execer, id string, m *MonitorInput) error {
	if m.Type == TypeHeartbeat {
		h := m.Heartbeat
		_, err := x.ExecContext(ctx, `UPDATE heartbeat_monitor_config SET expected_interval_seconds = ?,
			grace_seconds = ?, source_label = ? WHERE monitor_id = ?`,
			h.ExpectedIntervalSeconds, h.GraceSeconds, nullStr(h.SourceLabel), id)
		return err
	}
	table := map[string]string{
		TypeHTTP: "http_monitor_config",
		TypeTCP:  "tcp_monitor_config",
		TypeICMP: "icmp_monitor_config",
		TypeDNS:  "dns_monitor_config",
	}[m.Type]
	if table == "" {
		return errors.New("store: unknown monitor type " + m.Type)
	}
	if _, err := x.ExecContext(ctx, `DELETE FROM `+table+` WHERE monitor_id = ?`, id); err != nil {
		return err
	}
	return insertConfig(ctx, x, id, m)
}

func expectedJSON(vals []string) any {
	if len(vals) == 0 {
		return nil
	}
	b, _ := json.Marshal(vals)
	return string(b)
}

// GetTCPConfig returns the TCP config of a monitor.
func GetTCPConfig(ctx context.Context, q *sql.DB, monitorID string) (TCPConfig, error) {
	var c TCPConfig
	err := q.QueryRowContext(ctx, `SELECT host, port FROM tcp_monitor_config WHERE monitor_id = ?`, monitorID).
		Scan(&c.Host, &c.Port)
	if errors.Is(err, sql.ErrNoRows) {
		return TCPConfig{}, ErrNotFound
	}
	return c, err
}

// GetICMPConfig returns the ICMP config of a monitor.
func GetICMPConfig(ctx context.Context, q *sql.DB, monitorID string) (ICMPConfig, error) {
	var c ICMPConfig
	err := q.QueryRowContext(ctx, `SELECT host FROM icmp_monitor_config WHERE monitor_id = ?`, monitorID).Scan(&c.Host)
	if errors.Is(err, sql.ErrNoRows) {
		return ICMPConfig{}, ErrNotFound
	}
	return c, err
}

// GetDNSConfig returns the DNS config of a monitor.
func GetDNSConfig(ctx context.Context, q *sql.DB, monitorID string) (DNSConfig, error) {
	var c DNSConfig
	var resolver, expected sql.NullString
	err := q.QueryRowContext(ctx, `SELECT hostname, query_type, resolver, expected_values_json, match_mode
		FROM dns_monitor_config WHERE monitor_id = ?`, monitorID).
		Scan(&c.Hostname, &c.QueryType, &resolver, &expected, &c.MatchMode)
	if errors.Is(err, sql.ErrNoRows) {
		return DNSConfig{}, ErrNotFound
	}
	if err != nil {
		return DNSConfig{}, err
	}
	c.Resolver = resolver.String
	if expected.Valid {
		if err := json.Unmarshal([]byte(expected.String), &c.Expected); err != nil {
			return DNSConfig{}, err
		}
	}
	return c, nil
}

// targets is what each monitor checks, by monitor_id: the URL of an HTTP
// monitor, host:port (TCP), the host (ICMP), "hostname TYPE" (DNS) and the
// source label of a heartbeat monitor ("" when it has none). It never
// touches monitor_secrets or a heartbeat token hash.
const targets = `
	SELECT monitor_id, url AS target FROM http_monitor_config
	UNION ALL SELECT monitor_id, CASE WHEN instr(host, ':') > 0 THEN '[' || host || ']' ELSE host END || ':' || port
		FROM tcp_monitor_config
	UNION ALL SELECT monitor_id, host FROM icmp_monitor_config
	UNION ALL SELECT monitor_id, hostname || ' ' || query_type FROM dns_monitor_config
	UNION ALL SELECT monitor_id, coalesce(source_label, '') FROM heartbeat_monitor_config`

// Targets returns the target of every monitor (see targets), for list
// pages.
func Targets(ctx context.Context, q *sql.DB) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, targets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, t string
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

// Target returns the target of one monitor (see targets), or ErrNotFound.
func Target(ctx context.Context, q *sql.DB, monitorID string) (string, error) {
	var t string
	err := q.QueryRowContext(ctx, `SELECT target FROM (`+targets+`) WHERE monitor_id = ?`, monitorID).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return t, err
}
