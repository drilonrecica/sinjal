-- 007_aggregates (M6): rolled-up check results for the 5m, 1h and 1d tiers.
-- Definition matches spec/schema.sql. The primary key is the upsert key, so
-- a rollup that runs twice for a bucket replaces it, and it also serves the
-- range reads (monitor, resolution, bucket_start).

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
