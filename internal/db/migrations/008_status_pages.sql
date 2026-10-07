-- 008_status_pages (M7): public status pages, their groups, the monitors
-- they show under a public display name, and the hostnames mapped to them.
-- Definitions match spec/schema.sql. The published flag on incident notes
-- (P0-02) is incident_events.published, created in 004_incidents.

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
