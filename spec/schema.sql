-- Sinjal initial schema design draft.
-- Migrations remain the authoritative implementation mechanism.
-- Exact names/types may evolve before first release, but semantics must remain.

PRAGMA foreign_keys = ON;

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  login TEXT NOT NULL UNIQUE,
  display_name TEXT,
  role TEXT NOT NULL CHECK (role IN ('admin','viewer')),
  password_hash TEXT,
  totp_secret_enc BLOB,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  token_hash BLOB NOT NULL UNIQUE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  reauthenticated_at TEXT,
  user_agent TEXT,
  ip_hint TEXT
);

CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expiry ON sessions(expires_at);

CREATE TABLE passkeys (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id BLOB NOT NULL UNIQUE,
  public_key BLOB NOT NULL,
  sign_count INTEGER NOT NULL DEFAULT 0,
  transports_json TEXT,
  label TEXT,
  created_at TEXT NOT NULL,
  last_used_at TEXT
);

CREATE TABLE notification_profiles (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  quiet_hours_enabled INTEGER NOT NULL DEFAULT 0,
  quiet_start TEXT,
  quiet_end TEXT,
  critical_bypass INTEGER NOT NULL DEFAULT 1,
  reminder_after_seconds INTEGER,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE notification_channels (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('smtp','telegram','discord','webhook')),
  enabled INTEGER NOT NULL DEFAULT 1,
  config_enc BLOB NOT NULL,
  health_state TEXT NOT NULL DEFAULT 'unknown',
  last_success_at TEXT,
  last_failure_at TEXT,
  last_error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE notification_routes (
  profile_id TEXT NOT NULL REFERENCES notification_profiles(id) ON DELETE CASCADE,
  severity TEXT NOT NULL CHECK (severity IN ('info','warning','critical')),
  channel_id TEXT NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  PRIMARY KEY (profile_id, severity, channel_id)
);

CREATE TABLE monitors (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('http','tcp','icmp','dns','heartbeat')),
  enabled INTEGER NOT NULL DEFAULT 1,
  current_state TEXT NOT NULL DEFAULT 'pending'
    CHECK (current_state IN ('up','pending','down','flapping','paused','degraded')),
  current_state_since TEXT NOT NULL,
  interval_seconds INTEGER NOT NULL DEFAULT 30,
  timeout_ms INTEGER NOT NULL DEFAULT 5000,
  failure_threshold INTEGER NOT NULL DEFAULT 2,
  retry_delay_ms INTEGER NOT NULL DEFAULT 5000,
  success_threshold INTEGER NOT NULL DEFAULT 1,
  parent_monitor_id TEXT REFERENCES monitors(id) ON DELETE SET NULL,
  notification_profile_id TEXT REFERENCES notification_profiles(id) ON DELETE SET NULL,
  last_check_at TEXT,
  last_success_at TEXT,
  last_failure_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX idx_monitors_enabled ON monitors(enabled);
CREATE INDEX idx_monitors_state ON monitors(current_state);
CREATE INDEX idx_monitors_parent ON monitors(parent_monitor_id);

CREATE TABLE http_monitor_config (
  monitor_id TEXT PRIMARY KEY REFERENCES monitors(id) ON DELETE CASCADE,
  url TEXT NOT NULL,
  method TEXT NOT NULL DEFAULT 'GET',
  follow_redirects INTEGER NOT NULL DEFAULT 1,
  expected_status TEXT NOT NULL DEFAULT '200-399',
  body_contains TEXT,
  body_not_contains TEXT,
  json_assertions_json TEXT,
  headers_json TEXT,
  request_body TEXT,
  custom_user_agent TEXT,
  max_body_bytes INTEGER NOT NULL DEFAULT 1048576,
  tls_expiry_enabled INTEGER NOT NULL DEFAULT 1,
  tls_warning_days_json TEXT NOT NULL DEFAULT '[30,14,7]',
  insecure_skip_verify INTEGER NOT NULL DEFAULT 0,
  proxy_url TEXT,
  ip_family TEXT
);

CREATE TABLE monitor_secrets (
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value_enc BLOB NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (monitor_id, key)
);

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
  expected_values_json TEXT
);

CREATE TABLE heartbeat_monitor_config (
  monitor_id TEXT PRIMARY KEY REFERENCES monitors(id) ON DELETE CASCADE,
  token_hash BLOB NOT NULL UNIQUE,
  expected_interval_seconds INTEGER NOT NULL,
  grace_seconds INTEGER NOT NULL DEFAULT 0,
  last_beat_at TEXT
);

CREATE TABLE tags (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE monitor_tags (
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  PRIMARY KEY (monitor_id, tag_id)
);

CREATE TABLE check_results (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  checked_at TEXT NOT NULL,
  duration_ms REAL,
  success INTEGER NOT NULL,
  protocol_status TEXT,
  error_kind TEXT,
  error_message TEXT,
  diagnostic_snippet TEXT,
  metadata_json TEXT
);

CREATE INDEX idx_check_results_monitor_time
  ON check_results(monitor_id, checked_at DESC);

CREATE TABLE check_aggregates (
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  resolution_seconds INTEGER NOT NULL,
  bucket_start TEXT NOT NULL,
  total_count INTEGER NOT NULL,
  success_count INTEGER NOT NULL,
  failure_count INTEGER NOT NULL,
  min_ms REAL,
  max_ms REAL,
  avg_ms REAL,
  p95_ms REAL,
  PRIMARY KEY (monitor_id, resolution_seconds, bucket_start)
);

CREATE TABLE incidents (
  id TEXT PRIMARY KEY,
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  initial_failure_kind TEXT,
  summary TEXT,
  suppressed_by_parent INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);

CREATE INDEX idx_incidents_monitor_time
  ON incidents(monitor_id, started_at DESC);
CREATE INDEX idx_incidents_active
  ON incidents(monitor_id, ended_at);

CREATE TABLE incident_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  message TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE maintenance_windows (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  starts_at TEXT NOT NULL,
  duration_seconds INTEGER NOT NULL,
  recurrence TEXT NOT NULL DEFAULT 'none'
    CHECK (recurrence IN ('none','daily','weekly')),
  weekday_mask INTEGER,
  suppress_notifications INTEGER NOT NULL DEFAULT 1,
  exclude_from_adjusted_uptime INTEGER NOT NULL DEFAULT 1,
  scope_json TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE notification_deliveries (
  id TEXT PRIMARY KEY,
  incident_id TEXT REFERENCES incidents(id) ON DELETE CASCADE,
  channel_id TEXT NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  attempt INTEGER NOT NULL,
  status TEXT NOT NULL,
  attempted_at TEXT NOT NULL,
  delivered_at TEXT,
  error_message TEXT
);

CREATE TABLE status_pages (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  description TEXT,
  visibility TEXT NOT NULL
    CHECK (visibility IN ('public','authenticated','password','unlisted')),
  unlisted_token_hash BLOB,
  password_hash TEXT,
  theme TEXT NOT NULL DEFAULT 'paper',
  accent TEXT,
  logo_path TEXT,
  incident_days INTEGER NOT NULL DEFAULT 30,
  show_powered_by INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE status_page_groups (
  id TEXT PRIMARY KEY,
  status_page_id TEXT NOT NULL REFERENCES status_pages(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE status_page_monitors (
  status_page_id TEXT NOT NULL REFERENCES status_pages(id) ON DELETE CASCADE,
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  group_id TEXT REFERENCES status_page_groups(id) ON DELETE SET NULL,
  display_name TEXT NOT NULL,
  show_latency INTEGER NOT NULL DEFAULT 0,
  sort_order INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (status_page_id, monitor_id)
);

CREATE TABLE status_page_hosts (
  hostname TEXT PRIMARY KEY,
  status_page_id TEXT NOT NULL REFERENCES status_pages(id) ON DELETE CASCADE
);

CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  event_type TEXT NOT NULL,
  object_type TEXT,
  object_id TEXT,
  metadata_json TEXT,
  created_at TEXT NOT NULL
);

CREATE INDEX idx_audit_events_time ON audit_events(created_at DESC);

CREATE TABLE system_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
