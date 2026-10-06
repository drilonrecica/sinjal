-- 001_foundation (M0): instance-level settings. Feature tables arrive with
-- the milestone that first needs them (docs/17_MIGRATIONS_RELEASES.md).
CREATE TABLE system_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
