-- 003_monitors (M2): monitors, HTTP config, secrets, pauses, tags and raw
-- check results. Definitions match spec/schema.sql. notification_profiles is
-- created here in full because monitors reference it; channels and routes
-- arrive with M5.

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

CREATE TABLE monitors (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('http','tcp','icmp','dns','heartbeat')),
  enabled INTEGER NOT NULL DEFAULT 1,
  current_state TEXT NOT NULL DEFAULT 'pending'
    CHECK (current_state IN ('up','pending','down','paused')),
  current_state_since TEXT NOT NULL,
  flapping_since TEXT, -- FLAPPING overlay; set while flapping, survives restart (policy: docs/10)
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
  tls_not_after TEXT, -- last observed HTTPS certificate expiry (warning indicator)
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

-- Pause intervals; paused time is excluded from uptime (docs/10).
CREATE TABLE monitor_pauses (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  paused_at TEXT NOT NULL,
  resumed_at TEXT
);

CREATE INDEX idx_monitor_pauses_monitor_time
  ON monitor_pauses(monitor_id, paused_at);
-- At most one open pause per monitor.
CREATE UNIQUE INDEX idx_monitor_pauses_open
  ON monitor_pauses(monitor_id) WHERE resumed_at IS NULL;

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

