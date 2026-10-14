-- 010_saved_views (M8-08): per-user saved monitor filters (docs/29). A view
-- is a name, validated filters JSON (states, types, tags, text, the two
-- boolean built-ins) and a sort; names are unique per user and views go
-- with their user. Definition matches spec/schema.sql.

CREATE TABLE saved_views (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  filters_json TEXT NOT NULL,
  sort TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (user_id, name)
);
