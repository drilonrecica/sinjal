-- 002_auth (M1): users (with UI preferences), sessions, passkeys, audit log.
-- Definitions match spec/schema.sql. Sessions, passkeys and audit events
-- reference users; deleting a user removes its sessions and passkeys but keeps
-- audit rows (user_id becomes NULL).

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  login TEXT NOT NULL UNIQUE,
  display_name TEXT,
  role TEXT NOT NULL CHECK (role IN ('admin','viewer')),
  password_hash TEXT,
  totp_secret_enc BLOB,
  totp_last_step INTEGER, -- last accepted TOTP time step (replay prevention); NULL = none yet
  disabled INTEGER NOT NULL DEFAULT 0,
  theme TEXT CHECK (theme IN ('carbon','paper','midnight','terminal')), -- NULL = instance default
  density TEXT NOT NULL DEFAULT 'comfortable' CHECK (density IN ('comfortable','compact')),
  sidebar_collapsed INTEGER NOT NULL DEFAULT 0,
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
  backup_eligible INTEGER NOT NULL DEFAULT 0, -- BE flag at registration; must match on every assertion
  label TEXT,
  created_at TEXT NOT NULL,
  last_used_at TEXT
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
