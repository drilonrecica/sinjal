-- 004_incidents (M3): incidents with their timeline, and maintenance windows.
-- Definitions match spec/schema.sql.

CREATE TABLE incidents (
  id TEXT PRIMARY KEY,
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  initial_failure_kind TEXT,
  summary TEXT,
  suppressed_by_parent INTEGER NOT NULL DEFAULT 0,
  maintenance_overlap INTEGER NOT NULL DEFAULT 0,
  down_notified_at TEXT,
  reminder_sent_at TEXT,
  recovery_notified_at TEXT,
  created_at TEXT NOT NULL
);

CREATE INDEX idx_incidents_monitor_time
  ON incidents(monitor_id, started_at DESC);
-- At most one active incident per monitor.
CREATE UNIQUE INDEX idx_incidents_active
  ON incidents(monitor_id) WHERE ended_at IS NULL;

CREATE TABLE incident_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  message TEXT,
  published INTEGER NOT NULL DEFAULT 0, -- manual_note only: shown on status pages
  created_at TEXT NOT NULL
);

-- The timeline of one incident, in order.
CREATE INDEX idx_incident_events_incident
  ON incident_events(incident_id, id);

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

-- A monitor that is DOWN while this migration runs went down before
-- incidents existed. Give it its active incident, from the moment it was
-- declared DOWN (the first failure of that outage is no longer known), so
-- that DOWN always means one active incident.
INSERT INTO incidents (id, monitor_id, started_at, created_at)
SELECT lower(hex(randomblob(16))), id, current_state_since, current_state_since
FROM monitors WHERE current_state = 'down';
