-- 006_notifications (M5): notification channels, profile routes, delivery
-- attempts and TLS warning dedup. Definitions match spec/schema.sql.
-- notification_profiles already exists (003_monitors), because monitors
-- reference it.

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

-- One row per TLS warning threshold crossed for a given certificate.
-- Deduplicates warning notifications; a new not_after resets thresholds.
CREATE TABLE tls_warnings (
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  cert_not_after TEXT NOT NULL,
  threshold_days INTEGER NOT NULL,
  notified_at TEXT NOT NULL,
  PRIMARY KEY (monitor_id, cert_not_after, threshold_days)
);
