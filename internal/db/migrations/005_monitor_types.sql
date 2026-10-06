-- 005_monitor_types (M4): the protocol config tables for TCP, ICMP, DNS and
-- heartbeat monitors. Definitions match spec/schema.sql. The monitor type
-- CHECK already allows all five types, so no table is rebuilt.

CREATE TABLE tcp_monitor_config (
  monitor_id TEXT PRIMARY KEY REFERENCES monitors(id) ON DELETE CASCADE,
  host TEXT NOT NULL,
  port INTEGER NOT NULL
);

CREATE TABLE icmp_monitor_config (
  monitor_id TEXT PRIMARY KEY REFERENCES monitors(id) ON DELETE CASCADE,
  host TEXT NOT NULL
);

CREATE TABLE dns_monitor_config (
  monitor_id TEXT PRIMARY KEY REFERENCES monitors(id) ON DELETE CASCADE,
  hostname TEXT NOT NULL,
  query_type TEXT NOT NULL,
  resolver TEXT,
  expected_values_json TEXT,
  match_mode TEXT NOT NULL DEFAULT 'all' CHECK (match_mode IN ('any','all'))
);

CREATE TABLE heartbeat_monitor_config (
  monitor_id TEXT PRIMARY KEY REFERENCES monitors(id) ON DELETE CASCADE,
  token_hash BLOB NOT NULL UNIQUE,
  expected_interval_seconds INTEGER NOT NULL,
  grace_seconds INTEGER NOT NULL DEFAULT 0,
  source_label TEXT,
  last_beat_at TEXT
);
